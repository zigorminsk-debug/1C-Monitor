package monitor

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ragentImagePath = regexp.MustCompile(`(?i)(?:"([^"]*ragent\.exe)"|([A-Za-z]:\\.*?ragent\.exe))`)

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// pathDir возвращает каталог пути Windows (с обратными косыми чертами) независимо от платформы сборки.
func pathDir(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[:i]
	}
	return "."
}

type racFinder struct {
	mu       sync.Mutex
	cached   []string
	cachedAt time.Time
	stale    bool
}

func (f *racFinder) invalidate() {
	f.mu.Lock()
	f.stale = true
	f.mu.Unlock()
}

// candidates возвращает найденные rac.exe. Последний успешно подключившийся комплект всегда
// идёт первым, иначе нерабочий первый кандидат впустую запускался бы при каждом опросе.
func (f *racFinder) candidates(sys System, configuredPath, lastWorking string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.locked(sys, configuredPath)
	sort.SliceStable(list, func(i, j int) bool {
		return strings.EqualFold(list[i], lastWorking) && !strings.EqualFold(list[j], lastWorking)
	})
	return list
}

func (f *racFinder) locked(sys System, configuredPath string) []string {
	if !f.stale && !f.cachedAt.IsZero() && time.Since(f.cachedAt) < 10*time.Minute {
		allExist := true
		for _, path := range f.cached {
			if !fileExists(path) {
				allExist = false
				break
			}
		}
		if allExist {
			return append([]string(nil), f.cached...)
		}
	}
	f.stale = false
	result := FindRacExecutables(sys, configuredPath)
	f.cached = append([]string(nil), result...)
	f.cachedAt = time.Now()
	return result
}

// FindRacExecutables ищет rac.exe: путь из настроек, службы ragent в реестре, каталоги работающих
// процессов 1С, каталог программы, список установленных программ и типовые каталоги на дисках.
func FindRacExecutables(sys System, configuredPath string) []string {
	var result []string
	add := func(path string, requireRas bool) {
		if strings.TrimSpace(path) == "" || !fileExists(path) {
			return
		}
		if requireRas && !fileExists(filepath.Join(filepath.Dir(path), "ras.exe")) {
			return
		}
		for _, known := range result {
			if strings.EqualFold(known, path) {
				return
			}
		}
		result = append(result, path)
	}
	addFromDirectory := func(directory string) {
		if strings.TrimSpace(directory) == "" || !dirExists(directory) {
			return
		}
		// Полный рекурсивный обход не применяется: каталог D:\1C на сервере может содержать терабайты
		// баз и журналов. Ограниченный обход ищет только типовую структуру «версия\bin».
		type item struct {
			path  string
			depth int
		}
		var found []string
		queue := []item{{directory, 0}}
		for visited := 0; len(queue) > 0 && visited < 500; visited++ {
			current := queue[0]
			queue = queue[1:]
			for _, candidate := range []string{filepath.Join(current.path, "rac.exe"), filepath.Join(current.path, "bin", "rac.exe")} {
				if fileExists(candidate) {
					found = append(found, candidate)
				}
			}
			if current.depth >= 2 {
				continue
			}
			entries, err := os.ReadDir(current.path)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() {
					queue = append(queue, item{filepath.Join(current.path, entry.Name()), current.depth + 1})
				}
			}
		}
		versions := map[string][]int{}
		for _, file := range found {
			versions[file] = parseVersion(sys.FileVersion(file))
		}
		sort.SliceStable(found, func(i, j int) bool { return compareVersions(versions[found[i]], versions[found[j]]) > 0 })
		for _, file := range found {
			add(file, true)
		}
	}

	// Путь из настроек (или сохранённый после успешного подключения) проверяется первым. Для него
	// ras.exe рядом не требуется: rac.exe может подключаться к RAS, запущенному службой или на другом сервере.
	add(strings.Trim(strings.TrimSpace(configuredPath), `"`), false)

	// ImagePath служб ragent из обеих ветвей реестра. Это работает, даже когда 32-битный монитор
	// не может прочитать путь 64-битного процесса.
	for _, image := range sys.ServiceImagePaths() {
		if match := ragentImagePath.FindStringSubmatch(image); match != nil {
			path := match[1]
			if path == "" {
				path = match[2]
			}
			add(filepath.Join(pathDir(path), "rac.exe"), true)
		}
	}
	// Каталоги фактически работающих ragent/rphost/ras: путь ras особенно полезен,
	// если он запущен вручную из нестандартного каталога.
	for _, name := range []string{"ragent", "rphost", "ras"} {
		for _, image := range sys.ProcessImagePaths(name) {
			if strings.TrimSpace(image) != "" {
				add(filepath.Join(pathDir(image), "rac.exe"), true)
			}
		}
	}
	add(filepath.Join(sys.ExecutableDir(), "rac.exe"), true)

	// InstallLocation из списка установленных программ помогает при нестандартном каталоге.
	for _, location := range sys.InstalledOneCLocations() {
		addFromDirectory(location)
	}

	var roots []string
	addRoot := func(path string) {
		if path == "" || !dirExists(path) {
			return
		}
		for _, known := range roots {
			if strings.EqualFold(known, path) {
				return
			}
		}
		roots = append(roots, path)
	}
	for _, dir := range sys.ProgramFilesDirs() {
		addRoot(dir)
	}
	for _, drive := range sys.FixedDriveRoots() {
		for _, relative := range []string{"Program Files", "Program Files (x86)", "1cv8", "1C", "1С"} {
			addRoot(filepath.Join(drive, relative))
		}
	}
	for _, root := range roots {
		addFromDirectory(filepath.Join(root, "1cv8"))
		// Каталоги вида D:\1cv8 и D:\1C просматриваются и сами (обход ограничен глубиной и числом папок).
		name := filepath.Base(root)
		if strings.EqualFold(name, "1cv8") || strings.EqualFold(name, "1C") || strings.EqualFold(name, "1С") {
			addFromDirectory(root)
		}
	}
	return result
}

// parseVersion разбирает версию вида «8.3.22.1709»; нераспознанная строка даёт пустую версию.
func parseVersion(text string) []int {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	var parts []int
	for _, part := range strings.Split(fields[0], ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		parts = append(parts, n)
	}
	return parts
}

func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

var architectureCache sync.Map

// ArchitectureLabel возвращает разрядность исполняемого файла. Она читается из заголовка PE
// (один раз на файл), а не угадывается по папке.
func ArchitectureLabel(executable string) string {
	if cached, ok := architectureCache.Load(strings.ToLower(executable)); ok {
		return cached.(string)
	}
	if label := ReadPEArchitecture(executable); label != "" {
		architectureCache.Store(strings.ToLower(executable), label)
		return label
	}
	return "разрядность не определена"
}

// ReadPEArchitecture читает поле Machine заголовка PE. Пустая строка — файл не распознан.
func ReadPEArchitecture(executable string) string {
	f, err := os.Open(executable)
	if err != nil {
		return ""
	}
	defer f.Close()
	header := make([]byte, 0x40)
	if _, err := io.ReadFull(f, header); err != nil || header[0] != 'M' || header[1] != 'Z' {
		return ""
	}
	peOffset := int64(int32(binary.LittleEndian.Uint32(header[0x3C:])))
	if peOffset <= 0 {
		return ""
	}
	pe := make([]byte, 6)
	if _, err := f.ReadAt(pe, peOffset); err != nil || string(pe[:4]) != "PE\x00\x00" {
		return ""
	}
	switch binary.LittleEndian.Uint16(pe[4:]) {
	case 0x014C:
		return "x86"
	case 0x8664:
		return "x64"
	case 0xAA64:
		return "ARM64"
	}
	return ""
}
