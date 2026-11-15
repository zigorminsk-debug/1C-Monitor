// Package monitor содержит логику мониторинга сервера 1С: опрос процессов rphost, кластера через
// rac.exe, технологического журнала, счётчиков диска и сети, административные операции и отчёты.
// Пакет не зависит от графического интерфейса.
package monitor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level — окраска строки состояния.
type Level int

const (
	LevelNeutral Level = iota // жёлтый: запуск, пауза
	LevelOK                   // зелёный
	LevelBad                  // красный
)

// View — всё, что показывает интерфейс. Срезы после публикации не изменяются.
type View struct {
	WindowTitle         string
	ServerHeaderTitle   string
	ServerHeaderDetails string
	ServerHardwareText  string
	NetworkLoadText     string
	Status              string
	StatusLevel         Level
	RacStatus           string
	Footer              string
	AdminActionStatus   string
	AutoDrainStatus     string

	ProcessCount         int
	SessionCount         int
	ActiveSessionCount   int
	SleepingSessionCount int
	AlertCount           int

	TotalMemoryText      string
	TotalCpuText         string
	DiskUsageText        string
	DiskIoText           string
	DiskQueueText        string
	SessionBreakdownText string

	Processes []ProcessRow
	Sessions  []SessionRow
	Alerts    []AlertRow

	Running   bool
	AdminBusy bool
	// SettingsVersion растёт, когда настройки изменил сам монитор (найден rac.exe, адрес RAS).
	SettingsVersion int
	// ConfirmationResets растёт, когда монитор сбросил контрольную фразу после операции.
	ConfirmationResets int
}

// Options — зависимости монитора. Нулевые значения заменяются рабочими реализациями.
type Options struct {
	System  System
	Run     RunFunc
	DataDir string
	Now     func() time.Time
}

type cpuSample struct {
	at    time.Time
	total time.Duration
}

type historyRow struct {
	At                                        time.Time
	Pid                                       int
	Cpu, MemoryMb                             float64
	Sessions                                  int
	Users                                     string
	Risk                                      int
	Diagnosis                                 string
	DiskUsage, DiskRead, DiskWrite, DiskQueue float64
	NetworkReceive, NetworkSend, NetworkUsage float64
}

type serverHistoryRow struct {
	At                                         time.Time
	Cpu, RamMb                                 float64
	DiskUsage, DiskRead, DiskWrite, DiskQueue  float64
	NetIn, NetOut, NetUsage                    float64
	Sessions, ActiveSessions, SleepingSessions int
}

type drainState struct {
	Pid         int
	ProcessUUID string
	StartedAt   time.Time
	MemoryMb    float64
}

type dailyUserStat struct {
	Date              time.Time
	UserName          string
	InfoBase          string
	Samples           int
	Hosts             []string
	Pids              []int
	MaxProcessCpu     float64
	MaxProcessMemory  float64
	MaxCpu5Min        float64
	MaxDuration5Min   float64
	MaxDbDuration5Min float64
	MaxCalls5Min      int
	MaxMemory5MinMb   float64
	LastContext       string
}

type techContext struct {
	Context    string
	LastCallMs float64
}

// Monitor — ядро программы. Все методы потокобезопасны.
type Monitor struct {
	sys     System
	run     RunFunc
	dataDir string
	now     func() time.Time

	// Ожидания вынесены в поля, чтобы тесты не ждали реальные секунды.
	manualRacWait     time.Duration
	backgroundRacWait time.Duration
	settleDelay       time.Duration
	rasProbeInterval  time.Duration

	stop     chan struct{}
	stopOnce sync.Once

	notifyMu sync.Mutex
	onChange func()

	logMu      sync.Mutex
	logLines   []string
	logVersion int
	logOnce    map[string]time.Time

	finder racFinder

	techMu            sync.Mutex
	techFiles         []string
	techFilesAt       time.Time
	techFilesHour     int
	techContexts      map[string]techContext
	lastRacDiagnostic time.Time

	// Поля ниже принадлежат циклу обновления: он выполняется строго в одном экземпляре.
	cpu               map[int]cpuSample
	load              loadTracker
	net               netMonitor
	racPoll           chan struct{}
	lastRacPoll       time.Time
	lastTechLogScan   time.Time
	lastVolumeRefresh time.Time
	lastHistorySample time.Time
	lastPersistent    time.Time

	mu                  sync.Mutex // защищает всё, что ниже; под ним не выполняется ввод-вывод
	settings            Settings
	view                View
	busy                bool
	manualPending       bool
	adminBusy           bool
	adminConfirmation   string
	selectedPid         int
	cachedRac           *RacSnapshot
	cachedRacAt         time.Time
	loadHistory         []LoadPoint
	lastRacPath         string
	lastClusterID       string
	lastRacVersion      string
	lastInfoBases       map[string]InfoBaseRef
	history             []historyRow
	serverHistory       []serverHistoryRow
	dailyUsers          map[string]*dailyUserStat
	memoryOverSince     map[int]time.Time
	draining            map[int]drainState
	lastAlertEmission   map[string]time.Time
	lastAutomaticDrain  time.Time
	cachedVolumeSummary string
	staticSystem        string
	physicalDiskInfo    string
	virtualizationInfo  string
}

// New создаёт монитор и загружает настройки. Цикл измерений запускается методом Start.
func New(options Options) *Monitor {
	if options.System == nil {
		options.System = NewSystem()
	}
	if options.Run == nil {
		options.Run = RunProcess
	}
	if options.DataDir == "" {
		options.DataDir = DefaultDataDir()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	m := &Monitor{
		sys: options.System, run: options.Run, dataDir: options.DataDir, now: options.Now,
		manualRacWait: 30 * time.Second, backgroundRacWait: 2 * time.Second, settleDelay: 1200 * time.Millisecond, rasProbeInterval: 2 * time.Second,
		stop:    make(chan struct{}),
		logOnce: map[string]time.Time{}, techContexts: map[string]techContext{}, techFilesHour: -1,
		cpu: map[int]cpuSample{}, lastInfoBases: map[string]InfoBaseRef{}, dailyUsers: map[string]*dailyUserStat{},
		memoryOverSince: map[int]time.Time{}, draining: map[int]drainState{}, lastAlertEmission: map[string]time.Time{},
		lastRacVersion: "?", cachedVolumeSummary: "не определены",
		physicalDiskInfo: "Накопители: определение типа SSD/HDD/NVMe…",
	}
	m.settings = LoadSettings(m.dataDir)
	if strings.TrimSpace(m.settings.ReportFolder) == "" {
		m.settings.ReportFolder = m.defaultReportFolder()
	}
	manufacturer, model, ok := m.sys.BIOSInfo()
	m.virtualizationInfo = DescribeVirtualization(manufacturer, model, ok)
	machine := m.sys.MachineName()
	m.view = View{
		WindowTitle: "1C Server Monitor", ServerHeaderTitle: machine + " — сервер 1С", ServerHeaderDetails: "Определение характеристик сервера…",
		ServerHardwareText: "Определение оборудования…", NetworkLoadText: "Сеть: ожидание первого измерения…",
		Status: "Запуск…", StatusLevel: LevelNeutral, RacStatus: "Не проверено", Footer: "Ожидание первого измерения",
		AdminActionStatus: "Для опасных действий введите контрольную фразу", AutoDrainStatus: "Автовывод rphost выключен",
		TotalMemoryText: "—", TotalCpuText: "—", DiskUsageText: "—", DiskIoText: "чтение — • запись —", DiskQueueText: "очередь —",
		SessionBreakdownText: "активные — • спящие —", Running: true,
	}
	m.mu.Lock()
	m.updateServerHeaderLocked("", DiskSnapshot{}, NetworkSnapshot{})
	m.mu.Unlock()
	return m
}

func (m *Monitor) defaultReportFolder() string {
	return filepath.Join(m.sys.DocumentsDir(), "1CServerMonitor", "Reports")
}

// SetOnChange задаёт обработчик, вызываемый при любом изменении состояния. Он вызывается из
// произвольных горутин и обязан возвращать управление сразу.
func (m *Monitor) SetOnChange(handler func()) {
	m.notifyMu.Lock()
	m.onChange = handler
	m.notifyMu.Unlock()
}

func (m *Monitor) notify() {
	m.notifyMu.Lock()
	handler := m.onChange
	m.notifyMu.Unlock()
	if handler != nil {
		handler()
	}
}

// View возвращает копию отображаемого состояния.
func (m *Monitor) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.view
}

// Settings возвращает копию настроек.
func (m *Monitor) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.clone()
}

// UpdateSettings изменяет настройки в памяти (без записи на диск).
func (m *Monitor) UpdateSettings(change func(*Settings)) {
	m.mu.Lock()
	change(&m.settings)
	m.mu.Unlock()
}

func (m *Monitor) trySave(s Settings, reason string) bool {
	if err := s.Save(m.dataDir); err != nil {
		m.Log(fmt.Sprintf("Не удалось сохранить настройки (%s): %v", reason, err))
		return false
	}
	return true
}

func (m *Monitor) setStatus(text string, level Level) {
	m.mu.Lock()
	m.view.Status, m.view.StatusLevel = text, level
	m.mu.Unlock()
	m.notify()
}

func (m *Monitor) setRacStatus(text string) {
	m.mu.Lock()
	m.view.RacStatus = text
	m.mu.Unlock()
	m.notify()
}

func (m *Monitor) setAdminStatus(text string) {
	m.mu.Lock()
	m.view.AdminActionStatus = text
	m.mu.Unlock()
	m.notify()
}

// SetRunning включает или приостанавливает периодические измерения.
func (m *Monitor) SetRunning(running bool) {
	m.mu.Lock()
	if m.view.Running == running {
		m.mu.Unlock()
		return
	}
	m.view.Running = running
	if running {
		m.view.Status, m.view.StatusLevel = "Мониторинг включён", LevelOK
	} else {
		m.view.Status, m.view.StatusLevel = "Мониторинг на паузе", LevelNeutral
	}
	m.mu.Unlock()
	m.notify()
}

// SelectProcess запоминает строку, выбранную в таблице процессов (0 — выбор снят).
func (m *Monitor) SelectProcess(pid int) {
	m.mu.Lock()
	m.selectedPid = pid
	m.mu.Unlock()
}

// SelectedProcess возвращает PID выбранного процесса.
func (m *Monitor) SelectedProcess() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.selectedPid
}

// Start запускает определение накопителей и цикл измерений.
func (m *Monitor) Start() {
	if m.sys.IsWindows() && wow64Warning != "" {
		m.Log(wow64Warning)
	}
	go m.loadPhysicalDiskInfo()
	go m.loop()
}

// Close останавливает цикл и освобождает системные ресурсы.
func (m *Monitor) Close() {
	m.stopOnce.Do(func() {
		close(m.stop)
		m.sys.Close()
	})
}

func (m *Monitor) stopped() bool {
	select {
	case <-m.stop:
		return true
	default:
		return false
	}
}

func (m *Monitor) loop() {
	select {
	case <-m.stop:
		return
	case <-time.After(400 * time.Millisecond):
	}
	for !m.stopped() {
		m.mu.Lock()
		running := m.view.Running
		interval := math.Max(2, m.settings.IntervalSeconds)
		m.mu.Unlock()
		if running {
			m.Refresh(false)
		}
		select {
		case <-m.stop:
			return
		case <-time.After(time.Duration(interval * float64(time.Second))):
		}
	}
}

// Refresh выполняет цикл измерений. Ручной запрос во время идущего цикла не теряется,
// а выполняется сразу после него (вызов при этом возвращается немедленно).
func (m *Monitor) Refresh(manual bool) {
	m.mu.Lock()
	if m.busy {
		if manual {
			m.manualPending = true
		}
		m.mu.Unlock()
		return
	}
	m.busy = true
	m.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			m.mu.Lock()
			m.busy = false
			m.view.Status, m.view.StatusLevel = "Ошибка мониторинга", LevelBad
			m.mu.Unlock()
			m.Log(fmt.Sprintf("Ошибка цикла мониторинга: %v", r))
		}
	}()
	for {
		m.mu.Lock()
		forced := manual || m.manualPending
		manual, m.manualPending = false, false
		m.mu.Unlock()

		m.refreshCore(forced)

		m.mu.Lock()
		if !m.manualPending || m.stopped() {
			m.busy = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func waitFor(done <-chan struct{}, limit time.Duration) bool {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func seconds(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

func (m *Monitor) refreshCore(manual bool) {
	now := m.now()
	s := m.Settings()

	// Процессы, счётчики диска и сети, тома.
	local := m.sampleLocalProcesses()
	programs, cpuTotal, loadMeasured := m.sampleLoad()
	disk := m.sys.DiskSample()
	counters, _ := m.sys.NetworkCounters()
	network := m.net.sample(counters, time.Now())
	if time.Since(m.lastVolumeRefresh) >= time.Minute {
		summary := VolumeSummary(m.sys.Volumes())
		m.mu.Lock()
		m.cachedVolumeSummary = summary
		m.mu.Unlock()
		m.lastVolumeRefresh = time.Now()
	}

	// RAC опрашивается отдельной горутиной. Фоновый такт ждёт её не дольше 2 с, поэтому зависший
	// или недоступный RAS не останавливает локальные метрики; результат подхватит следующий такт.
	// Ручное обновление ждёт свежие данные до 30 с.
	if manual && m.racPoll != nil {
		waitFor(m.racPoll, m.manualRacWait)
	}
	if m.racPoll != nil && isClosed(m.racPoll) {
		m.racPoll = nil
	}
	// Повтор только по интервалу: при отсутствии подключения RAC не перезапускается на каждом такте.
	racDue := manual || now.Sub(m.lastRacPoll) >= seconds(math.Max(5, s.RacPollSeconds))
	if racDue && m.racPoll == nil {
		m.lastRacPoll = now
		scanTechLog := manual || now.Sub(m.lastTechLogScan) >= seconds(math.Max(15, s.TechLogPollSeconds))
		if scanTechLog {
			m.lastTechLogScan = now
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			m.pollRac(scanTechLog)
		}()
		m.racPoll = done
		wait := m.backgroundRacWait
		if manual {
			wait = m.manualRacWait
		}
		if waitFor(done, wait) {
			m.racPoll = nil
		}
	}
	m.mu.Lock()
	rac, selectedRac := m.cachedRac, m.lastRacPath
	m.mu.Unlock()

	procs, sessions := BuildRows(local, rac, s)

	if candidate := m.evaluateGracefulDrain(procs, sessions, now, s); candidate >= 0 {
		// Пауза включается и после неудачной попытки: иначе при ошибке RAC команда и запись
		// в журнал повторялись бы на каждом такте.
		if m.turnOffProcess(procs[candidate], true) {
			procs[candidate].DrainStatus = "Выводится с " + time.Now().Format("15:04:05")
		}
		m.mu.Lock()
		m.lastAutomaticDrain = now
		m.mu.Unlock()
	}

	m.mu.Lock()
	if now.Sub(m.lastHistorySample) >= 10*time.Second {
		for _, p := range procs {
			m.history = append(m.history, historyRow{now, p.Pid, p.Cpu, p.MemoryMb, p.SessionCount, p.Users, p.Risk, p.Diagnosis,
				disk.UsagePercent, disk.ReadBytesPerSec, disk.WriteBytesPerSec, disk.QueueLength, network.ReceiveBytesPerSec, network.SendBytesPerSec, network.UtilizationPercent})
		}
		if len(m.history) > 12000 {
			m.history = append([]historyRow(nil), m.history[len(m.history)-12000:]...)
		}
		m.lastHistorySample = now
	}
	persist := now.Sub(m.lastPersistent) >= seconds(math.Max(30, s.HistoryWriteSeconds))
	if persist {
		m.accumulateDailyStatsLocked(now, procs, sessions, disk, network)
		m.lastPersistent = now
	}
	historyCount := len(m.history)
	m.mu.Unlock()
	if persist {
		m.appendDailyHistory(procs, disk, network, now)
	}

	// Сначала сеансы с блокировками, затем активные, затем спящие; внутри групп — по имени пользователя.
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.IsBlocked() != b.IsBlocked() {
			return a.IsBlocked()
		}
		if a.IsSleeping != b.IsSleeping {
			return !a.IsSleeping
		}
		return strings.ToLower(a.UserName) < strings.ToLower(b.UserName)
	})

	active, sleeping := 0, 0
	for _, x := range sessions {
		if x.IsSleeping {
			sleeping++
		} else {
			active++
		}
	}
	var deviations []string
	if disk.Available && disk.UsagePercent >= s.DiskAlert {
		deviations = append(deviations, fmt.Sprintf("диск %s%% ≥ порога %s%% (ср. очередь %s)", num(disk.UsagePercent, 0), num(s.DiskAlert, 0), diskQueueLabel(disk)))
	}
	totalMemory, totalCpu := 0.0, 0.0
	for _, p := range procs {
		totalMemory += p.MemoryMb
		totalCpu += p.Cpu
		if p.Risk >= 50 {
			deviations = append(deviations, fmt.Sprintf("PID %d: %s", p.Pid, p.Diagnosis))
		}
	}
	for _, x := range sessions {
		if x.IsBlocked() {
			deviations = append(deviations, fmt.Sprintf("блокировка: %s, %s, PID %d", x.UserName, x.InfoBase, x.Pid))
		}
	}

	m.mu.Lock()
	v := &m.view
	v.Processes, v.Sessions = procs, sessions
	v.ProcessCount, v.SessionCount = len(procs), len(sessions)
	v.ActiveSessionCount, v.SleepingSessionCount = active, sleeping
	v.SessionBreakdownText = fmt.Sprintf("активные %d • спящие %d", active, sleeping)
	v.AlertCount = len(deviations)
	v.TotalMemoryText, v.TotalCpuText = FormatMb(totalMemory), num(totalCpu, 1)+" %"
	if disk.Available {
		v.DiskUsageText = num(disk.UsagePercent, 1) + " %"
		v.DiskIoText = "чтение " + FormatRate(disk.ReadBytesPerSec) + " • запись " + FormatRate(disk.WriteBytesPerSec)
		v.DiskQueueText = "средняя очередь " + diskQueueLabel(disk)
	} else {
		v.DiskUsageText, v.DiskIoText, v.DiskQueueText = "нет данных", "счётчики недоступны", "Windows PDH"
	}
	if len(deviations) > 0 {
		shown := deviations
		more := ""
		if len(shown) > 3 {
			shown, more = shown[:3], "; … подробнее на вкладке «Обзор»"
		}
		v.Status, v.StatusLevel = fmt.Sprintf("Отклонения: %d · %s%s", len(deviations), strings.Join(shown, "; "), more), LevelBad
	} else {
		v.Status, v.StatusLevel = "Нагрузка в норме", LevelOK
	}
	v.Footer = fmt.Sprintf("Последнее измерение: %s • логических CPU: %d • история: %d точек", now.Format("02.01.2006 15:04:05"), m.sys.LogicalCPUs(), historyCount)
	m.updateServerHeaderLocked(selectedRac, disk, network)
	m.mu.Unlock()

	if loadMeasured { // самое первое измерение не с чем сравнить: загрузка процессора ещё неизвестна
		m.recordLoadPoint(now, programs, cpuTotal, procs, sessions, active, disk, deviations)
	}
	m.addAlerts(procs, sessions, disk, now, s)
	m.checkAutomaticDailyReport(now)
	if manual {
		m.Log(fmt.Sprintf("Ручное обновление: rphost=%d, сеансы=%d", len(procs), len(sessions)))
	}
	m.notify()
}

func (m *Monitor) sampleLocalProcesses() []ProcessRow {
	infos, err := m.sys.Processes("rphost")
	if err != nil {
		m.LogOnce("processes:"+err.Error(), "Не удалось получить список процессов rphost: "+err.Error(), 10*time.Minute)
	}
	now := m.now()
	cpus := float64(m.sys.LogicalCPUs())
	if cpus < 1 {
		cpus = 1
	}
	rows := make([]ProcessRow, 0, len(infos))
	alive := map[int]bool{}
	for _, info := range infos {
		cpu := 0.0
		if info.CPUTimeOK {
			if old, ok := m.cpu[info.PID]; ok {
				if sec := now.Sub(old.at).Seconds(); sec > 0 {
					cpu = math.Max(0, math.Min(100, (info.CPUTime-old.total).Seconds()/sec/cpus*100))
				}
			}
			m.cpu[info.PID] = cpuSample{now, info.CPUTime}
		}
		alive[info.PID] = true
		rows = append(rows, ProcessRow{Pid: info.PID, Cpu: cpu, MemoryMb: float64(info.WorkingSet) / 1048576, DrainStatus: "Работает"})
	}
	for pid := range m.cpu {
		if !alive[pid] {
			delete(m.cpu, pid)
		}
	}
	return rows
}

// BuildRows объединяет локальные процессы rphost со снимком кластера: показатели RAC, привязка
// сеансов к процессам, оценка риска. Возвращает строки процессов (по убыванию риска и памяти)
// и копию сеансов с нагрузкой их процесса.
func BuildRows(local []ProcessRow, rac *RacSnapshot, s Settings) ([]ProcessRow, []SessionRow) {
	var sessions []SessionRow
	metrics := map[int]RacProcess{}
	if rac != nil {
		sessions = append([]SessionRow(nil), rac.Sessions...)
		metrics = rac.Processes
	}
	byPid := map[int][]int{}
	for i, x := range sessions {
		byPid[x.Pid] = append(byPid[x.Pid], i)
	}
	users := func(linked []int, placeholder bool) string {
		var names []string
		seen := map[string]bool{}
		for _, i := range linked {
			name := sessions[i].UserName
			if placeholder && strings.TrimSpace(name) == "" {
				name = UnknownUser
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
			if len(names) == 8 {
				break
			}
		}
		return strings.Join(names, ", ")
	}
	// ОС учитывает CPU/RAM на уровне rphost, а не отдельного сеанса. Поэтому показывается точная
	// нагрузка процесса и отдельно — оценочная доля CPU с большим весом активных сеансов.
	applyLoad := func(p ProcessRow, linked []int) {
		weight := func(x SessionRow) float64 {
			if x.IsSleeping {
				return 0.05
			}
			return 1
		}
		total := 0.0
		for _, i := range linked {
			total += weight(sessions[i])
		}
		for _, i := range linked {
			x := &sessions[i]
			x.ProcessCpu, x.ProcessMemoryMb = p.Cpu, p.MemoryMb
			if total > 0 {
				x.EstimatedCpu = p.Cpu * weight(*x) / total
			} else {
				x.EstimatedCpu = 0
			}
		}
	}
	hasBlocked := func(linked []int) bool {
		for _, i := range linked {
			if sessions[i].IsBlocked() {
				return true
			}
		}
		return false
	}

	procs := append([]ProcessRow(nil), local...)
	localPids := map[int]bool{}
	for i := range procs {
		p := &procs[i]
		localPids[p.Pid] = true
		if rp, ok := metrics[p.Pid]; ok {
			p.ProcessUUID, p.AvgCallMs, p.AvgDbMs, p.RacMemoryMb = rp.ProcessUUID, rp.AvgCallMs, rp.AvgDbMs, rp.MemoryMb
		}
		linked := byPid[p.Pid]
		applyLoad(*p, linked)
		p.SessionCount = len(linked)
		p.Users = users(linked, true)
		score(p, hasBlocked(linked), s)
	}
	// Процессы, которые видит кластер, но не видит ОС (удалённый рабочий сервер).
	var remote []int
	for pid := range metrics {
		if !localPids[pid] {
			remote = append(remote, pid)
		}
	}
	sort.Ints(remote)
	for _, pid := range remote {
		rp := metrics[pid]
		linked := byPid[pid]
		p := ProcessRow{Pid: rp.Pid, ProcessUUID: rp.ProcessUUID, MemoryMb: rp.MemoryMb, RacMemoryMb: rp.MemoryMb, AvgCallMs: rp.AvgCallMs, AvgDbMs: rp.AvgDbMs,
			SessionCount: len(linked), Users: users(linked, false), DrainStatus: "Работает"}
		applyLoad(p, linked)
		score(&p, hasBlocked(linked), s)
		procs = append(procs, p)
	}
	sort.SliceStable(procs, func(i, j int) bool {
		if procs[i].Risk != procs[j].Risk {
			return procs[i].Risk > procs[j].Risk
		}
		return procs[i].MemoryMb > procs[j].MemoryMb
	})
	return procs, sessions
}

func score(p *ProcessRow, blocked bool, s Settings) {
	var reasons []string
	risk := 0
	switch {
	case p.Cpu >= s.CpuAlert:
		risk += 45
		reasons = append(reasons, "высокий CPU "+num(p.Cpu, 0)+"%")
	case p.Cpu >= s.CpuAlert*0.7:
		risk += 20
		reasons = append(reasons, "повышенный CPU")
	}
	switch {
	case p.MemoryMb >= s.MemoryAlertMb:
		risk += 40
		reasons = append(reasons, "RAM "+FormatMb(p.MemoryMb))
	case p.MemoryMb >= s.MemoryAlertMb*0.75:
		risk += 15
		reasons = append(reasons, "RAM близка к порогу")
	}
	if p.AvgCallMs >= s.CallAlertMs {
		risk += 35
		reasons = append(reasons, "долгие вызовы "+num(p.AvgCallMs, 0)+" мс")
	}
	if blocked {
		risk += 45
		reasons = append(reasons, "есть блокировки")
	}
	if p.SessionCount > 100 {
		risk += 15
		reasons = append(reasons, fmt.Sprintf("много сеансов: %d", p.SessionCount))
	}
	if risk > 100 {
		risk = 100
	}
	p.Risk = risk
	if len(reasons) == 0 {
		p.Diagnosis = "Норма"
	} else {
		p.Diagnosis = strings.Join(reasons, "; ")
	}
}

func (m *Monitor) addAlerts(procs []ProcessRow, sessions []SessionRow, disk DiskSnapshot, now time.Time, s Settings) {
	type candidate struct {
		key string
		row AlertRow
	}
	var candidates []candidate
	if disk.Available && disk.UsagePercent >= s.DiskAlert {
		severity := "ВНИМАНИЕ"
		if disk.UsagePercent >= 98 {
			severity = "КРИТИЧНО"
		}
		candidates = append(candidates, candidate{"disk", AlertRow{now, severity, "Диск", fmt.Sprintf("Загрузка %s%% за интервал, чтение %s, запись %s, средняя очередь %s",
			num(disk.UsagePercent, 1), FormatRate(disk.ReadBytesPerSec), FormatRate(disk.WriteBytesPerSec), diskQueueLabel(disk))}})
	}
	for _, p := range procs {
		if p.Risk < 50 {
			continue
		}
		severity := "ВНИМАНИЕ"
		if p.Risk >= 80 {
			severity = "КРИТИЧНО"
		}
		message := p.Diagnosis
		if p.Users != "" {
			message += "; пользователи: " + p.Users
		}
		candidates = append(candidates, candidate{fmt.Sprintf("pid:%d", p.Pid), AlertRow{now, severity, fmt.Sprintf("PID %d", p.Pid), message}})
	}
	for _, x := range sessions {
		if x.IsBlocked() {
			candidates = append(candidates, candidate{fmt.Sprintf("block:%s:%d", x.SessionUUID, x.Pid), AlertRow{now, "БЛОКИРОВКА", x.UserName, fmt.Sprintf("База %s, PID %d, %s", x.InfoBase, x.Pid, x.Blocked)}})
		}
	}
	repeatAfter := seconds(math.Max(60, s.AlertRepeatSeconds))
	m.mu.Lock()
	var add []AlertRow
	for _, item := range candidates {
		if last, ok := m.lastAlertEmission[item.key]; ok && now.Sub(last) < repeatAfter {
			continue
		}
		m.lastAlertEmission[item.key] = now
		add = append(add, item.row)
	}
	for key, at := range m.lastAlertEmission {
		if now.Sub(at) > 24*time.Hour {
			delete(m.lastAlertEmission, key)
		}
	}
	if len(add) > 0 {
		if len(add) > 20 {
			add = add[:20]
		}
		alerts := append(add, m.view.Alerts...)
		if len(alerts) > 100 {
			alerts = alerts[:100]
		}
		m.view.Alerts = alerts
	}
	m.mu.Unlock()
}

// DescribeVirtualization определяет тип сервера по производителю и модели из BIOS.
func DescribeVirtualization(manufacturer, model string, ok bool) string {
	if !ok {
		return "Тип сервера не определён"
	}
	manufacturer, model = strings.TrimSpace(manufacturer), strings.TrimSpace(model)
	text := strings.ToLower(manufacturer + " " + model)
	has := func(s string) bool { return strings.Contains(text, s) }
	platform := ""
	switch {
	case has("vmware"):
		platform = "VMware"
	case has("virtualbox") || has("innotek"):
		platform = "VirtualBox"
	case has("virtual machine") && has("microsoft"):
		platform = "Microsoft Hyper-V"
	case has("kvm") || has("qemu"):
		platform = "KVM/QEMU"
	case has("xen"):
		platform = "Xen"
	case has("amazon") || has("ec2"):
		platform = "Amazon EC2"
	case has("google"):
		platform = "Google Compute Engine"
	case has("parallels"):
		platform = "Parallels"
	}
	if platform != "" {
		return "Виртуальный сервер · " + platform + " · " + manufacturer + " " + model
	}
	return "Физический сервер · " + manufacturer + " " + model
}

// VolumeSummary описывает занятое и свободное место на локальных томах.
func VolumeSummary(volumes []Volume) string {
	if len(volumes) == 0 {
		return "не определены"
	}
	parts := make([]string, 0, len(volumes))
	for _, v := range volumes {
		total, free := float64(v.Total)/1073741824, float64(v.Free)/1073741824
		parts = append(parts, fmt.Sprintf("%s занято %s/%s ГБ, свободно %s ГБ", strings.TrimRight(v.Name, `\`), num(total-free, 1), num(total, 1), num(free, 1)))
	}
	return strings.Join(parts, "; ")
}

func (m *Monitor) physicalMemoryText() string {
	total, available, ok := m.sys.PhysicalMemory()
	if !ok {
		return "не определена"
	}
	return num(float64(total)/1073741824, 1) + " ГБ, свободно " + num(float64(available)/1073741824, 1) + " ГБ"
}

func (m *Monitor) updateServerHeaderLocked(racPath string, disk DiskSnapshot, network NetworkSnapshot) {
	machine := m.sys.MachineName()
	v := &m.view
	v.WindowTitle = machine + " — 1C Server Monitor"
	v.ServerHeaderTitle = machine + " — сервер 1С"
	if m.staticSystem == "" {
		m.staticSystem = fmt.Sprintf("%s · %s · %s · %d лог. CPU", strings.TrimSpace(m.sys.OSDescription()), m.sys.OSArchitecture(), m.sys.CPUDescription(), m.sys.LogicalCPUs())
	}
	oneC := "1С/RAC: не подключён"
	if strings.TrimSpace(racPath) != "" {
		oneC = fmt.Sprintf("1С/RAC %s (%s)", m.lastRacVersion, ArchitectureLabel(racPath))
	}
	diskText := "диск: ожидание данных"
	if disk.Available {
		diskText = fmt.Sprintf("диск %s%% · R %s · W %s · ср. очередь %s", num(disk.UsagePercent, 0), FormatRate(disk.ReadBytesPerSec), FormatRate(disk.WriteBytesPerSec), diskQueueLabel(disk))
	}
	if network.Available {
		v.NetworkLoadText = fmt.Sprintf("Сеть: ↓ %s · ↑ %s · загрузка %s%% · адаптеров %d", FormatRate(network.ReceiveBytesPerSec), FormatRate(network.SendBytesPerSec), num(network.UtilizationPercent, 1), network.InterfaceCount)
	} else {
		v.NetworkLoadText = "Сеть: ожидание второго измерения"
	}
	v.ServerHardwareText = m.virtualizationInfo + " · " + m.physicalDiskInfo + " · Тома: " + m.cachedVolumeSummary
	v.ServerHeaderDetails = fmt.Sprintf("%s · RAM %s\n%s\n%s · RAS %s · rphost %d · %s · %s", m.staticSystem, m.physicalMemoryText(), v.ServerHardwareText, oneC, m.settings.Endpoint, v.ProcessCount, diskText, v.NetworkLoadText)
}

const physicalDiskScript = "[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new(); if(Get-Command Get-PhysicalDisk -ErrorAction SilentlyContinue){ Get-PhysicalDisk | ForEach-Object { $kind=if($_.BusType -eq 'NVMe'){'NVMe'}elseif($_.MediaType -eq 'SSD'){'SSD'}elseif($_.MediaType -eq 'HDD'){'HDD'}else{'тип не определён'}; '{0} [{1}, {2}, {3:N1} ГБ]' -f $_.FriendlyName,$kind,$_.BusType,($_.Size/1GB) } } else { Get-WmiObject Win32_DiskDrive | ForEach-Object { $kind=if($_.Model -match 'NVMe'){'NVMe'}elseif($_.Model -match 'SSD'){'SSD'}else{'HDD'}; '{0} [{1}, {2}, {3:N1} ГБ]' -f $_.Model,$kind,$_.InterfaceType,($_.Size/1GB) } }"

func (m *Monitor) loadPhysicalDiskInfo() {
	if !m.sys.IsWindows() {
		return
	}
	info := "Накопители: тип не определён"
	output, err := m.run("powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", physicalDiskScript}, 60*time.Second)
	if err != nil {
		m.Log("Определение SSD/HDD/NVMe: " + err.Error())
	} else {
		var disks []string
		for _, line := range strings.Split(strings.ReplaceAll(output, "\r", ""), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				disks = append(disks, line)
			}
		}
		if len(disks) > 0 {
			info = "Накопители: " + strings.Join(disks, "; ")
		}
	}
	m.mu.Lock()
	m.physicalDiskInfo = info
	m.mu.Unlock()
}

// OpenURI открывает ссылку или файл программой по умолчанию.
func (m *Monitor) OpenURI(uri string) {
	if err := m.sys.Open(uri); err != nil {
		m.Log("Не удалось открыть ссылку: " + err.Error())
	}
}

type netMonitor struct {
	previous map[string][2]uint64
	last     time.Time
}

// sample вычисляет скорость приёма и передачи по разности накопительных счётчиков адаптеров.
func (n *netMonitor) sample(counters []NicCounters, now time.Time) NetworkSnapshot {
	if n.previous == nil {
		n.previous = map[string][2]uint64{}
	}
	var received, sent, speed float64
	for _, nic := range counters {
		speed += math.Max(0, nic.Speed)
		if old, ok := n.previous[nic.ID]; ok {
			if nic.Received >= old[0] {
				received += float64(nic.Received - old[0])
			}
			if nic.Sent >= old[1] {
				sent += float64(nic.Sent - old[1])
			}
		}
		n.previous[nic.ID] = [2]uint64{nic.Received, nic.Sent}
	}
	elapsed := 0.0
	if !n.last.IsZero() {
		elapsed = now.Sub(n.last).Seconds()
	}
	n.last = now
	if elapsed <= 0 {
		return NetworkSnapshot{}
	}
	received /= elapsed
	sent /= elapsed
	utilization := 0.0
	if speed > 0 {
		utilization = math.Max(0, math.Min(100, (received+sent)*8/speed*100))
	}
	return NetworkSnapshot{true, received, sent, utilization, len(counters)}
}

func (m *Monitor) writeDiagnostic(name, text string) (string, error) {
	if err := os.MkdirAll(m.dataDir, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(m.dataDir, name)
	return file, os.WriteFile(file, append([]byte{0xEF, 0xBB, 0xBF}, text...), 0o644)
}
