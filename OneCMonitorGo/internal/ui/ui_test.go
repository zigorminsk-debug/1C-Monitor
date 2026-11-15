package ui

import (
	"errors"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

// fakeProc — процесс имитации с накопительными счётчиками, как их отдаёт Windows.
type fakeProc struct {
	pid         int
	name        string
	cpu         time.Duration
	read, write float64
	memory      uint64
}

// fakeSystem имитирует сервер: каждые 5 секунд модельного времени процессы получают нагрузку
// по сценарию — антивирусная проверка (шаги 40–46) и тяжёлое проведение документа (шаги 70–78).
type fakeSystem struct {
	monitor.GenericSystem
	dir         string
	now         time.Time
	step        int
	procs       []*fakeProc
	busy, total time.Duration
	disk        monitor.DiskSnapshot
	network     uint64
}

const (
	fakeCPUs = 15
	fakeTick = 5 * time.Second
	mb       = 1 << 20
	gb       = 1 << 30
)

func newFakeSystem(dir string) *fakeSystem {
	f := &fakeSystem{dir: dir, now: time.Date(2026, 11, 16, 10, 0, 0, 0, time.Local)}
	add := func(pid int, name string) { f.procs = append(f.procs, &fakeProc{pid: pid, name: name}) }
	add(4, "System")
	add(1300, "ras.exe")
	add(1400, "ragent.exe")
	add(1500, "rmngr.exe")
	add(1800, "sqlservr.exe")
	add(2100, "MsMpEng.exe")
	add(4812, "rphost.exe")
	add(9120, "rphost.exe")
	add(2264, "rphost.exe")
	for i := 0; i < 12; i++ {
		add(3000+4*i, "1cv8c.exe")
	}
	for i := 0; i < 8; i++ {
		add(600+4*i, "svchost.exe")
	}
	f.advance()
	return f
}

// load — нагрузка процесса на шаге: CPU в процентах всего сервера, чтение и запись в байтах в секунду, память.
func (f *fakeSystem) load(p *fakeProc) (cpu, read, write float64, memory uint64) {
	scan, posting := f.step >= 40 && f.step <= 46, f.step >= 70 && f.step <= 78
	switch p.name {
	case "System":
		cpu, memory = 0.5, 200*mb
	case "ras.exe":
		memory = 40 * mb
	case "ragent.exe":
		cpu, memory = 0.1, 120*mb
	case "rmngr.exe":
		cpu, memory = 0.5, 900*mb
	case "sqlservr.exe":
		cpu, read, write, memory = 5, 4*mb, 2*mb, 18*gb
		if scan {
			cpu, read = 8, 6*mb
		}
		if posting {
			cpu, read, write = 22, 25*mb, 12*mb
		}
	case "MsMpEng.exe":
		cpu, read, memory = 1, 0.5*mb, 420*mb
		if scan {
			cpu, read = 14, 70*mb
		}
	case "rphost.exe":
		switch p.pid {
		case 4812:
			cpu, read, write, memory = 6, 0.2*mb, 0.1*mb, 5*gb
			if posting {
				cpu, memory = 58, 7*gb
			} else if f.step > 78 {
				memory = 6200 * mb
			}
		case 9120:
			cpu, memory = 2, 1300*mb
		default:
			cpu, memory = 1, 640*mb
		}
	case "1cv8c.exe":
		cpu, memory = 0.4, 350*mb
	default:
		cpu, memory = 0.1, 60*mb
	}
	wobble := 1 + 0.25*math.Sin(float64(f.step)*0.9+float64(p.pid))
	return cpu * wobble, read * wobble, write * wobble, memory
}

// advance продвигает модельное время на один такт.
func (f *fakeSystem) advance() {
	f.step++
	f.now = f.now.Add(fakeTick)
	capacity := fakeTick * fakeCPUs
	busy, read, write := 0.0, 0.0, 0.0
	for _, p := range f.procs {
		cpu, r, w, memory := f.load(p)
		p.cpu += time.Duration(cpu / 100 * float64(capacity))
		p.read += r * fakeTick.Seconds()
		p.write += w * fakeTick.Seconds()
		p.memory = memory
		busy, read, write = busy+cpu, read+r, write+w
	}
	f.total += capacity
	f.busy += time.Duration(busy / 100 * float64(capacity))
	usage := math.Min(100, 8+(read+write)/mb*1.2)
	f.disk = monitor.DiskSnapshot{Available: true, UsagePercent: usage, ReadBytesPerSec: read, WriteBytesPerSec: write, QueueLength: usage / 40}
	f.network += 40 * mb
}

func (f *fakeSystem) clock() time.Time { return f.now }

func (f *fakeSystem) IsWindows() bool           { return true }
func (f *fakeSystem) MachineName() string       { return "SRV-1C" }
func (f *fakeSystem) OSDescription() string     { return "Windows Server 2016 Standard (10.0.14393)" }
func (f *fakeSystem) OSArchitecture() string    { return "X64" }
func (f *fakeSystem) CPUDescription() string    { return "Intel(R) Xeon(R) Gold 6248R CPU @ 3.00GHz" }
func (f *fakeSystem) LogicalCPUs() int          { return fakeCPUs }
func (f *fakeSystem) DocumentsDir() string      { return filepath.Join(f.dir, "docs") }
func (f *fakeSystem) DesktopDir() string        { return f.dir }
func (f *fakeSystem) ExecutableDir() string     { return f.dir }
func (f *fakeSystem) FileVersion(string) string { return "8.3.24.1624" }
func (f *fakeSystem) BIOSInfo() (string, string, bool) {
	return "VMware, Inc.", "VMware Virtual Platform", true
}
func (f *fakeSystem) PhysicalMemory() (uint64, uint64, bool) {
	used := uint64(6 * gb) // системный кэш и прочее
	for _, p := range f.procs {
		used += p.memory
	}
	return 60 * gb, 60*gb - used, true
}
func (f *fakeSystem) Volumes() []monitor.Volume {
	return []monitor.Volume{{Name: `C:\`, Total: 120 << 30, Free: 41 << 30}, {Name: `D:\`, Total: 800 << 30, Free: 310 << 30}}
}
func (f *fakeSystem) DiskSample() monitor.DiskSnapshot { return f.disk }
func (f *fakeSystem) NetworkCounters() ([]monitor.NicCounters, error) {
	return []monitor.NicCounters{{ID: "eth0", Received: f.network, Sent: f.network / 3, Speed: 1e10}}, nil
}
func (f *fakeSystem) CPUTimes() (time.Duration, time.Duration, bool) {
	return f.total - f.busy, f.total, true
}
func (f *fakeSystem) AllProcesses() ([]monitor.ProcInfo, error) {
	result := make([]monitor.ProcInfo, 0, len(f.procs))
	for _, p := range f.procs {
		result = append(result, monitor.ProcInfo{PID: p.pid, Name: p.name, CPUTime: p.cpu, CPUTimeOK: true, WorkingSet: p.memory,
			ReadBytes: uint64(p.read), WriteBytes: uint64(p.write)})
	}
	return result, nil
}
func (f *fakeSystem) Processes(name string) ([]monitor.ProcInfo, error) {
	all, _ := f.AllProcesses()
	var result []monitor.ProcInfo
	for _, info := range all {
		if info.Name == name+".exe" {
			result = append(result, info)
		}
	}
	return result, nil
}

const (
	infoBases = "infobase : aaaaaaaa-0000-0000-0000-000000000001\nname : trade\n\ninfobase : bbbbbbbb-0000-0000-0000-000000000002\nname : buh\n"
	processes = "process : 11111111-1111-1111-1111-111111111111\npid : 4812\navg-call-time : 6.250\navg-db-call-time : 2.100\nmemory-size : 7340032\n\n" +
		"process : 22222222-2222-2222-2222-222222222222\npid : 9120\navg-call-time : 0.040\navg-db-call-time : 0.010\nmemory-size : 1331200\n\n" +
		"process : 33333333-3333-3333-3333-333333333333\npid : 2264\navg-call-time : 0.015\navg-db-call-time : 0.004\nmemory-size : 655360\n"
	connections = "connection : c0000000-0000-0000-0000-000000000001\nconn-id : 101\nhost : PC-BUH-01\nprocess : 11111111-1111-1111-1111-111111111111\ninfobase : aaaaaaaa-0000-0000-0000-000000000001\napplication : 1CV8C\nsession-number : 5\n\n" +
		"connection : c0000000-0000-0000-0000-000000000002\nconn-id : 102\nhost : PC-SKLAD-07\nprocess : 22222222-2222-2222-2222-222222222222\ninfobase : bbbbbbbb-0000-0000-0000-000000000002\napplication : 1CV8C\nsession-number : 6\n\n" +
		"connection : c0000000-0000-0000-0000-000000000003\nconn-id : 103\nhost : PC-DIR\nprocess : 11111111-1111-1111-1111-111111111111\ninfobase : aaaaaaaa-0000-0000-0000-000000000001\napplication : 1CV8C\nsession-number : 7\n"
)

func session(uuid, id, infoBase, user, host, app, extra string) string {
	return "session : " + uuid + "\nsession-id : " + id + "\ninfobase : " + infoBase + "\nuser-name : " + user + "\nhost : " + host + "\napp-id : " + app +
		"\nstarted-at : 2026-11-15T08:14:02\nlast-active-at : 2026-11-15T12:03:41\nhibernate : no\nblocked-by-dbms : 0\nblocked-by-ls : 0\nmemory-current : 73400320\ncalls-last-5min : 184\nduration-last-5min : 96000\n" + extra + "\n"
}

var sessions = session("50000000-0000-0000-0000-00000000000a", "5", "aaaaaaaa-0000-0000-0000-000000000001", "Иванова Мария Петровна", "PC-BUH-01", "1CV8C", "duration-current : 48200\nduration-current-dbms : 31000\ncurrent-service-name : Документ.РеализацияТоваровУслуг.ПровестиНаСервере\n") +
	session("50000000-0000-0000-0000-00000000000b", "6", "bbbbbbbb-0000-0000-0000-000000000002", "Петров Сергей", "PC-SKLAD-07", "1CV8C", "") +
	session("50000000-0000-0000-0000-00000000000c", "7", "aaaaaaaa-0000-0000-0000-000000000001", "Директор", "PC-DIR", "1CV8C", "blocked-by-ls : 5\n") +
	session("50000000-0000-0000-0000-00000000000d", "8", "aaaaaaaa-0000-0000-0000-000000000001", "Сидорова Анна", "PC-BUH-02", "1CV8C", "hibernate : yes\n") +
	session("50000000-0000-0000-0000-00000000000e", "9", "bbbbbbbb-0000-0000-0000-000000000002", "", "SRV-1C", "BackgroundJob", "")

func fakeRun(exe string, args []string, _ time.Duration) (string, error) {
	if filepath.Base(exe) != "rac.exe" {
		return "Samsung SSD 870 [SSD, SATA, 931,5 ГБ]\nVMware Virtual NVMe Disk [NVMe, NVMe, 120,0 ГБ]\n", nil
	}
	line := " " + strings.Join(args, " ") + " "
	switch {
	case strings.Contains(line, " cluster list "):
		return "cluster : dddddddd-0000-0000-0000-00000000000d\nname : \"Local cluster\"\n", nil
	case strings.Contains(line, "session list"):
		return sessions, nil
	case strings.Contains(line, "infobase summary list"):
		return infoBases, nil
	case strings.Contains(line, "process list"):
		return processes, nil
	case strings.Contains(line, "connection list"):
		return connections, nil
	}
	return "", errors.New("неизвестная команда")
}

// TestWindow строит окно на программном рендерере Fyne, наполняет его данными имитации кластера
// и проверяет содержимое. Если задана переменная SCREENSHOT_DIR, туда сохраняются снимки вкладок.
func TestWindow(t *testing.T) {
	dir := t.TempDir()
	rac := filepath.Join(dir, "rac.exe")
	if err := os.WriteFile(rac, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	system := newFakeSystem(dir)
	m := monitor.New(monitor.Options{System: system, Run: fakeRun, DataDir: filepath.Join(dir, "data"), Now: system.clock})
	defer m.Close()
	m.SetRunning(false)
	m.UpdateSettings(func(s *monitor.Settings) { s.RacPath, s.ClusterUser = rac, "" })

	app := test.NewApp()
	defer app.Quit()
	app.Settings().SetTheme(NewTheme())
	win := test.NewWindow(nil)
	defer win.Close()
	u := New(app, win, m)
	win.Resize(fyne.NewSize(1450, 900))

	// Десять минут модельного времени: такты запускаются вручную.
	const steps = 120
	m.Refresh(true)
	for i := 1; i < steps; i++ {
		system.advance()
		m.Refresh(false)
	}
	for i := 0; i < 40; i++ {
		m.Log("Строка журнала № " + itoa(i+1))
	}
	m.Log("Пример длинной строки журнала: RAC C:\\Program Files\\1cv8\\8.3.24.1624\\bin\\rac.exe, localhost:1545: Ошибка соединения с сервером")
	u.Refresh()

	view := m.View()
	if view.ProcessCount != 3 || len(view.Sessions) != 5 {
		t.Fatalf("данные имитации: процессов %d, сеансов %d; %s", view.ProcessCount, len(view.Sessions), m.LogText())
	}
	if win.Title() != "SRV-1C — 1C Server Monitor" {
		t.Errorf("заголовок окна: %q", win.Title())
	}
	layout := func() {
		// Программный рендерер сам не пересчитывает раскладку при изменении текста; подписи
		// с переносом строк уточняют высоту после первого прохода, поэтому проходов несколько.
		for pass := 0; pass < 3; pass++ {
			win.Content().Refresh()
		}
	}

	// Вкладка графиков: щелчок рядом с пиком выбирает пик и объясняет его.
	history := m.LoadHistory()
	if len(history) != steps-1 { // первое измерение только задаёт точку отсчёта
		t.Fatalf("точек истории %d, ожидалось %d", len(history), steps-1)
	}
	u.tabs.SelectIndex(1)
	layout()
	cpuChart, diskChart := u.loadCharts[0], u.loadCharts[2]
	// Щелчок мимо точки на несколько единиц экрана: выбирается ближайшее измерение.
	tapNear := func(chart *loadChart, point monitor.LoadPoint, offset float32) {
		t.Helper()
		x, ok := chart.timeX(point.At)
		if !ok {
			t.Fatalf("момент %s вне графика", point.TimeText())
		}
		chart.Tapped(&fyne.PointEvent{Position: fyne.NewPos(x+offset, 60)})
	}
	peak := func(value func(monitor.LoadPoint) float64) monitor.LoadPoint {
		best := history[0]
		for _, point := range history {
			if value(point) > value(best) {
				best = point
			}
		}
		return best
	}
	expect := func(what string, point monitor.LoadPoint, fragments ...string) {
		t.Helper()
		if !strings.Contains(u.loadMoment.Text, point.TimeText()) {
			t.Errorf("%s: выбран «%s», ожидался момент %s", what, u.loadMoment.Text, point.TimeText())
		}
		for _, fragment := range fragments {
			if !strings.Contains(u.loadExplanation.Text, fragment) {
				t.Errorf("%s: в выводе нет «%s»:\n%s", what, fragment, u.loadExplanation.Text)
			}
		}
	}
	if !strings.Contains(u.loadMoment.Text, history[len(history)-1].TimeText()) {
		t.Errorf("без выбора должен показываться последний момент: %q", u.loadMoment.Text)
	}
	diskPeak := peak(func(p monitor.LoadPoint) float64 { return p.DiskUsage })
	tapNear(diskChart, diskPeak, 2)
	expect("пик диска", diskPeak, "MsMpEng.exe — чтение", "Диск: активность")
	cpuPeak := peak(func(p monitor.LoadPoint) float64 { return p.CpuTotal })
	if cpuPeak.CpuTotal < 80 || cpuPeak.Cpu1C < 50 {
		t.Errorf("пик процессора в имитации: %s", cpuPeak.CpuText())
	}
	tapNear(cpuChart, cpuPeak, -2)
	expect("пик процессора", cpuPeak, "rphost.exe ×3", "sqlservr.exe", "rphost PID 4812", "Иванова Мария Петровна", "Документ.РеализацияТоваровУслуг.ПровестиНаСервере")
	// Новые измерения не сбрасывают выбранный момент.
	system.advance()
	m.Refresh(false)
	u.Refresh()
	expect("после нового измерения", cpuPeak)

	shots := os.Getenv("SCREENSHOT_DIR")
	for i, item := range u.tabs.Items {
		u.tabs.SelectIndex(i)
		if i == 2 {
			// выбор строки показывает подробности сеанса
			if table := findTable(item.Content); table != nil {
				table.Select(widget.TableCellID{Row: 0, Col: 1})
			}
		}
		u.Refresh()
		layout()
		if shots == "" {
			continue
		}
		file, err := os.Create(filepath.Join(shots, "tab"+itoa(i+1)+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, win.Canvas().Capture()); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	// Окно должно помещаться на экране 1024×768 (сеанс RDP): минимальный размер задаёт содержимое.
	if min := win.Content().MinSize(); min.Width > 1000 || min.Height > 700 {
		t.Errorf("минимальный размер окна слишком велик: %v", min)
	}
	if u.selectedSession == "" {
		t.Error("выбор строки в таблице сеансов не сработал")
	}

	// Монитор сбросил контрольную фразу и изменил настройки — интерфейс это отражает.
	m.SetAdminConfirmation("что-то")
	m.KillAllRphost()
	u.Refresh()
	if !strings.Contains(u.view.AdminActionStatus, monitor.ConfirmKillPhrase) {
		t.Errorf("статус администрирования: %q", u.view.AdminActionStatus)
	}
}

func findTable(object fyne.CanvasObject) *widget.Table {
	switch value := object.(type) {
	case *widget.Table:
		return value
	case *fyne.Container:
		for _, child := range value.Objects {
			if table := findTable(child); table != nil {
				return table
			}
		}
	}
	return nil
}

func loadPoints(base time.Time, values ...float64) []monitor.LoadPoint {
	points := make([]monitor.LoadPoint, len(values))
	for i, value := range values {
		points[i] = monitor.LoadPoint{At: base.Add(time.Duration(i) * 5 * time.Second), CpuTotal: value}
	}
	return points
}

// TestChartPeaks проверяет выбор пика щелчком и то, что сжатие длинной истории не срезает пики.
func TestChartPeaks(t *testing.T) {
	base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	points := loadPoints(base, 10, 12, 11, 95, 13, 12, 11, 10, 60, 9)
	at := func(i int) time.Time { return points[i].At }

	if got := peakNear(points, at(2), 6*time.Second, cpuValue); got != 3 {
		t.Errorf("щелчок рядом с пиком: выбрана точка %d, ожидалась 3", got)
	}
	if got := peakNear(points, at(6).Add(time.Second), 2*time.Second, cpuValue); got != 6 {
		t.Errorf("узкий допуск: выбрана точка %d, ожидалась ближайшая 6", got)
	}
	if got := peakNear(points, at(9).Add(time.Hour), time.Second, cpuValue); got != 9 {
		t.Errorf("щелчок правее последнего измерения: выбрана точка %d", got)
	}
	if got := nearestPoint(nil, base); got != -1 {
		t.Errorf("пустая история: %d", got)
	}

	// 2000 измерений на 100 пикселях: одиночный пик должен остаться на линии.
	values := make([]float64, 2000)
	for i := range values {
		values[i] = 20
	}
	values[1234] = 97
	long := loadPoints(base, values...)
	paths := chartPaths(long, long[0].At, long[len(long)-1].At, 0, cpuValue, 100, 101)
	if len(paths) != 1 || len(paths[0]) > 50 {
		t.Fatalf("сжатие: ломаных %d", len(paths))
	}
	top := float32(1000)
	for _, vertex := range paths[0] {
		if vertex.y < top {
			top = vertex.y
		}
	}
	if top > 3.5 { // 97 % на высоте 101 пиксель — третья строка сверху
		t.Errorf("пик потерян при сжатии: верхняя точка линии на строке %v", top)
	}

	// Пауза мониторинга и отсутствие данных разрывают линию.
	broken := loadPoints(base, 10, 20, -1, 30, 40)
	broken = append(broken, monitor.LoadPoint{At: base.Add(10 * time.Minute), CpuTotal: 50}, monitor.LoadPoint{At: base.Add(10*time.Minute + 5*time.Second), CpuTotal: 55})
	paths = chartPaths(broken, broken[0].At, broken[len(broken)-1].At, 20*time.Second, cpuValue, 800, 100)
	if len(paths) != 3 || len(paths[0]) != 2 || len(paths[1]) != 2 || len(paths[2]) != 2 {
		t.Errorf("разрывы линии: %v", paths)
	}
}
