package monitor

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxLoadPoints — длина истории для графиков: 6 часов при измерениях раз в 5 секунд.
const maxLoadPoints = 4320

// ProgramLoad — нагрузка программы Windows; процессы с одинаковым именем образа сложены.
type ProgramLoad struct {
	Name     string  // имя образа, например sqlservr.exe
	Count    int     // количество процессов
	Cpu      float64 // % от всех логических процессоров
	MemoryMb float64 // рабочий набор
	ReadBps  float64 // ввод-вывод процесса: диск, сеть и каналы вместе
	WriteBps float64
	OneC     bool // процесс сервера 1С: rphost, rmngr, ragent, ras
}

func (p ProgramLoad) CpuText() string    { return num(p.Cpu, 1) + " %" }
func (p ProgramLoad) MemoryText() string { return FormatMb(p.MemoryMb) }
func (p ProgramLoad) ReadText() string   { return FormatRate(p.ReadBps) }
func (p ProgramLoad) WriteText() string  { return FormatRate(p.WriteBps) }

// Label — имя программы с числом процессов, если их несколько.
func (p ProgramLoad) Label() string {
	if p.Count > 1 {
		return fmt.Sprintf("%s ×%d", p.Name, p.Count)
	}
	return p.Name
}

// ActionLoad — сеанс 1С, работавший в момент измерения.
type ActionLoad struct {
	UserName      string
	InfoBase      string
	AppID         string
	Host          string
	Pid           int
	State         string
	Blocked       string
	CurrentCallMs float64
	CurrentDbMs   float64
	EstimatedCpu  float64
	Calls5Min     int
	CpuTime5Min   float64
	MemoryMb      float64
	Operation     string
	TechContext   string
	DbRequest     string
}

// IsBlocked сообщает, ждал ли сеанс блокировки.
func (a ActionLoad) IsBlocked() bool { return a.Blocked != "" && a.Blocked != NotBlocked }

func (a ActionLoad) User() string {
	if strings.TrimSpace(a.UserName) == "" {
		return UnknownUser
	}
	return a.UserName
}

func (a ActionLoad) PidText() string {
	if a.Pid > 0 {
		return itoa(a.Pid)
	}
	return "—"
}

func (a ActionLoad) CallText() string { return durationText(a.CurrentCallMs) }
func (a ActionLoad) DbText() string   { return durationText(a.CurrentDbMs) }

func (a ActionLoad) CpuText() string {
	if a.Pid > 0 {
		return "≈ " + num(a.EstimatedCpu, 1) + " %"
	}
	return "—"
}

// What — что делал сеанс: контекст техжурнала, иначе метод RAC, иначе запрос СУБД.
func (a ActionLoad) What() string {
	for _, text := range []string{a.TechContext, a.Operation, a.DbRequest} {
		if strings.TrimSpace(text) != "" {
			return text
		}
	}
	return "действие не определено (нет данных RAC и техжурнала)"
}

func durationText(ms float64) string {
	if ms > 0 {
		return num(ms/1000, 1) + " с"
	}
	return "—"
}

// LoadPoint — одно измерение для графиков нагрузки вместе с тем, что работало в этот момент.
// После публикации точка не изменяется.
type LoadPoint struct {
	At time.Time

	CpuTotal float64 // % всего сервера; отрицательное значение — нет данных
	Cpu1C    float64 // % процессов сервера 1С

	MemoryTotalMb float64 // 0 — нет данных
	MemoryUsedMb  float64
	Memory1CMb    float64

	DiskOK    bool
	DiskUsage float64 // % времени активности физических дисков
	DiskRead  float64 // байт/с
	DiskWrite float64
	DiskQueue float64

	Sessions       int
	ActiveSessions int

	// AllPrograms — false, если список процессов Windows недоступен и в Programs только rphost.
	AllPrograms bool
	Programs    []ProgramLoad // самые нагруженные по CPU, памяти и вводу-выводу, по убыванию CPU
	Processes   []ProcessRow  // рабочие процессы rphost
	Actions     []ActionLoad  // выполнявшиеся сеансы 1С
	// RacAge — возраст сведений о сеансах; отрицательное значение — RAC не отвечал.
	RacAge     time.Duration
	Deviations []string
}

func (p LoadPoint) TimeText() string { return p.At.Format("02.01.2006 15:04:05") }

// MemoryPercent — доля занятой памяти сервера, либо -1.
func (p LoadPoint) MemoryPercent() float64 {
	if p.MemoryTotalMb <= 0 {
		return -1
	}
	return p.MemoryUsedMb / p.MemoryTotalMb * 100
}

// Memory1CPercent — доля памяти сервера, занятая процессами 1С, либо -1.
func (p LoadPoint) Memory1CPercent() float64 {
	if p.MemoryTotalMb <= 0 {
		return -1
	}
	return p.Memory1CMb / p.MemoryTotalMb * 100
}

func (p LoadPoint) CpuText() string {
	if p.CpuTotal < 0 {
		return "1С " + num(p.Cpu1C, 1) + " %"
	}
	return "сервер " + num(p.CpuTotal, 1) + " % · 1С " + num(p.Cpu1C, 1) + " %"
}

func (p LoadPoint) MemoryText() string {
	if p.MemoryTotalMb <= 0 {
		return "1С " + FormatMb(p.Memory1CMb)
	}
	return "занято " + FormatMb(p.MemoryUsedMb) + " из " + FormatMb(p.MemoryTotalMb) + " (" + num(p.MemoryPercent(), 0) + " %) · 1С " + FormatMb(p.Memory1CMb)
}

func (p LoadPoint) DiskText() string {
	if !p.DiskOK {
		return "счётчики недоступны"
	}
	return "активность " + num(p.DiskUsage, 1) + " % · очередь " + num(p.DiskQueue, 2) + " · чтение " + FormatRate(p.DiskRead) + " · запись " + FormatRate(p.DiskWrite)
}

func topPrograms(programs []ProgramLoad, limit int, value func(ProgramLoad) float64, minimum float64, text func(ProgramLoad) string) string {
	sorted := append([]ProgramLoad(nil), programs...)
	sort.SliceStable(sorted, func(i, j int) bool { return value(sorted[i]) > value(sorted[j]) })
	var parts []string
	for _, program := range sorted {
		if len(parts) == limit || value(program) < minimum {
			break
		}
		parts = append(parts, program.Label()+" — "+text(program))
	}
	return strings.Join(parts, "; ")
}

// Explain описывает словами, что создавало нагрузку в момент измерения.
func (p LoadPoint) Explain() []string {
	var lines []string
	add := func(text, top string) {
		if top != "" {
			text += " Больше всего: " + top + "."
		}
		lines = append(lines, text)
	}
	add("Процессор: "+p.CpuText()+".", topPrograms(p.Programs, 3, func(x ProgramLoad) float64 { return x.Cpu }, 1, ProgramLoad.CpuText))
	add("Память: "+p.MemoryText()+".", topPrograms(p.Programs, 3, func(x ProgramLoad) float64 { return x.MemoryMb }, 64, ProgramLoad.MemoryText))
	add("Диск: "+p.DiskText()+".", topPrograms(p.Programs, 3, func(x ProgramLoad) float64 { return x.ReadBps + x.WriteBps }, 65536,
		func(x ProgramLoad) string { return "чтение " + x.ReadText() + ", запись " + x.WriteText() }))

	hot := append([]ProcessRow(nil), p.Processes...)
	sort.SliceStable(hot, func(i, j int) bool { return hot[i].Cpu > hot[j].Cpu })
	shown := 0
	for _, process := range hot {
		if shown == 3 || (process.Cpu < 5 && process.Risk < 50) {
			continue
		}
		shown++
		text := fmt.Sprintf("rphost PID %d: CPU %s, RAM %s, сеансов %d", process.Pid, process.CpuText(), process.MemoryText(), process.SessionCount)
		if process.Risk >= 20 && process.Diagnosis != "" {
			text += " — " + process.Diagnosis
		}
		if process.Users != "" {
			text += ". Пользователи: " + process.Users
		}
		lines = append(lines, text+".")
	}

	if p.RacAge > 90*time.Second {
		lines = append(lines, "Сведения о сеансах 1С получены за "+ageText(p.RacAge)+" до этого момента: RAC не отвечал, действия могли измениться.")
	}
	switch {
	case p.RacAge < 0:
		lines = append(lines, "Действия 1С: кластер не отвечал через RAC, сведений о сеансах на этот момент нет.")
	case len(p.Actions) == 0:
		lines = append(lines, "Действия 1С: выполнявшихся сеансов не было.")
	default:
		for i, action := range p.Actions {
			if i == 3 {
				break
			}
			text := "Действие 1С: " + action.User() + " (" + action.InfoBase + ", rphost " + action.PidText() + ") — " + action.What()
			if action.CurrentCallMs > 0 {
				text += ", вызов " + action.CallText()
				if action.CurrentDbMs > 0 {
					text += ", из них СУБД " + action.DbText()
				}
			}
			if action.IsBlocked() {
				text += ", блокировка: " + action.Blocked
			}
			lines = append(lines, text+".")
		}
	}
	for _, deviation := range p.Deviations {
		lines = append(lines, "Отклонение: "+deviation+".")
	}
	return lines
}

func ageText(age time.Duration) string {
	if age >= time.Hour {
		return num(age.Hours(), 1) + " ч"
	}
	if age >= 2*time.Minute {
		return num(age.Minutes(), 0) + " мин"
	}
	return num(age.Seconds(), 0) + " с"
}

var oneCServerImages = map[string]bool{"rphost.exe": true, "rmngr.exe": true, "ragent.exe": true, "ras.exe": true}

type procCounters struct {
	name        string
	cpu         time.Duration
	read, write uint64
}

// loadTracker превращает накопительные счётчики процессов в нагрузку за интервал между измерениями.
type loadTracker struct {
	at          time.Time
	procs       map[int]procCounters
	idle, total time.Duration
	timesOK     bool
}

// sample возвращает нагрузку всех программ (по убыванию CPU) и загрузку процессора сервера
// (отрицательное значение — нет данных). Для первого измерения интервала ещё нет: скорости
// неизвестны, и measured равно false.
func (t *loadTracker) sample(all []ProcInfo, idle, total time.Duration, timesOK bool, at time.Time, cpus int) (programs []ProgramLoad, cpuTotal float64, measured bool) {
	if cpus < 1 {
		cpus = 1
	}
	elapsed := at.Sub(t.at).Seconds()
	first := t.procs == nil || elapsed <= 0
	clamp := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 100 {
			return 100
		}
		return v
	}

	groups := map[string]*ProgramLoad{}
	var order []string
	current := make(map[int]procCounters, len(all))
	sum := 0.0
	for _, info := range all {
		name := info.Name
		if name == "" {
			name = "PID " + itoa(info.PID)
		}
		key := strings.ToLower(name)
		counters := procCounters{key, info.CPUTime, info.ReadBytes, info.WriteBytes}
		current[info.PID] = counters
		group := groups[key]
		if group == nil {
			group = &ProgramLoad{Name: name, OneC: oneCServerImages[key]}
			groups[key] = group
			order = append(order, key)
		}
		group.Count++
		group.MemoryMb += float64(info.WorkingSet) / 1048576
		if first {
			continue
		}
		// Процесс, которого не было в прошлом измерении (или PID занят другой программой),
		// запущен внутри интервала: вся его накопленная нагрузка относится к этому интервалу.
		old, known := t.procs[info.PID]
		if !known || old.name != key || old.cpu > counters.cpu || old.read > counters.read || old.write > counters.write {
			old = procCounters{}
		}
		if info.CPUTimeOK {
			cpu := clamp((counters.cpu - old.cpu).Seconds() / elapsed / float64(cpus) * 100)
			group.Cpu += cpu
			sum += cpu
		}
		group.ReadBps += float64(counters.read-old.read) / elapsed
		group.WriteBps += float64(counters.write-old.write) / elapsed
	}

	cpuTotal = -1
	switch {
	case first:
	case timesOK && t.timesOK && total > t.total && idle >= t.idle:
		cpuTotal = clamp(100 - float64(idle-t.idle)/float64(total-t.total)*100)
	case len(all) > 0:
		cpuTotal = clamp(sum)
	}
	t.at, t.procs, t.idle, t.total, t.timesOK = at, current, idle, total, timesOK

	programs = make([]ProgramLoad, 0, len(order))
	for _, key := range order {
		group := *groups[key]
		group.Cpu = clamp(group.Cpu)
		programs = append(programs, group)
	}
	sort.SliceStable(programs, func(i, j int) bool {
		if programs[i].Cpu != programs[j].Cpu {
			return programs[i].Cpu > programs[j].Cpu
		}
		return programs[i].MemoryMb > programs[j].MemoryMb
	})
	return programs, cpuTotal, !first
}

// heaviestPrograms оставляет программы, заметные хотя бы по одному ресурсу, и все процессы сервера 1С.
func heaviestPrograms(programs []ProgramLoad) []ProgramLoad {
	keep := map[string]bool{}
	mark := func(limit int, value func(ProgramLoad) float64) {
		sorted := append([]ProgramLoad(nil), programs...)
		sort.SliceStable(sorted, func(i, j int) bool { return value(sorted[i]) > value(sorted[j]) })
		for i := 0; i < len(sorted) && i < limit; i++ {
			if value(sorted[i]) > 0 {
				keep[sorted[i].Name] = true
			}
		}
	}
	mark(6, func(p ProgramLoad) float64 { return p.Cpu })
	mark(6, func(p ProgramLoad) float64 { return p.MemoryMb })
	mark(4, func(p ProgramLoad) float64 { return p.ReadBps + p.WriteBps })
	result := make([]ProgramLoad, 0, len(keep)+4)
	for _, program := range programs {
		if keep[program.Name] || program.OneC {
			result = append(result, program)
		}
	}
	return result
}

// busiestActions отбирает сеансы, которые что-то выполняли: сначала ждущие блокировок и идущие
// вызовы, затем по оценочной доле CPU и накопленной за 5 минут нагрузке.
func busiestActions(sessions []SessionRow, limit int) []ActionLoad {
	var busy []SessionRow
	for _, s := range sessions {
		if !s.IsSleeping && (s.IsBlocked() || s.DurationCurrentMs > 0 || s.CallsLast5Min > 0 || s.CpuTimeLast5Min > 0) {
			busy = append(busy, s)
		}
	}
	sort.SliceStable(busy, func(i, j int) bool {
		a, b := busy[i], busy[j]
		if a.IsBlocked() != b.IsBlocked() {
			return a.IsBlocked()
		}
		if (a.DurationCurrentMs > 0) != (b.DurationCurrentMs > 0) {
			return a.DurationCurrentMs > 0
		}
		if a.EstimatedCpu != b.EstimatedCpu {
			return a.EstimatedCpu > b.EstimatedCpu
		}
		if a.DurationCurrentMs != b.DurationCurrentMs {
			return a.DurationCurrentMs > b.DurationCurrentMs
		}
		return a.CpuTimeLast5Min > b.CpuTimeLast5Min
	})
	if len(busy) > limit {
		busy = busy[:limit]
	}
	actions := make([]ActionLoad, 0, len(busy))
	for _, s := range busy {
		actions = append(actions, ActionLoad{
			UserName: s.UserName, InfoBase: s.InfoBase, AppID: s.AppID, Host: s.Host, Pid: s.Pid, State: s.SessionState(), Blocked: s.Blocked,
			CurrentCallMs: s.DurationCurrentMs, CurrentDbMs: s.DurationDbCurrentMs, EstimatedCpu: s.EstimatedCpu,
			Calls5Min: s.CallsLast5Min, CpuTime5Min: s.CpuTimeLast5Min, MemoryMb: s.MemoryCurrentMb,
			Operation: s.Operation, TechContext: s.TechContext, DbRequest: s.DbRequest,
		})
	}
	return actions
}

// sampleLoad собирает нагрузку программ Windows. Вызывается только из цикла обновления.
func (m *Monitor) sampleLoad() ([]ProgramLoad, float64, bool) {
	all, err := m.sys.AllProcesses()
	if err != nil {
		m.LogOnce("all-processes:"+err.Error(), "Не удалось получить список процессов Windows для графиков: "+err.Error(), 10*time.Minute)
	}
	idle, total, ok := m.sys.CPUTimes()
	return m.load.sample(all, idle, total, ok, m.now(), m.sys.LogicalCPUs())
}

// recordLoadPoint добавляет измерение в историю графиков.
func (m *Monitor) recordLoadPoint(now time.Time, programs []ProgramLoad, cpuTotal float64, procs []ProcessRow, sessions []SessionRow, active int, disk DiskSnapshot, deviations []string) {
	point := LoadPoint{
		At: now, CpuTotal: cpuTotal, DiskOK: disk.Available, DiskUsage: disk.UsagePercent, DiskRead: disk.ReadBytesPerSec, DiskWrite: disk.WriteBytesPerSec,
		DiskQueue: disk.QueueLength, Sessions: len(sessions), ActiveSessions: active, AllPrograms: len(programs) > 0,
		Processes: procs, Actions: busiestActions(sessions, 12), Deviations: deviations, RacAge: -1,
	}
	if total, available, ok := m.sys.PhysicalMemory(); ok && total > 0 {
		point.MemoryTotalMb, point.MemoryUsedMb = float64(total)/1048576, float64(total-available)/1048576
	}
	if point.AllPrograms {
		for _, program := range programs {
			if program.OneC {
				point.Cpu1C += program.Cpu
				point.Memory1CMb += program.MemoryMb
			}
		}
		point.Programs = heaviestPrograms(programs)
	} else {
		// Список процессов Windows недоступен: известны только rphost из основного измерения.
		group := ProgramLoad{Name: "rphost.exe", OneC: true, Count: len(procs)}
		for _, process := range procs {
			group.Cpu += process.Cpu
			group.MemoryMb += process.MemoryMb
		}
		point.Cpu1C, point.Memory1CMb = group.Cpu, group.MemoryMb
		if group.Count > 0 {
			point.Programs = []ProgramLoad{group}
		}
	}
	if point.Cpu1C > 100 {
		point.Cpu1C = 100
	}
	if point.CpuTotal >= 0 && point.CpuTotal < point.Cpu1C {
		point.CpuTotal = point.Cpu1C // измерения сделаны не одновременно; часть не может быть больше целого
	}

	m.mu.Lock()
	if m.cachedRac != nil {
		point.RacAge = now.Sub(m.cachedRacAt)
		if point.RacAge < 0 {
			point.RacAge = 0
		}
	}
	if len(m.loadHistory) >= maxLoadPoints+maxLoadPoints/8 {
		// Новый массив: срезы, уже отданные интерфейсу, остаются неизменными.
		m.loadHistory = append(make([]LoadPoint, 0, maxLoadPoints+maxLoadPoints/8+1), m.loadHistory[len(m.loadHistory)-maxLoadPoints:]...)
	}
	m.loadHistory = append(m.loadHistory, point)
	m.mu.Unlock()
}

// LoadHistory возвращает историю измерений для графиков, от старых к новым (примерно 6 часов).
// Срез только для чтения: монитор его больше не изменяет.
func (m *Monitor) LoadHistory() []LoadPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadHistory[:len(m.loadHistory):len(m.loadHistory)]
}
