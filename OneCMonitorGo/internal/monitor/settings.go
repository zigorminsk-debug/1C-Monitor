package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings — настройки монитора. Имена полей в settings.json совпадают с версией программы
// на .NET, поэтому обе версии читают один и тот же файл %LOCALAPPDATA%\1CServerMonitor\settings.json.
type Settings struct {
	RacPath       string
	Endpoint      string
	AgentEndpoint string
	ClusterUser   string
	// Пароль администратора кластера хранится только в памяти и в settings.json не записывается.
	ClusterPassword    string `json:"-"`
	TechLogPath        string
	ReportFolder       string
	LastAutoReportDate string
	AutoDailyReport    bool
	// Базы, в которых фоновые задания запретил сам монитор (только их он включает обратно).
	BackgroundJobsDisabledInfobases []string

	AutoDrainEnabled         bool
	AutoDrainMemoryMb        float64
	AutoDrainMinutes         float64
	AutoDrainMinProcesses    float64
	AutoDrainCooldownMinutes float64

	IntervalSeconds     float64
	RacPollSeconds      float64
	TechLogPollSeconds  float64
	HistoryWriteSeconds float64
	AlertRepeatSeconds  float64
	RacTimeoutSeconds   float64
	CpuAlert            float64
	MemoryAlertMb       float64
	CallAlertMs         float64
	DiskAlert           float64
}

// DefaultSettings возвращает рекомендуемые значения.
func DefaultSettings() Settings {
	return Settings{
		Endpoint: "127.0.0.1:1545", AgentEndpoint: "127.0.0.1:1540", AutoDailyReport: true,
		AutoDrainMemoryMb: 6144, AutoDrainMinutes: 10, AutoDrainMinProcesses: 2, AutoDrainCooldownMinutes: 30,
		IntervalSeconds: 5, RacPollSeconds: 15, TechLogPollSeconds: 60, HistoryWriteSeconds: 60, AlertRepeatSeconds: 300,
		RacTimeoutSeconds: 60, CpuAlert: 80, MemoryAlertMb: 4096, CallAlertMs: 5000, DiskAlert: 90,
	}
}

func settingsFile(dataDir string) string { return filepath.Join(dataDir, "settings.json") }

// LoadSettings читает настройки; отсутствующий или повреждённый файл даёт значения по умолчанию.
func LoadSettings(dataDir string) Settings {
	s := DefaultSettings()
	data, err := os.ReadFile(settingsFile(dataDir))
	if err != nil {
		return s
	}
	loaded := s
	if json.Unmarshal(trimBOM(data), &loaded) != nil {
		return s
	}
	return loaded
}

func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// Save записывает настройки через временный файл: сбой посреди записи не оставит повреждённый
// settings.json (в нём хранится и список баз, фоновые задания которых выключал монитор).
func (s Settings) Save(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if s.BackgroundJobsDisabledInfobases == nil {
		s.BackgroundJobsDisabledInfobases = []string{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	file := settingsFile(dataDir)
	temp := file + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, file)
}

func (s Settings) clone() Settings {
	s.BackgroundJobsDisabledInfobases = append([]string(nil), s.BackgroundJobsDisabledInfobases...)
	return s
}
