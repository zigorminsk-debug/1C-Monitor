package monitor

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ProcInfo — процесс операционной системы.
type ProcInfo struct {
	PID        int
	CPUTime    time.Duration // суммарное процессорное время (user + kernel)
	CPUTimeOK  bool          // false, если время недоступно (нет прав)
	WorkingSet uint64        // рабочий набор, байт
	// Поля ниже заполняет только AllProcesses.
	Name       string // имя образа, например sqlservr.exe
	ReadBytes  uint64 // накопительный ввод-вывод процесса: диск, сеть и каналы
	WriteBytes uint64
}

// NicCounters — накопительные счётчики сетевого адаптера.
type NicCounters struct {
	ID       string
	Received uint64
	Sent     uint64
	Speed    float64 // бит/с, 0 — неизвестно
}

// Volume — локальный том.
type Volume struct {
	Name  string
	Total uint64
	Free  uint64
}

// System — доступ к операционной системе. Реализация для Windows находится в system_windows.go,
// на остальных платформах используется заглушка (программа запускается, но не видит процессы 1С).
type System interface {
	IsWindows() bool
	MachineName() string
	OSDescription() string
	OSArchitecture() string
	CPUDescription() string
	LogicalCPUs() int
	PhysicalMemory() (total, available uint64, ok bool)
	BIOSInfo() (manufacturer, model string, ok bool)
	Volumes() []Volume
	FixedDriveRoots() []string
	ProgramFilesDirs() []string
	Processes(name string) ([]ProcInfo, error)
	// AllProcesses — все процессы системы с именами и счётчиками ввода-вывода (для графиков нагрузки).
	AllProcesses() ([]ProcInfo, error)
	// CPUTimes — накопительное время простоя и общее время всех процессоров.
	CPUTimes() (idle, total time.Duration, ok bool)
	ProcessImagePaths(name string) []string
	DiskSample() DiskSnapshot
	NetworkCounters() ([]NicCounters, error)
	FileVersion(path string) string
	ServiceImagePaths() []string
	InstalledOneCLocations() []string
	DocumentsDir() string
	DesktopDir() string
	ExecutableDir() string
	Open(target string) error
	Close()
}

// GenericSystem — переносимая часть реализации System. Сведения, доступные только через
// Windows API, возвращаются пустыми.
type GenericSystem struct{}

func (GenericSystem) IsWindows() bool { return false }

func (GenericSystem) MachineName() string {
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return "localhost"
}

func (GenericSystem) OSDescription() string  { return runtime.GOOS }
func (GenericSystem) OSArchitecture() string { return runtime.GOARCH }
func (GenericSystem) CPUDescription() string { return "процессор не определён" }
func (GenericSystem) LogicalCPUs() int       { return runtime.NumCPU() }

func (GenericSystem) PhysicalMemory() (uint64, uint64, bool) { return 0, 0, false }
func (GenericSystem) BIOSInfo() (string, string, bool)       { return "", "", false }
func (GenericSystem) Volumes() []Volume                      { return nil }
func (GenericSystem) FixedDriveRoots() []string              { return nil }
func (GenericSystem) ProgramFilesDirs() []string             { return nil }
func (GenericSystem) Processes(string) ([]ProcInfo, error)   { return nil, nil }
func (GenericSystem) AllProcesses() ([]ProcInfo, error)      { return nil, nil }
func (GenericSystem) CPUTimes() (time.Duration, time.Duration, bool) {
	return 0, 0, false
}
func (GenericSystem) ProcessImagePaths(string) []string       { return nil }
func (GenericSystem) DiskSample() DiskSnapshot                { return DiskSnapshot{} }
func (GenericSystem) NetworkCounters() ([]NicCounters, error) { return nil, nil }
func (GenericSystem) FileVersion(string) string               { return "?" }
func (GenericSystem) ServiceImagePaths() []string             { return nil }
func (GenericSystem) InstalledOneCLocations() []string        { return nil }
func (GenericSystem) Open(string) error                       { return nil }
func (GenericSystem) Close()                                  {}

func (GenericSystem) DocumentsDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Documents")
	}
	return "."
}

func (GenericSystem) DesktopDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Desktop")
	}
	return "."
}

func (GenericSystem) ExecutableDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// wow64Warning заполняется на Windows, если 32-битная сборка запущена в 64-битной системе.
var wow64Warning string

// DefaultDataDir — каталог настроек, истории и диагностики: %LOCALAPPDATA%\1CServerMonitor.
func DefaultDataDir() string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "1CServerMonitor")
	}
	return filepath.Join(os.TempDir(), "1CServerMonitor")
}
