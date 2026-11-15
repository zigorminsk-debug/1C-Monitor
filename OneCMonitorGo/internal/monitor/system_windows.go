//go:build windows

package monitor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Функции, которых нет в golang.org/x/sys/windows, и те, что должны работать на Windows Server 2016
// (GetIfEntry2Ex появилась только в сборке 1703, поэтому используется GetIfEntry2).
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetNativeSystemInfo  = kernel32.NewProc("GetNativeSystemInfo")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")

	iphlpapi        = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIfEntry2 = iphlpapi.NewProc("GetIfEntry2")

	pdh                             = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery                = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter        = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = pdh.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery               = pdh.NewProc("PdhCloseQuery")
)

const (
	createNoWindow        = 0x08000000
	pdhFmtDouble          = 0x00000200
	processorArchAMD64    = 9
	processorArchARM64    = 12
	ifTypeSoftwareLoopbck = 24
	ifTypeTunnel          = 131
	ifOperStatusUp        = 1

	// Размер MIB_IF_ROW2 и смещения счётчиков байтов, одинаковые для x86 и x64.
	mibIfRow2Size      = 1352
	mibIfRow2InOctets  = 1208
	mibIfRow2OutOctets = 1280
)

func init() {
	if runtime.GOARCH == "386" && nativeArchitecture() != "X86" {
		wow64Warning = "Запущена 32-битная сборка монитора в 64-битной Windows: память процессов rphost больше 4 ГБ может отображаться неточно. Используйте сборку x64."
	}
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

type systemInfo struct {
	ProcessorArchitecture     uint16
	Reserved                  uint16
	PageSize                  uint32
	MinimumApplicationAddress uintptr
	MaximumApplicationAddress uintptr
	ActiveProcessorMask       uintptr
	NumberOfProcessors        uint32
	ProcessorType             uint32
	AllocationGranularity     uint32
	ProcessorLevel            uint16
	ProcessorRevision         uint16
}

func nativeArchitecture() string {
	if procGetNativeSystemInfo.Find() != nil {
		return strings.ToUpper(runtime.GOARCH)
	}
	var info systemInfo
	procGetNativeSystemInfo.Call(uintptr(unsafe.Pointer(&info)))
	switch info.ProcessorArchitecture {
	case processorArchAMD64:
		return "X64"
	case processorArchARM64:
		return "Arm64"
	default:
		return "X86"
	}
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// pdhCounterValue соответствует PDH_FMT_COUNTERVALUE: значение лежит по смещению 8 на x86 и x64.
type pdhCounterValue struct {
	CStatus uint32
	_       uint32
	Double  float64
}

type windowsSystem struct {
	GenericSystem

	diskMu                    sync.Mutex
	diskReady, diskClosed     bool
	query                     uintptr
	usage, read, write, queue uintptr
}

// NewSystem возвращает реализацию System для Windows.
func NewSystem() System {
	s := &windowsSystem{}
	s.openDiskCounters()
	return s
}

func (s *windowsSystem) IsWindows() bool { return true }

func (s *windowsSystem) MachineName() string {
	if name, err := windows.ComputerName(); err == nil && name != "" {
		return name
	}
	return s.GenericSystem.MachineName()
}

// openKey открывает раздел HKLM в 64-битном представлении реестра (и для 32-битной сборки).
func openKey(path string, access uint32) (registry.Key, error) {
	return registry.OpenKey(registry.LOCAL_MACHINE, path, access|registry.WOW64_64KEY)
}

func readString(path, name string) string {
	key, err := openKey(path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *windowsSystem) OSDescription() string {
	v := windows.RtlGetVersion()
	version := fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	if product := readString(`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "ProductName"); product != "" {
		// В реестре Windows 11 продукт по-прежнему называется «Windows 10».
		if v.ProductType == 1 && v.BuildNumber >= 22000 {
			product = strings.Replace(product, "Windows 10", "Windows 11", 1)
		}
		return product + " (" + version + ")"
	}
	return "Microsoft Windows " + version
}

func (s *windowsSystem) OSArchitecture() string { return nativeArchitecture() }

func (s *windowsSystem) CPUDescription() string {
	if cpu := readString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString"); cpu != "" {
		return cpu
	}
	return s.GenericSystem.CPUDescription()
}

func (s *windowsSystem) PhysicalMemory() (total, available uint64, ok bool) {
	if procGlobalMemoryStatusEx.Find() != nil {
		return 0, 0, false
	}
	status := memoryStatusEx{}
	status.Length = uint32(unsafe.Sizeof(status))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); r == 0 {
		return 0, 0, false
	}
	return status.TotalPhys, status.AvailPhys, true
}

func (s *windowsSystem) BIOSInfo() (manufacturer, model string, ok bool) {
	key, err := openKey(`HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer key.Close()
	manufacturer, _, _ = key.GetStringValue("SystemManufacturer")
	model, _, _ = key.GetStringValue("SystemProductName")
	return strings.TrimSpace(manufacturer), strings.TrimSpace(model), true
}

func (s *windowsSystem) FixedDriveRoots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var roots []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		pointer, err := windows.UTF16PtrFromString(root)
		if err != nil || windows.GetDriveType(pointer) != windows.DRIVE_FIXED {
			continue
		}
		roots = append(roots, root)
	}
	return roots
}

func (s *windowsSystem) Volumes() []Volume {
	var volumes []Volume
	for _, root := range s.FixedDriveRoots() {
		pointer, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		var freeForCaller, total, free uint64
		if windows.GetDiskFreeSpaceEx(pointer, &freeForCaller, &total, &free) != nil || total == 0 {
			continue // том не готов
		}
		volumes = append(volumes, Volume{Name: root, Total: total, Free: free})
	}
	return volumes
}

func (s *windowsSystem) ProgramFilesDirs() []string {
	var dirs []string
	for _, name := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		if dir := os.Getenv(name); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// AllProcesses возвращает все процессы системы. Используется NtQuerySystemInformation:
// процессорное время, память и счётчики ввода-вывода доступны без открытия процесса,
// то есть и без прав администратора, и для 64-битных процессов из 32-битной сборки.
func (s *windowsSystem) AllProcesses() ([]ProcInfo, error) {
	size := uint32(1 << 20)
	var buffer []byte
	for attempt := 0; ; attempt++ {
		buffer = make([]byte, size)
		var needed uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buffer[0]), size, &needed)
		if err == nil {
			break
		}
		if err != windows.STATUS_INFO_LENGTH_MISMATCH || attempt >= 6 {
			return nil, err
		}
		if needed > size {
			size = needed
		}
		size += 1 << 18 // список процессов может вырасти между вызовами
	}
	var result []ProcInfo
	for offset := uintptr(0); offset+unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{}) <= uintptr(len(buffer)); {
		info := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buffer[offset]))
		// PID 0 — «Бездействие системы»: его время — это простой процессора, а не нагрузка.
		if info.UniqueProcessID != 0 {
			name := ""
			if info.ImageName.Buffer != nil {
				name = info.ImageName.String()
			}
			workingSet := uint64(info.WorkingSetSize)
			// В 32-битной сборке поле рабочего набора 32-битное; частный рабочий набор передаётся
			// 64-битным числом и не превышает полный, поэтому для больших процессов берётся он.
			if private := uint64(info.WorkingSetPrivateSize); unsafe.Sizeof(info.WorkingSetSize) == 4 && info.WorkingSetPrivateSize > 0 && private > workingSet {
				workingSet = private
			}
			result = append(result, ProcInfo{
				PID:        int(info.UniqueProcessID),
				CPUTime:    time.Duration(info.UserTime+info.KernelTime) * 100, // единицы по 100 нс
				CPUTimeOK:  true,
				WorkingSet: workingSet,
				Name:       name,
				ReadBytes:  uint64(info.ReadTransferCount),
				WriteBytes: uint64(info.WriteTransferCount),
			})
		}
		if info.NextEntryOffset == 0 {
			break
		}
		offset += uintptr(info.NextEntryOffset)
	}
	return result, nil
}

// Processes возвращает процессы с указанным именем образа (без .exe).
func (s *windowsSystem) Processes(name string) ([]ProcInfo, error) {
	all, err := s.AllProcesses()
	if err != nil {
		return nil, err
	}
	image := strings.ToLower(name) + ".exe"
	var result []ProcInfo
	for _, info := range all {
		if strings.ToLower(info.Name) == image {
			result = append(result, info)
		}
	}
	return result, nil
}

// CPUTimes возвращает время простоя и общее время процессоров (GetSystemTimes; время ядра включает простой).
func (s *windowsSystem) CPUTimes() (idle, total time.Duration, ok bool) {
	if procGetSystemTimes.Find() != nil {
		return 0, 0, false
	}
	var idleTime, kernelTime, userTime windows.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idleTime)), uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&userTime))); r == 0 {
		return 0, 0, false
	}
	ticks := func(t windows.Filetime) time.Duration {
		return time.Duration(uint64(t.HighDateTime)<<32|uint64(t.LowDateTime)) * 100 // единицы по 100 нс
	}
	return ticks(idleTime), ticks(kernelTime) + ticks(userTime), true
}

func (s *windowsSystem) ProcessImagePaths(name string) []string {
	processes, err := s.Processes(name)
	if err != nil {
		return nil
	}
	var paths []string
	for _, process := range processes {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.PID))
		if err != nil {
			continue // нет прав: путь будет найден через реестр служб
		}
		buffer := make([]uint16, 32768)
		length := uint32(len(buffer))
		if windows.QueryFullProcessImageName(handle, 0, &buffer[0], &length) == nil && length > 0 {
			paths = append(paths, windows.UTF16ToString(buffer[:length]))
		}
		windows.CloseHandle(handle)
	}
	return paths
}

func (s *windowsSystem) openDiskCounters() {
	defer func() { _ = recover() }()
	for _, proc := range []*windows.LazyProc{procPdhOpenQuery, procPdhAddEnglishCounter, procPdhCollectQueryData, procPdhGetFormattedCounterValue, procPdhCloseQuery} {
		if proc.Find() != nil {
			return
		}
	}
	if r, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&s.query))); r != 0 {
		s.query = 0
		return
	}
	add := func(path string, counter *uintptr) bool {
		pointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return false
		}
		r, _, _ := procPdhAddEnglishCounter.Call(s.query, uintptr(unsafe.Pointer(pointer)), 0, uintptr(unsafe.Pointer(counter)))
		return r == 0
	}
	if !add(`\PhysicalDisk(_Total)\% Disk Time`, &s.usage) || !add(`\PhysicalDisk(_Total)\Disk Read Bytes/sec`, &s.read) ||
		!add(`\PhysicalDisk(_Total)\Disk Write Bytes/sec`, &s.write) || !add(`\PhysicalDisk(_Total)\Avg. Disk Queue Length`, &s.queue) {
		return
	}
	// Первое измерение создаёт базовую точку для скоростных счётчиков.
	r, _, _ := procPdhCollectQueryData.Call(s.query)
	s.diskReady = r == 0
}

func pdhValue(counter uintptr) (float64, bool) {
	if counter == 0 {
		return 0, false
	}
	var kind uint32
	var value pdhCounterValue
	r, _, _ := procPdhGetFormattedCounterValue.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(&value)))
	// PDH_CSTATUS_VALID_DATA (0) и PDH_CSTATUS_NEW_DATA (1) — оба означают корректное значение.
	if r != 0 || value.CStatus > 1 || value.Double != value.Double || value.Double > 1e300 || value.Double < -1e300 {
		return 0, false
	}
	return value.Double, true
}

func (s *windowsSystem) DiskSample() (snapshot DiskSnapshot) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	defer func() {
		if recover() != nil {
			snapshot = DiskSnapshot{}
		}
	}()
	if !s.diskReady || s.diskClosed {
		return DiskSnapshot{}
	}
	if r, _, _ := procPdhCollectQueryData.Call(s.query); r != 0 {
		return DiskSnapshot{}
	}
	usage, ok := pdhValue(s.usage)
	if !ok {
		return DiskSnapshot{}
	}
	read, _ := pdhValue(s.read)
	write, _ := pdhValue(s.write)
	queue, _ := pdhValue(s.queue)
	clamp := func(v, low, high float64) float64 {
		if v < low {
			return low
		}
		if v > high {
			return high
		}
		return v
	}
	return DiskSnapshot{Available: true, UsagePercent: clamp(usage, 0, 100), ReadBytesPerSec: clamp(read, 0, 1e300), WriteBytesPerSec: clamp(write, 0, 1e300), QueueLength: clamp(queue, 0, 1e300)}
}

func (s *windowsSystem) Close() {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if s.diskClosed {
		return
	}
	s.diskClosed = true
	if s.query != 0 {
		procPdhCloseQuery.Call(s.query)
	}
}

// NetworkCounters возвращает накопительные счётчики работающих адаптеров, кроме петлевых и туннельных.
func (s *windowsSystem) NetworkCounters() ([]NicCounters, error) {
	if err := procGetIfEntry2.Find(); err != nil {
		return nil, err
	}
	size := uint32(16 * 1024)
	var buffer []byte
	for attempt := 0; ; attempt++ {
		buffer = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW || attempt >= 4 {
			return nil, err
		}
	}
	var result []NicCounters
	for adapter := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])); adapter != nil; adapter = adapter.Next {
		if adapter.OperStatus != ifOperStatusUp || adapter.IfType == ifTypeSoftwareLoopbck || adapter.IfType == ifTypeTunnel {
			continue
		}
		// MIB_IF_ROW2 читается как буфер с фиксированными смещениями: описание структуры в
		// golang.org/x/sys для 32-битной сборки на 4 байта короче настоящей (1352 байта).
		var row [mibIfRow2Size / 8]uint64
		row[0] = adapter.Luid
		*(*uint32)(unsafe.Add(unsafe.Pointer(&row[0]), 8)) = adapter.IfIndex
		if r, _, _ := procGetIfEntry2.Call(uintptr(unsafe.Pointer(&row[0]))); r != 0 {
			continue
		}
		received, sent := row[mibIfRow2InOctets/8], row[mibIfRow2OutOctets/8]
		speed := float64(adapter.ReceiveLinkSpeed)
		if adapter.ReceiveLinkSpeed == ^uint64(0) || adapter.ReceiveLinkSpeed == 0 {
			speed = 0
		}
		result = append(result, NicCounters{ID: windows.BytePtrToString(adapter.AdapterName), Received: received, Sent: sent, Speed: speed})
	}
	return result, nil
}

func (s *windowsSystem) FileVersion(path string) (version string) {
	version = "?"
	defer func() { _ = recover() }()
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return
	}
	buffer := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buffer[0])) != nil {
		return
	}
	var info *windows.VS_FIXEDFILEINFO
	var length uint32
	if windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&info), &length) != nil || info == nil || length == 0 {
		return
	}
	return fmt.Sprintf("%d.%d.%d.%d", info.FileVersionMS>>16, info.FileVersionMS&0xffff, info.FileVersionLS>>16, info.FileVersionLS&0xffff)
}

// registryViews — 64- и 32-битное представления реестра: сервер 1С может быть установлен в любом.
var registryViews = []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY}

func subKeyStrings(view uint32, path string, visit func(key registry.Key)) {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.ENUMERATE_SUB_KEYS|view)
	if err != nil {
		return
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil && len(names) == 0 {
		return
	}
	for _, name := range names {
		key, err := registry.OpenKey(root, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		visit(key)
		key.Close()
	}
}

// ServiceImagePaths возвращает командные строки служб Windows, запускающих ragent.exe.
func (s *windowsSystem) ServiceImagePaths() []string {
	var result []string
	seen := map[string]bool{}
	for _, view := range registryViews {
		subKeyStrings(view, `SYSTEM\CurrentControlSet\Services`, func(key registry.Key) {
			image, _, err := key.GetStringValue("ImagePath")
			if err != nil || !strings.Contains(strings.ToLower(image), "ragent") {
				return
			}
			if expanded, err := registry.ExpandString(image); err == nil {
				image = expanded
			}
			if !seen[strings.ToLower(image)] {
				seen[strings.ToLower(image)] = true
				result = append(result, image)
			}
		})
	}
	return result
}

// InstalledOneCLocations возвращает каталоги установки программ 1С из списка установленных программ.
func (s *windowsSystem) InstalledOneCLocations() []string {
	var result []string
	seen := map[string]bool{}
	for _, view := range registryViews {
		for _, path := range []string{`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`} {
			subKeyStrings(view, path, func(key registry.Key) {
				name, _, _ := key.GetStringValue("DisplayName")
				if upper := strings.ToUpper(name); !strings.Contains(upper, "1C") && !strings.Contains(upper, "1С") {
					return
				}
				location, _, _ := key.GetStringValue("InstallLocation")
				if expanded, err := registry.ExpandString(location); err == nil {
					location = expanded
				}
				location = strings.Trim(strings.TrimSpace(location), `"`)
				if location != "" && !seen[strings.ToLower(location)] {
					seen[strings.ToLower(location)] = true
					result = append(result, location)
				}
			})
		}
	}
	return result
}

func knownFolder(id *windows.KNOWNFOLDERID, fallback string) string {
	if path, err := windows.KnownFolderPath(id, 0); err == nil && path != "" {
		return path
	}
	return fallback
}

func (s *windowsSystem) DocumentsDir() string {
	return knownFolder(windows.FOLDERID_Documents, s.GenericSystem.DocumentsDir())
}

func (s *windowsSystem) DesktopDir() string {
	return knownFolder(windows.FOLDERID_Desktop, s.GenericSystem.DesktopDir())
}

func (s *windowsSystem) ExecutableDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// Open открывает файл или ссылку программой по умолчанию.
func (s *windowsSystem) Open(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
