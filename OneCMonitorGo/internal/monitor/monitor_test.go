package monitor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSystem имитирует сервер с двумя процессами rphost.
type fakeSystem struct {
	GenericSystem
	dir string

	mu         sync.Mutex
	cpuSeconds float64
	opened     []string
	bigMemory  uint64
}

func (f *fakeSystem) IsWindows() bool           { return true }
func (f *fakeSystem) MachineName() string       { return "SRV-1C" }
func (f *fakeSystem) LogicalCPUs() int          { return 4 }
func (f *fakeSystem) DocumentsDir() string      { return filepath.Join(f.dir, "docs") }
func (f *fakeSystem) DesktopDir() string        { return filepath.Join(f.dir, "desktop") }
func (f *fakeSystem) ExecutableDir() string     { return filepath.Join(f.dir, "app") }
func (f *fakeSystem) FileVersion(string) string { return "8.3.22.1709" }
func (f *fakeSystem) BIOSInfo() (string, string, bool) {
	return "VMware, Inc.", "VMware Virtual Platform", true
}
func (f *fakeSystem) PhysicalMemory() (uint64, uint64, bool) { return 64 << 30, 20 << 30, true }
func (f *fakeSystem) Volumes() []Volume {
	return []Volume{{Name: `C:\`, Total: 100 << 30, Free: 40 << 30}}
}
func (f *fakeSystem) DiskSample() DiskSnapshot {
	return DiskSnapshot{Available: true, UsagePercent: 12, ReadBytesPerSec: 1 << 20, WriteBytesPerSec: 2 << 20, QueueLength: 0.5}
}
func (f *fakeSystem) Open(target string) error {
	f.mu.Lock()
	f.opened = append(f.opened, target)
	f.mu.Unlock()
	return nil
}
func (f *fakeSystem) Processes(name string) ([]ProcInfo, error) {
	if name != "rphost" {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cpuSeconds += 0.5
	return []ProcInfo{
		{PID: 1111, WorkingSet: f.bigMemory, CPUTimeOK: true, CPUTime: time.Duration(f.cpuSeconds * float64(time.Second))},
		{PID: 2222, WorkingSet: 512 << 20, CPUTimeOK: true, CPUTime: time.Second},
	}, nil
}

// fakeRac имитирует rac.exe и остальные внешние программы.
type fakeRac struct {
	mu       sync.Mutex
	calls    []string
	sessions string
	fail     bool
	hang     chan struct{} // не nil — rac.exe «зависает», пока канал не закрыт
	delay    time.Duration
	denied   map[string]bool
}

func has(args []string, words ...string) bool {
	for _, word := range words {
		found := false
		for _, arg := range args {
			found = found || arg == word
		}
		if !found {
			return false
		}
	}
	return true
}

func option(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

func (r *fakeRac) run(exe string, args []string, timeout time.Duration) (string, error) {
	name := filepath.Base(exe)
	r.mu.Lock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	hang, fail, delay := r.hang, r.fail, r.delay
	r.mu.Unlock()
	if name != "rac.exe" {
		return "", nil
	}
	if hang != nil {
		<-hang
		return "", errors.New("Превышено время ожидания rac.exe")
	}
	time.Sleep(delay)
	if fail {
		return "", errors.New("Ошибка соединения с сервером")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case has(args, "cluster", "list"):
		if option(args, "127.0.0.1:") != "" { // имя localhost «не разрешается», как на сервере с IPv6
			return "cluster : dddddddd-0000-0000-0000-00000000000d\nhost : SRV\nport : 1541\nname : \"Local cluster\"\n", nil
		}
		return "", errors.New("Ошибка соединения с сервером")
	case has(args, "session", "terminate"):
		uuid := option(args, "--session=")
		var kept []string
		for _, block := range strings.Split(r.sessions, "\n\n") {
			if strings.TrimSpace(block) != "" && !strings.Contains(block, uuid) {
				kept = append(kept, block)
			}
		}
		r.sessions = strings.Join(kept, "\n\n") + "\n"
		return "", nil
	case has(args, "session", "list"):
		return r.sessions, nil
	case has(args, "infobase", "summary", "list"):
		return testInfoBases, nil
	case has(args, "infobase", "info"):
		state := "off"
		if r.denied[option(args, "--infobase=")] {
			state = "on"
		}
		return "infobase : " + option(args, "--infobase=") + "\nscheduled-jobs-deny : " + state + "\n", nil
	case has(args, "infobase", "update"):
		r.denied[option(args, "--infobase=")] = option(args, "--scheduled-jobs-deny=") == "on"
		return "", nil
	case has(args, "process", "list"):
		return testProcesses, nil
	case has(args, "process", "turn-off"):
		return "", nil
	case has(args, "connection", "list"):
		return testConnections, nil
	}
	return "", errors.New("неизвестная команда")
}

func (r *fakeRac) count(substring string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, call := range r.calls {
		if strings.Contains(call, substring) {
			n++
		}
	}
	return n
}

func (r *fakeRac) matching(substring string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []string
	for _, call := range r.calls {
		if strings.Contains(call, substring) {
			result = append(result, call)
		}
	}
	return result
}

func (r *fakeRac) set(change func()) {
	r.mu.Lock()
	change()
	r.mu.Unlock()
}

type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

type fixture struct {
	m     *Monitor
	rac   *fakeRac
	sys   *fakeSystem
	clock *clock
	dir   string
	path  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	racPath := filepath.Join(dir, "1cv8", "8.3.22.1709", "bin", "rac.exe")
	for _, d := range []string{filepath.Dir(racPath), filepath.Join(dir, "desktop"), filepath.Join(dir, "app"), filepath.Join(dir, "techlog", "rphost_1111")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// ras.exe рядом нет: путь из настроек должен приниматься и без него.
	if err := os.WriteFile(racPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	hourFile := filepath.Join(dir, "techlog", "rphost_1111", time.Now().Format("06010215")+".log")
	techLog := "\uFEFF01:00.000001-900000,CALL,1,process=rphost,p:processName=trade,SessionID=5,Usr=Иванов,Context='Форма.Вызов : Документ.Заказ.Форма.Модуль.Записать\nДокумент.Заказ.МодульОбъекта : 120 : Запрос.Выполнить();'\n"
	if err := os.WriteFile(hourFile, []byte(techLog), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		rac:   &fakeRac{sessions: testSessions, denied: map[string]bool{}},
		sys:   &fakeSystem{dir: dir, bigMemory: 5 << 30},
		clock: &clock{}, dir: dir, path: racPath,
	}
	f.m = New(Options{System: f.sys, Run: f.rac.run, DataDir: filepath.Join(dir, "data"), Now: f.clock.now})
	f.m.manualRacWait, f.m.backgroundRacWait, f.m.settleDelay, f.m.rasProbeInterval = 5*time.Second, 300*time.Millisecond, time.Millisecond, 10*time.Millisecond
	f.m.SetRunning(false) // такты запускаются вручную, чтобы сценарий был детерминированным
	f.m.UpdateSettings(func(s *Settings) {
		s.RacPath, s.TechLogPath, s.Endpoint, s.RacTimeoutSeconds = `"`+racPath+`"`, filepath.Join(dir, "techlog"), "localhost:1545", 15
	})
	t.Cleanup(f.m.Close)
	return f
}

func findSession(view View, number, infoBase string) SessionRow {
	for _, s := range view.Sessions {
		if s.SessionNumber == number && s.InfoBase == infoBase {
			return s
		}
	}
	return SessionRow{}
}

func findProcess(view View, pid int) ProcessRow {
	for _, p := range view.Processes {
		if p.Pid == pid {
			return p
		}
	}
	return ProcessRow{}
}

func waitUntil(t *testing.T, limit time.Duration, name string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMonitorScenario(t *testing.T) {
	f := newFixture(t)
	m, rac := f.m, f.rac
	changes := 0
	var changesMu sync.Mutex
	m.SetOnChange(func() { changesMu.Lock(); changes++; changesMu.Unlock() })

	// 0. RAS недоступен: неудачный опрос не повторяется на каждом такте.
	rac.set(func() { rac.fail = true })
	m.Refresh(false)
	failedCalls := rac.count("rac.exe")
	view := m.View()
	check(t, failedCalls > 0 && strings.Contains(view.RacStatus, "Недоступны"), "неудачный опрос показан в статусе: "+view.RacStatus)
	eq(t, view.ProcessCount, 2, "локальные rphost видны без RAC")
	m.Refresh(false)
	m.Refresh(false)
	eq(t, rac.count("rac.exe"), failedCalls, "RAC не перезапускается на каждом такте без подключения")
	rac.set(func() { rac.fail = false })

	// 1. Ручное обновление: подключение, сеансы, привязка PID, техжурнал.
	m.Refresh(true)
	view = m.View()
	check(t, strings.HasPrefix(view.RacStatus, "Подключено: 127.0.0.1:1545"), "подключение через запасной адрес 127.0.0.1: "+view.RacStatus)
	eq(t, m.Settings().RacPath, f.path, "путь в кавычках без ras.exe рядом принят и нормализован")
	eq(t, m.Settings().Endpoint, "127.0.0.1:1545", "рабочий адрес RAS записан в настройки")
	check(t, view.SettingsVersion > 0, "интерфейс уведомлён об изменении настроек")
	eq(t, len(view.Sessions), 8, "сеансы показаны")
	eq(t, view.ProcessCount, 2, "число rphost")
	eq(t, view.ActiveSessionCount, 7, "активные сеансы")
	eq(t, view.SleepingSessionCount, 1, "спящие сеансы")
	ivanov := findSession(view, "5", "trade")
	eq(t, ivanov.Pid, 1111, "сеанс связан с PID rphost")
	const context = "Форма.Вызов : Документ.Заказ.Форма.Модуль.Записать → Документ.Заказ.МодульОбъекта : 120 : Запрос.Выполнить();"
	eq(t, ivanov.TechContext, context, "контекст техжурнала")
	eq(t, ivanov.LastCallMs, 900.0, "длительность из техжурнала")
	eq(t, findSession(view, "5", "buh").TechContext, "", "тот же номер сеанса в другой базе не получает чужой контекст")
	big := findProcess(view, 1111)
	check(t, big.Risk >= 40 && strings.Contains(big.Diagnosis, "RAM"), "риск по памяти: "+big.Diagnosis)
	eq(t, big.SessionCount, 1, "сеансов в процессе")
	eq(t, big.AvgCallMs, 250.0, "показатели RAC объединены с процессом")
	check(t, strings.Contains(m.LogText(), "Ручное обновление: rphost=2, сеансы=8"), "ручное обновление записано в журнал")
	check(t, strings.Contains(view.ServerHeaderDetails, "1С/RAC 8.3.22.1709"), "версия RAC в заголовке: "+view.ServerHeaderDetails)
	check(t, strings.Contains(view.ServerHardwareText, "VMware"), "виртуализация определена: "+view.ServerHardwareText)
	changesMu.Lock()
	check(t, changes > 0, "интерфейс получает уведомления")
	changesMu.Unlock()

	// 2. Обычный такт сразу после: RAC не опрашивается, загрузка CPU считается по разности.
	callsBefore := rac.count("rac.exe")
	time.Sleep(60 * time.Millisecond)
	m.Refresh(false)
	eq(t, rac.count("rac.exe"), callsBefore, "RAC не опрашивается раньше интервала")
	check(t, findProcess(m.View(), 1111).Cpu > 0, "CPU вычислен по разности процессорного времени")

	// 3. Новый снимок RAC без сканирования техжурнала: контекст не теряется.
	m.lastRacPoll = time.Time{}
	m.Refresh(false)
	check(t, rac.count("rac.exe") > callsBefore, "RAC опрошен по интервалу")
	eq(t, findSession(m.View(), "5", "trade").TechContext, context, "контекст сохраняется при опросе без сканирования техжурнала")

	// 4. Зависший rac.exe не останавливает локальные такты.
	hang := make(chan struct{})
	rac.set(func() { rac.hang = hang })
	m.lastRacPoll = time.Time{}
	callsBefore = rac.count("rac.exe")
	started := time.Now()
	m.Refresh(false)
	first := time.Since(started)
	check(t, first >= 250*time.Millisecond && first < 2*time.Second, "такт ждёт зависший RAC ограниченное время: "+first.String())
	eq(t, len(m.View().Sessions), 8, "последние сеансы остаются на экране")
	eq(t, rac.count("rac.exe"), callsBefore+1, "запущен один rac.exe")
	started = time.Now()
	m.lastRacPoll = time.Time{}
	m.Refresh(false)
	check(t, time.Since(started) < 250*time.Millisecond, "следующий такт не ждёт идущий опрос")
	eq(t, rac.count("rac.exe"), callsBefore+1, "второй опрос не запускается, пока идёт первый")
	check(t, m.View().Status != "Ошибка мониторинга", "мониторинг продолжает работать")

	// 5. Зависший опрос завершился — следующий такт восстанавливает подключение.
	rac.set(func() { rac.hang = nil })
	close(hang)
	waitUntil(t, 5*time.Second, "завершение зависшего опроса", func() bool { return isClosed(m.racPoll) })
	m.lastRacPoll = time.Time{}
	m.Refresh(false)
	check(t, strings.HasPrefix(m.View().RacStatus, "Подключено"), "статус после восстановления: "+m.View().RacStatus)

	// 6. Ручное обновление во время идущего цикла не теряется.
	manualBefore := strings.Count(m.LogText(), "Ручное обновление")
	rac.set(func() { rac.delay = 20 * time.Millisecond })
	m.lastRacPoll = time.Time{}
	done := make(chan struct{})
	go func() { defer close(done); m.Refresh(false) }()
	waitUntil(t, 2*time.Second, "начало цикла", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.busy })
	started = time.Now()
	m.Refresh(true)
	check(t, time.Since(started) < 15*time.Millisecond, "ручной запрос во время цикла возвращается сразу")
	<-done
	rac.set(func() { rac.delay = 0 })
	eq(t, strings.Count(m.LogText(), "Ручное обновление"), manualBefore+1, "отложенное ручное обновление выполнено сразу после цикла")

	// 7. Административные операции.
	m.TerminateDuplicateSessions()
	check(t, strings.Contains(m.View().AdminActionStatus, ConfirmPhrase), "нужна контрольная фраза: "+m.View().AdminActionStatus)
	eq(t, rac.count("terminate"), 0, "без подтверждения ничего не завершается")
	m.SetAdminConfirmation(" подтверждаю ")
	rac.set(func() { rac.delay = 30 * time.Millisecond })
	operation := make(chan struct{})
	go func() { defer close(operation); m.TerminateDuplicateSessions() }()
	waitUntil(t, 2*time.Second, "начало операции", func() bool { return m.View().AdminBusy })
	m.TerminateAllSessions()
	check(t, strings.Contains(m.View().AdminActionStatus, "Дождитесь"), "вторая операция отклонена, пока идёт первая: "+m.View().AdminActionStatus)
	<-operation
	rac.set(func() { rac.delay = 0 })
	terminated := rac.matching("terminate")
	if len(terminated) != 1 || !strings.Contains(terminated[0], "--session=50000000-0000-0000-0000-00000000000a") {
		t.Errorf("должен быть завершён ровно один ранний дубликат: %v", terminated)
	}
	check(t, !strings.Contains(terminated[0], "--cluster-user"), "без администратора кластера параметры авторизации не передаются")
	waitUntil(t, 2*time.Second, "обновление списка сеансов", func() bool { return len(m.View().Sessions) == 7 })
	check(t, m.View().ConfirmationResets > 0 && !m.View().AdminBusy, "контрольная фраза сброшена, операция завершена")
	m.TerminateSleepingSessions()
	check(t, strings.Contains(m.View().AdminActionStatus, ConfirmPhrase), "сброшенная фраза требуется заново")

	// Принудительное завершение rphost требует отдельной фразы.
	m.SetAdminConfirmation(ConfirmPhrase)
	m.KillAllRphost()
	eq(t, rac.count("taskkill.exe"), 0, "обычной фразы для taskkill недостаточно")
	m.SetAdminConfirmation(ConfirmKillPhrase)
	m.KillAllRphost()
	eq(t, rac.count("taskkill.exe /F /IM rphost.exe"), 1, "taskkill выполнен после своей фразы")

	// 8. Фоновые задания: запрет, завершение работающих, обратное включение только «своих» баз.
	rac.set(func() { rac.denied[buhUUID] = true }) // в buh задания были запрещены до монитора
	m.UpdateSettings(func(s *Settings) { s.ClusterUser, s.ClusterPassword = "admin", "" })
	m.SetAdminConfirmation(ConfirmPhrase)
	m.DisableBackgroundJobs()
	updates := rac.matching("infobase update")
	if len(updates) != 1 || !strings.Contains(updates[0], tradeUUID) || !strings.Contains(updates[0], "--scheduled-jobs-deny=on") {
		t.Errorf("запрет должен быть установлен только в базе trade: %v", updates)
	}
	check(t, strings.Contains(updates[0], "--cluster-user=admin") && strings.Contains(updates[0], "--cluster-pwd="), "администратор с пустым паролем передаётся в RAC")
	eq(t, strings.Join(m.Settings().BackgroundJobsDisabledInfobases, ","), tradeUUID, "запомнена только база, выключенная монитором")
	eq(t, rac.count("session terminate")+rac.count("terminate --"), 3, "работающие фоновые сеансы завершены")
	waitUntil(t, 2*time.Second, "обновление после фоновых заданий", func() bool { return len(m.View().Sessions) == 5 })
	m.SetAdminConfirmation(ConfirmPhrase)
	m.EnableBackgroundJobs()
	eq(t, rac.count("--scheduled-jobs-deny=off"), 1, "задания включены только в базе, выключенной монитором")
	eq(t, len(m.Settings().BackgroundJobsDisabledInfobases), 0, "список выключенных баз очищен")
	rac.set(func() {
		check(t, rac.denied[buhUUID], "база, запрещённая до монитора, не тронута")
	})
	m.UpdateSettings(func(s *Settings) { s.ClusterUser = "" })

	// 9. Отчёты и настройки.
	file := m.ExportHTMLReport()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("HTML-отчёт: %v", err)
	}
	report := string(data)
	check(t, strings.HasPrefix(report, "\uFEFF<!doctype html>") && strings.HasSuffix(report, "</html>"), "отчёт — завершённый HTML с BOM")
	check(t, strings.Contains(report, "Петров") && strings.Contains(report, "Рабочие процессы") && strings.Contains(report, "csl.by"), "в отчёте есть сеансы, процессы и контакты")
	check(t, strings.Contains(m.LogText(), "HTML-отчёт сохранён"), "создание отчёта записано в журнал")
	f.clock.advance(2 * time.Minute) // следующая точка суточной статистики
	m.Refresh(true)
	m.GenerateDailyReport()
	check(t, strings.HasPrefix(m.View().AdminActionStatus, "Отчёт создан: "), "суточный отчёт: "+m.View().AdminActionStatus)
	daily, err := os.ReadFile(strings.TrimPrefix(m.View().AdminActionStatus, "Отчёт создан: "))
	check(t, err == nil && strings.Contains(string(daily), "Кто генерировал нагрузку") && strings.Contains(string(daily), "Иванов"), "суточный отчёт содержит пользователей")
	f.sys.mu.Lock()
	eq(t, len(f.sys.opened), 2, "оба отчёта открыты программой по умолчанию")
	f.sys.mu.Unlock()
	history, err := os.ReadFile(filepath.Join(f.dir, "data", "history", "history-"+time.Now().Format("2006-01-02")+".csv"))
	check(t, err == nil && strings.HasPrefix(string(history), "\uFEFFВремя;PID;") && strings.Contains(string(history), ";1111;"), "история записана в CSV")

	m.SaveSettingsAndCheck()
	saved := LoadSettings(filepath.Join(f.dir, "data"))
	eq(t, saved.RacPath, f.path, "настройки записаны на диск")
	check(t, !strings.Contains(m.LogText(), "Ошибка цикла") && m.View().Status != "Ошибка мониторинга", "ошибок мониторинга нет: "+m.View().Status)
	if t.Failed() {
		t.Log("--- журнал монитора ---\n" + m.LogText())
	}
}

func TestAutoDrain(t *testing.T) {
	f := newFixture(t)
	m, rac := f.m, f.rac
	m.UpdateSettings(func(s *Settings) {
		s.AutoDrainEnabled, s.AutoDrainMemoryMb, s.AutoDrainMinutes, s.AutoDrainMinProcesses, s.AutoDrainCooldownMinutes = true, 4096, 10, 2, 30
	})
	m.Refresh(true)
	check(t, strings.Contains(m.View().AutoDrainStatus, "запрещён"), "при двух процессах и минимуме 2 автовывод запрещён: "+m.View().AutoDrainStatus)
	m.UpdateSettings(func(s *Settings) { s.AutoDrainMinProcesses = 1 })
	m.Refresh(true)
	check(t, strings.Contains(m.View().AutoDrainStatus, "Автовывод активен"), "порог превышен недостаточно долго: "+m.View().AutoDrainStatus)
	eq(t, rac.count("turn-off"), 0, "раньше срока процесс не выводится")

	f.clock.advance(11 * time.Minute)
	m.Refresh(true)
	turnOff := rac.matching("turn-off")
	if len(turnOff) != 1 || !strings.Contains(turnOff[0], "--process=11111111-1111-1111-1111-111111111111") {
		t.Fatalf("должен быть выведен процесс 1111: %v", turnOff)
	}
	eq(t, rac.count("taskkill"), 0, "принудительное завершение не применяется")
	check(t, strings.HasPrefix(findProcess(m.View(), 1111).DrainStatus, "Выводится с "), "состояние процесса: "+findProcess(m.View(), 1111).DrainStatus)
	m.Refresh(true)
	eq(t, rac.count("turn-off"), 1, "выводимый процесс не выводится повторно")
	check(t, strings.Contains(m.View().AutoDrainStatus, "Плавно выводится PID 1111"), "статус автовывода: "+m.View().AutoDrainStatus)

	// Ручной вывод: нужна фраза и выбранная строка; ниже минимума процессов операция отменяется.
	m.SetAdminConfirmation(ConfirmPhrase)
	m.GracefulTurnOffSelected()
	check(t, strings.Contains(m.View().AdminActionStatus, "выберите процесс"), "без выбора строки вывод не выполняется: "+m.View().AdminActionStatus)
	m.SelectProcess(2222)
	m.UpdateSettings(func(s *Settings) { s.AutoDrainMinProcesses = 2 })
	m.GracefulTurnOffSelected()
	check(t, strings.Contains(m.View().AdminActionStatus, "слишком мало"), "минимум процессов соблюдается: "+m.View().AdminActionStatus)
	eq(t, rac.count("turn-off"), 1, "лишних команд turn-off нет")
}

func TestStartRas(t *testing.T) {
	f := newFixture(t)
	m, rac := f.m, f.rac
	original := startDetached
	t.Cleanup(func() { startDetached = original })
	var started []string
	startDetached = func(exe string, args []string) (<-chan int, error) {
		started = append(started, filepath.Base(exe)+" "+strings.Join(args, " "))
		rac.set(func() { rac.fail = false }) // RAS «поднялся»
		return make(chan int), nil
	}

	m.StartRas()
	check(t, strings.Contains(m.View().RacStatus, "ras.exe не найден"), "без ras.exe рядом запуск невозможен: "+m.View().RacStatus)
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "ras.exe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m.StartRas()
	eq(t, len(started), 0, "работающий RAS второй раз не запускается")
	check(t, strings.Contains(m.LogText(), "RAS уже работает: 127.0.0.1:1545"), "обнаружен работающий RAS")

	rac.set(func() { rac.fail = true })
	m.UpdateSettings(func(s *Settings) { s.Endpoint, s.AgentEndpoint = "127.0.0.1:1777", "" })
	m.StartRas()
	if len(started) != 1 || started[0] != "ras.exe cluster --port=1777 localhost:1540" {
		t.Fatalf("команда запуска RAS: %v", started)
	}
	check(t, strings.Contains(m.LogText(), "RAS готов: 127.0.0.1:1777 · агент localhost:1540"), "готовность RAS обнаружена: "+m.View().RacStatus)

	// ras.exe сразу завершился — пользователь видит код возврата, а не ждёт тайм-аут.
	rac.set(func() { rac.fail = true })
	startDetached = func(string, []string) (<-chan int, error) {
		exited := make(chan int, 1)
		exited <- 3
		return exited, nil
	}
	m.StartRas()
	check(t, strings.Contains(m.View().RacStatus, "завершился с кодом 3"), "код возврата ras.exe показан: "+m.View().RacStatus)
}

func TestAlertsAndLog(t *testing.T) {
	f := newFixture(t)
	m := f.m
	// RAM выше порога (+40) и долгие вызовы по данным RAC (+35) дают риск 75.
	m.UpdateSettings(func(s *Settings) { s.MemoryAlertMb, s.CallAlertMs = 1024, 100 })
	m.Refresh(true)
	view := m.View()
	check(t, view.AlertCount > 0 && view.StatusLevel == LevelBad, "отклонение показано в статусе: "+view.Status)
	check(t, len(view.Alerts) > 0, "тревога добавлена в список")
	before := len(view.Alerts)
	m.Refresh(true)
	eq(t, len(m.View().Alerts), before, "тревога не повторяется раньше интервала")

	for i := 0; i < 3*maxLogLines; i++ {
		m.Log("строка")
	}
	lines, version := m.LogLines()
	check(t, len(lines) <= maxLogLines && version > 0, "журнал ограничен по размеру")
	m.LogOnce("k", "один раз", time.Hour)
	m.LogOnce("K", "один раз", time.Hour)
	eq(t, strings.Count(m.LogText(), "один раз"), 1, "LogOnce подавляет повторы")
}
