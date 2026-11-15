//go:build windows

package monitor

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ownProcess(t *testing.T, all []ProcInfo) ProcInfo {
	t.Helper()
	for _, info := range all {
		if info.PID == os.Getpid() {
			return info
		}
	}
	t.Fatalf("собственный процесс PID %d не найден среди %d процессов", os.Getpid(), len(all))
	return ProcInfo{}
}

// TestWindowsSystem вызывает настоящие функции Windows: список процессов и их счётчики, время
// процессоров, память, диски, сеть, реестр. Выполняется только в Windows (в GitHub Actions —
// для 64- и 32-битной сборки), на остальных платформах файл не компилируется.
func TestWindowsSystem(t *testing.T) {
	s := NewSystem()
	defer s.Close()
	if !s.IsWindows() {
		t.Fatal("IsWindows() = false")
	}
	manufacturer, model, biosOK := s.BIOSInfo()
	t.Logf("сервер %q, %s, %s, CPU %q × %d, BIOS %q %q (%v), сборка %s", s.MachineName(), s.OSDescription(), s.OSArchitecture(),
		s.CPUDescription(), s.LogicalCPUs(), manufacturer, model, biosOK, runtime.GOARCH)
	if !strings.Contains(s.OSDescription(), "Windows") || s.OSArchitecture() == "" || s.LogicalCPUs() < 1 || s.MachineName() == "" {
		t.Error("сведения о системе не определены")
	}
	if strings.Contains(s.CPUDescription(), "не определён") {
		t.Error("процессор не определён: чтение 64-битного представления реестра не работает")
	}

	total, available, ok := s.PhysicalMemory()
	t.Logf("память: всего %d МБ, свободно %d МБ", total>>20, available>>20)
	if !ok || total < 1<<30 || available == 0 || available > total {
		t.Errorf("память: всего %d, свободно %d, ok=%v", total, available, ok)
	}

	// Процессы: собственный процесс виден по имени, а его счётчики растут вместе с работой.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Base(executable)
	idleBefore, totalBefore, timesOK := s.CPUTimes()
	started := time.Now()
	before, err := s.AllProcesses()
	if err != nil {
		t.Fatal(err)
	}
	own := ownProcess(t, before)
	if !strings.EqualFold(own.Name, image) || !own.CPUTimeOK || own.WorkingSet < 1<<20 || own.WorkingSet > 4<<30 {
		t.Errorf("собственный процесс: %+v, ожидалось имя %s", own, image)
	}
	system := false
	for _, info := range before {
		if info.PID == 0 {
			t.Error("в список попал процесс бездействия (PID 0)")
		}
		system = system || (info.PID == 4 && info.Name == "System")
	}
	if !system || len(before) < 10 {
		t.Errorf("процессов %d, процесс System найден: %v", len(before), system)
	}

	const payload = 8 << 20
	file := filepath.Join(t.TempDir(), "io.bin")
	if err := os.WriteFile(file, make([]byte, payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(file); err != nil {
		t.Fatal(err)
	}
	for busy := time.Now(); time.Since(busy) < 500*time.Millisecond; {
	}
	after, err := s.AllProcesses()
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	ownAfter := ownProcess(t, after)
	cpu, written, read := ownAfter.CPUTime-own.CPUTime, ownAfter.WriteBytes-own.WriteBytes, ownAfter.ReadBytes-own.ReadBytes
	t.Logf("собственный процесс за %v: CPU %v, записано %d КБ, прочитано %d КБ, рабочий набор %d МБ", elapsed.Round(time.Millisecond), cpu, written>>10, read>>10, ownAfter.WorkingSet>>20)
	if cpu < 200*time.Millisecond || cpu > elapsed*time.Duration(runtime.NumCPU()) {
		t.Errorf("процессорное время собственного процесса %v за %v", cpu, elapsed)
	}
	if written < payload || written > 8*payload || read < payload || read > 8*payload {
		t.Errorf("счётчики ввода-вывода: записано %d, прочитано %d при %d байтах в каждую сторону", written, read, payload)
	}
	byName, err := s.Processes(strings.TrimSuffix(image, filepath.Ext(image)))
	if err != nil || len(byName) == 0 || ownProcess(t, byName).PID != os.Getpid() {
		t.Errorf("поиск процесса по имени %s: %v, %v", image, byName, err)
	}
	if paths := s.ProcessImagePaths(strings.TrimSuffix(image, filepath.Ext(image))); len(paths) == 0 || !strings.EqualFold(paths[0], executable) {
		t.Errorf("путь к образу процесса: %v, ожидался %s", paths, executable)
	}

	idleAfter, totalAfter, _ := s.CPUTimes()
	capacity := time.Since(started) * time.Duration(s.LogicalCPUs())
	t.Logf("время процессоров за интервал: всего %v, простой %v, ожидалось около %v", totalAfter-totalBefore, idleAfter-idleBefore, capacity)
	if !timesOK || idleAfter < idleBefore || idleAfter-idleBefore > totalAfter-totalBefore || totalAfter-totalBefore < capacity/2 || totalAfter-totalBefore > capacity*2 {
		t.Errorf("GetSystemTimes: ok=%v, всего %v, простой %v при ожидаемых %v", timesOK, totalAfter-totalBefore, idleAfter-idleBefore, capacity)
	}

	// Диски и тома.
	roots, volumes := s.FixedDriveRoots(), s.Volumes()
	t.Logf("диски %v, тома %+v", roots, volumes)
	if len(roots) == 0 || len(volumes) == 0 {
		t.Error("локальные диски не найдены")
	}
	for _, volume := range volumes {
		if volume.Total == 0 || volume.Free > volume.Total {
			t.Errorf("том %+v", volume)
		}
	}
	s.DiskSample()
	time.Sleep(1100 * time.Millisecond)
	disk := s.DiskSample()
	t.Logf("счётчики диска: %+v", disk)
	if !disk.Available {
		t.Error("счётчики PhysicalDisk(_Total) недоступны")
	}
	if disk.UsagePercent < 0 || disk.UsagePercent > 100 || disk.ReadBytesPerSec < 0 || disk.WriteBytesPerSec < 0 || disk.QueueLength < 0 || disk.QueueLength > 1e6 {
		t.Errorf("счётчики диска вне допустимых значений: %+v", disk)
	}

	checkNetwork(t, s)

	// Реестр, версии файлов, каталоги.
	cmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	version := s.FileVersion(cmd)
	t.Logf("версия %s: %s; несуществующий файл: %s", cmd, version, s.FileVersion(file+".missing"))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`).MatchString(version) || s.FileVersion(file+".missing") != "?" {
		t.Errorf("версия файла: %q", version)
	}
	services, installed := s.ServiceImagePaths(), s.InstalledOneCLocations()
	t.Logf("служб с путём к образу: %d, установок 1С в реестре: %d", len(services), len(installed))
	if len(services) < 20 {
		t.Errorf("служб найдено %d: чтение реестра служб не работает", len(services))
	}
	for name, dir := range map[string]string{"документы": s.DocumentsDir(), "рабочий стол": s.DesktopDir(), "программа": s.ExecutableDir()} {
		t.Logf("каталог «%s»: %s", name, dir)
		if !filepath.IsAbs(dir) {
			t.Errorf("каталог «%s» не определён: %q", name, dir)
		}
	}
	if dirs := s.ProgramFilesDirs(); len(dirs) == 0 {
		t.Error("каталоги Program Files не определены")
	}
	if dir := DefaultDataDir(); !strings.Contains(strings.ToLower(dir), `appdata\local\1cservermonitor`) {
		t.Errorf("каталог данных: %s", dir)
	}
}

// checkNetwork сверяет счётчики адаптеров с командлетом Get-NetAdapterStatistics: так проверяются
// смещения полей MIB_IF_ROW2, которые для 32-битной сборки заданы вручную.
func checkNetwork(t *testing.T, s System) {
	output, psErr := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-NetAdapter | ForEach-Object { $x = $_ | Get-NetAdapterStatistics; "$($_.InterfaceGuid) $($x.ReceivedBytes) $($x.SentBytes)" }`).Output()
	counters, err := s.NetworkCounters()
	if err != nil {
		t.Fatalf("сетевые счётчики: %v", err)
	}
	t.Logf("адаптеры: %+v", counters)
	if len(counters) == 0 {
		t.Error("работающие сетевые адаптеры не найдены")
	}
	later, _ := s.NetworkCounters()
	for i, counter := range counters {
		if counter.ID == "" || counter.Speed < 0 || counter.Speed > 1e12 {
			t.Errorf("адаптер %+v", counter)
		}
		if i < len(later) && later[i].ID == counter.ID && (later[i].Received < counter.Received || later[i].Sent < counter.Sent) {
			t.Errorf("счётчики адаптера уменьшились: %+v → %+v", counter, later[i])
		}
	}
	if psErr != nil {
		t.Logf("Get-NetAdapterStatistics недоступен, сверка пропущена: %v", psErr)
		return
	}
	compared := 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		received, errReceived := strconv.ParseUint(fields[1], 10, 64)
		sent, errSent := strconv.ParseUint(fields[2], 10, 64)
		if errReceived != nil || errSent != nil {
			continue
		}
		for _, counter := range counters {
			if !strings.EqualFold(counter.ID, fields[0]) {
				continue
			}
			compared++
			t.Logf("адаптер %s: Windows сообщает приём %d и передачу %d, монитор — %d и %d", fields[0], received, sent, counter.Received, counter.Sent)
			// PowerShell опрошен раньше, поэтому счётчики монитора могут быть немного больше.
			close := func(reference, value uint64) bool {
				return value >= reference && value-reference <= reference/10+64<<20
			}
			if !close(received, counter.Received) || !close(sent, counter.Sent) {
				t.Errorf("счётчики адаптера %s расходятся с Windows", fields[0])
			}
		}
	}
	if compared == 0 {
		t.Logf("общих адаптеров для сверки нет; вывод PowerShell:\n%s", output)
	}
}

// TestWindowsMonitor прогоняет два такта монитора на настоящей системе без сервера 1С.
func TestWindowsMonitor(t *testing.T) {
	m := New(Options{DataDir: t.TempDir()})
	defer m.Close()
	m.SetRunning(false)
	m.Refresh(true)
	time.Sleep(1500 * time.Millisecond)
	m.Refresh(true)

	view := m.View()
	t.Logf("заголовок: %s\n%s\n%s\nсостояние: %s\nRAC: %s\nдиск: %s · %s · %s\n%s", view.ServerHeaderTitle, view.ServerHeaderDetails, view.ServerHardwareText,
		view.Status, view.RacStatus, view.DiskUsageText, view.DiskIoText, view.DiskQueueText, view.NetworkLoadText)
	if !strings.Contains(view.ServerHeaderDetails, "Windows") || view.Status == "Ошибка мониторинга" {
		t.Errorf("состояние монитора: %q / %q", view.ServerHeaderDetails, view.Status)
	}
	history := m.LoadHistory()
	if len(history) == 0 {
		t.Fatalf("измерений для графиков нет; журнал:\n%s", m.LogText())
	}
	point := history[len(history)-1]
	t.Logf("измерение %s:\n%s", point.TimeText(), strings.Join(point.Explain(), "\n"))
	if !point.AllPrograms || len(point.Programs) < 3 || point.CpuTotal < 0 || point.CpuTotal > 100 || point.MemoryTotalMb < 1024 ||
		point.MemoryUsedMb <= 0 || point.MemoryUsedMb > point.MemoryTotalMb || !point.DiskOK {
		t.Errorf("измерение для графиков: %+v", point)
	}
	memory := 0.0
	for _, program := range point.Programs {
		memory += program.MemoryMb
		if program.Name == "" || program.Count < 1 || program.Cpu < 0 || program.Cpu > 100 || program.ReadBps < 0 || program.WriteBps < 0 {
			t.Errorf("программа %+v", program)
		}
	}
	if memory < 50 || memory > point.MemoryTotalMb {
		t.Errorf("память самых нагруженных программ %.0f МБ при %.0f МБ на сервере", memory, point.MemoryTotalMb)
	}
	t.Logf("журнал монитора:\n%s", m.LogText())
}
