package monitor

import (
	"strings"
	"testing"
	"time"
)

func findProgram(programs []ProgramLoad, name string) ProgramLoad {
	for _, program := range programs {
		if program.Name == name {
			return program
		}
	}
	return ProgramLoad{}
}

func near(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }

// TestLoadTracker проверяет расчёт нагрузки программ по накопительным счётчикам Windows.
func TestLoadTracker(t *testing.T) {
	const cpus = 4
	base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	proc := func(pid int, name string, cpu time.Duration, memoryMb, read, write uint64) ProcInfo {
		return ProcInfo{PID: pid, Name: name, CPUTime: cpu, CPUTimeOK: true, WorkingSet: memoryMb << 20, ReadBytes: read, WriteBytes: write}
	}
	var tracker loadTracker

	programs, total, measured := tracker.sample([]ProcInfo{
		proc(100, "rphost.exe", 50*time.Second, 2000, 1000, 1000),
		proc(104, "rphost.exe", 10*time.Second, 1000, 0, 0),
		proc(200, "sqlservr.exe", 500*time.Second, 8000, 5000, 5000),
		proc(300, "old.exe", 90*time.Second, 10, 0, 0),
	}, 1000*time.Second, 2000*time.Second, true, base, cpus)
	if measured || total >= 0 {
		t.Fatalf("первое измерение не должно давать загрузку: measured=%v total=%v", measured, total)
	}
	if rphost := findProgram(programs, "rphost.exe"); rphost.Count != 2 || rphost.MemoryMb != 3000 || rphost.Cpu != 0 || !rphost.OneC {
		t.Errorf("первое измерение, rphost: %+v", rphost)
	}

	// Через 10 секунд: на 4 процессорах доступно 40 секунд процессорного времени.
	programs, total, measured = tracker.sample([]ProcInfo{
		proc(100, "rphost.exe", 60*time.Second, 2500, 1000+10<<20, 1000+20<<20), // 10 с → 25 %
		proc(104, "rphost.exe", 12*time.Second, 1000, 0, 0),                     // 2 с → 5 %
		proc(200, "sqlservr.exe", 504*time.Second, 8000, 5000+50<<20, 5000),     // 4 с → 10 %
		proc(300, "new.exe", 2*time.Second, 20, 30<<20, 0),                      // PID занят новой программой: 2 с → 5 %
		proc(400, "backup.exe", 6*time.Second, 300, 100<<20, 0),                 // запущен внутри интервала: 6 с → 15 %
	}, 1000*time.Second+12*time.Second, 2000*time.Second+40*time.Second, true, base.Add(10*time.Second), cpus)
	if !measured {
		t.Fatal("второе измерение должно дать загрузку")
	}
	if !near(total, 70) { // простой 12 с из 40
		t.Errorf("загрузка сервера %v, ожидалось 70", total)
	}
	if programs[0].Name != "rphost.exe" {
		t.Errorf("программы должны идти по убыванию CPU: %+v", programs)
	}
	rphost := findProgram(programs, "rphost.exe")
	if !near(rphost.Cpu, 30) || rphost.MemoryMb != 3500 || !near(rphost.ReadBps, 1<<20) || !near(rphost.WriteBps, 2<<20) || rphost.Label() != "rphost.exe ×2" {
		t.Errorf("rphost: %+v", rphost)
	}
	if sql := findProgram(programs, "sqlservr.exe"); !near(sql.Cpu, 10) || !near(sql.ReadBps, 5<<20) || sql.WriteBps != 0 || sql.OneC {
		t.Errorf("sqlservr: %+v", sql)
	}
	if reused := findProgram(programs, "new.exe"); !near(reused.Cpu, 5) || !near(reused.ReadBps, 3<<20) {
		t.Errorf("повторно занятый PID: %+v", reused)
	}
	if started := findProgram(programs, "backup.exe"); !near(started.Cpu, 15) || !near(started.ReadBps, 10<<20) {
		t.Errorf("процесс, запущенный внутри интервала: %+v", started)
	}
	if findProgram(programs, "old.exe").Count != 0 {
		t.Error("завершившийся процесс остался в списке")
	}

	// Без GetSystemTimes загрузка сервера — сумма по процессам.
	_, total, _ = tracker.sample([]ProcInfo{
		proc(100, "rphost.exe", 80*time.Second, 2500, 1000+10<<20, 1000+20<<20), // 20 с → 50 %
		proc(200, "sqlservr.exe", 504*time.Second, 8000, 5000+50<<20, 5000),
	}, 0, 0, false, base.Add(20*time.Second), cpus)
	if !near(total, 50) {
		t.Errorf("загрузка по сумме процессов %v, ожидалось 50", total)
	}
}

func TestBusiestActions(t *testing.T) {
	sessions := []SessionRow{
		{UserName: "Спящий", IsSleeping: true, DurationCurrentMs: 99999},
		{UserName: "Ожидает"},
		{UserName: "Недавний", CallsLast5Min: 5, CpuTimeLast5Min: 800, EstimatedCpu: 1},
		{UserName: "Долгий", DurationCurrentMs: 48200, DurationDbCurrentMs: 31000, EstimatedCpu: 20, Operation: "Документ.Провести", Pid: 7},
		{UserName: "Быстрый", DurationCurrentMs: 300, EstimatedCpu: 35, TechContext: "Отчёт.Сформировать", Operation: "Вызов"},
		{UserName: "Ждёт", Blocked: "да (DBMS=0, LS=5)", InfoBase: "trade"},
		{UserName: "", Blocked: NotBlocked, CpuTimeLast5Min: 10},
	}
	actions := busiestActions(sessions, 4)
	var names []string
	for _, action := range actions {
		names = append(names, action.User())
	}
	if got := strings.Join(names, ","); got != "Ждёт,Быстрый,Долгий,Недавний" {
		t.Fatalf("порядок действий: %s", got)
	}
	if actions[1].What() != "Отчёт.Сформировать" || actions[2].What() != "Документ.Провести" || actions[2].CallText() != "48,2 с" || actions[2].DbText() != "31,0 с" {
		t.Errorf("описание действий: %+v", actions[1:3])
	}
	if !actions[0].IsBlocked() || !strings.Contains(actions[0].What(), "не определено") {
		t.Errorf("сеанс с блокировкой: %+v", actions[0])
	}

	point := LoadPoint{
		At: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), CpuTotal: 91.2, Cpu1C: 60, MemoryTotalMb: 61440, MemoryUsedMb: 40000, Memory1CMb: 9000,
		DiskOK: true, DiskUsage: 45, DiskQueue: 1.2, DiskRead: 20 << 20, DiskWrite: 4 << 20, RacAge: 5 * time.Second,
		Programs:   []ProgramLoad{{Name: "rphost.exe", Count: 3, Cpu: 60, MemoryMb: 9000, OneC: true}, {Name: "sqlservr.exe", Count: 1, Cpu: 25, MemoryMb: 18000, ReadBps: 20 << 20}, {Name: "idle.exe", Count: 1, Cpu: 0.2, MemoryMb: 10}},
		Processes:  []ProcessRow{{Pid: 7, Cpu: 58, MemoryMb: 7000, SessionCount: 2, Risk: 80, Diagnosis: "высокий CPU 58%", Users: "Долгий"}, {Pid: 8, Cpu: 1, MemoryMb: 600}},
		Actions:    actions,
		Deviations: []string{"PID 7: высокий CPU 58%"},
	}
	text := strings.Join(point.Explain(), "\n")
	for _, fragment := range []string{
		"Процессор: сервер 91,2 % · 1С 60,0 %. Больше всего: rphost.exe ×3 — 60,0 %; sqlservr.exe — 25,0 %.",
		"Память: занято 39,06 ГБ из 60,00 ГБ (65 %) · 1С 8,79 ГБ. Больше всего: sqlservr.exe — 17,58 ГБ; rphost.exe ×3 — 8,79 ГБ.",
		"Диск: активность 45,0 % · очередь 1,20 · чтение 20,0 МБ/с · запись 4,0 МБ/с. Больше всего: sqlservr.exe — чтение 20,0 МБ/с, запись 0 КБ/с.",
		"rphost PID 7: CPU 58,0 %, RAM 6,84 ГБ, сеансов 2 — высокий CPU 58%. Пользователи: Долгий.",
		"Действие 1С: Ждёт (trade, rphost —) — действие не определено (нет данных RAC и техжурнала), блокировка: да (DBMS=0, LS=5).",
		"Действие 1С: Долгий (, rphost 7) — Документ.Провести, вызов 48,2 с, из них СУБД 31,0 с.",
		"Отклонение: PID 7: высокий CPU 58%.",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("в выводе нет «%s»:\n%s", fragment, text)
		}
	}
	if strings.Contains(text, "idle.exe") || strings.Contains(text, "PID 8") {
		t.Errorf("в вывод попали ненагруженные программы:\n%s", text)
	}

	point.RacAge, point.Actions = -1, nil
	if text := strings.Join(point.Explain(), "\n"); !strings.Contains(text, "кластер не отвечал через RAC") {
		t.Errorf("нет данных RAC:\n%s", text)
	}
	point.RacAge, point.Actions = 10*time.Minute, actions
	if text := strings.Join(point.Explain(), "\n"); !strings.Contains(text, "получены за 10 мин до этого момента") {
		t.Errorf("устаревшие данные RAC:\n%s", text)
	}
}

// TestLoadHistory проверяет запись измерений циклом монитора и ограничение длины истории.
func TestLoadHistory(t *testing.T) {
	f := newFixture(t)
	f.m.Refresh(true)
	if n := len(f.m.LoadHistory()); n != 0 {
		t.Fatalf("первое измерение только задаёт точку отсчёта, а точек %d", n)
	}
	f.clock.advance(5 * time.Second)
	f.m.Refresh(true)
	history := f.m.LoadHistory()
	if len(history) != 1 {
		t.Fatalf("точек истории %d, ожидалась 1", len(history))
	}
	point := history[0]
	// У имитации нет списка всех процессов Windows: известны только rphost.
	if point.AllPrograms || len(point.Programs) != 1 || point.Programs[0].Name != "rphost.exe" || point.Programs[0].Count != len(point.Processes) || len(point.Processes) == 0 {
		t.Errorf("программы без списка процессов: %+v", point.Programs)
	}
	if point.CpuTotal >= 0 || point.Memory1CMb <= 0 || !near(point.MemoryTotalMb, 65536) || !near(point.MemoryUsedMb, 45056) || !point.DiskOK || point.DiskUsage != 12 {
		t.Errorf("показатели точки: %+v", point)
	}
	if point.RacAge < 0 || point.Sessions == 0 || len(point.Actions) == 0 {
		t.Errorf("сведения о сеансах: возраст %v, сеансов %d, действий %d", point.RacAge, point.Sessions, len(point.Actions))
	}
	if text := strings.Join(point.Explain(), "\n"); !strings.Contains(text, "Процессор: 1С ") || !strings.Contains(text, "Действие 1С: ") {
		t.Errorf("вывод:\n%s", text)
	}

	// История ограничена, а уже выданный срез при этом не меняется.
	at := point.At
	for i := 0; i < maxLoadPoints+maxLoadPoints/4; i++ {
		at = at.Add(5 * time.Second)
		f.m.recordLoadPoint(at, nil, -1, nil, nil, 0, DiskSnapshot{}, nil)
	}
	trimmed := f.m.LoadHistory()
	if len(trimmed) < maxLoadPoints || len(trimmed) > maxLoadPoints+maxLoadPoints/8 {
		t.Errorf("длина истории %d при пределе %d", len(trimmed), maxLoadPoints)
	}
	if !trimmed[len(trimmed)-1].At.Equal(at) || !trimmed[0].At.After(point.At) {
		t.Errorf("история должна содержать последние измерения: %v … %v", trimmed[0].At, trimmed[len(trimmed)-1].At)
	}
	if len(history) != 1 || !history[0].At.Equal(point.At) || len(history[0].Processes) == 0 {
		t.Error("ранее выданный срез изменился")
	}
}
