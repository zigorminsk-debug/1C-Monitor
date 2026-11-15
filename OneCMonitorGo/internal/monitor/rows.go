package monitor

import "time"

// ProcessRow — строка таблицы рабочих процессов rphost.
type ProcessRow struct {
	Pid          int
	ProcessUUID  string
	Cpu          float64
	MemoryMb     float64
	RacMemoryMb  float64
	DrainStatus  string
	SessionCount int
	AvgCallMs    float64
	AvgDbMs      float64
	Users        string
	Risk         int
	Diagnosis    string
}

func (p ProcessRow) CpuText() string     { return num(p.Cpu, 1) + " %" }
func (p ProcessRow) MemoryText() string  { return FormatMb(p.MemoryMb) }
func (p ProcessRow) AvgCallText() string { return num(p.AvgCallMs, 0) + " мс" }
func (p ProcessRow) AvgDbText() string   { return num(p.AvgDbMs, 0) + " мс" }
func (p ProcessRow) RiskText() string    { return itoa(p.Risk) }

// RiskLevel: 0 — норма, 1 — внимание, 2 — высокий, 3 — критичный.
func (p ProcessRow) RiskLevel() int { return riskLevel(p.Risk) }

func riskLevel(risk int) int {
	switch {
	case risk >= 80:
		return 3
	case risk >= 50:
		return 2
	case risk >= 20:
		return 1
	}
	return 0
}

// SessionRow — сеанс 1С по данным RAC, дополненный нагрузкой процесса и контекстом техжурнала.
type SessionRow struct {
	SessionUUID   string
	UserName      string
	InfoBase      string
	AppID         string
	Host          string
	Pid           int
	SessionNumber string
	StartedAt     string
	LastActiveAt  string
	IsSleeping    bool

	MemoryCurrentMb      float64
	MemoryLast5MinMb     float64
	CpuTimeCurrent       float64
	CpuTimeLast5Min      float64
	DurationCurrentMs    float64
	DurationDbCurrentMs  float64
	DurationLast5MinMs   float64
	DurationDbLast5MinMs float64
	CallsLast5Min        int
	BytesLast5Min        float64
	DbBytesLast5Min      float64
	ReadCurrent          float64
	WriteCurrent         float64
	ReadLast5Min         float64
	WriteLast5Min        float64

	ProcessCpu      float64
	ProcessMemoryMb float64
	EstimatedCpu    float64

	TechContext string
	LastCallMs  float64
	Operation   string
	DbRequest   string
	Blocked     string // «нет» либо описание блокировки
}

// IsBlocked сообщает, ждёт ли сеанс блокировки СУБД или менеджера блокировок 1С.
func (s SessionRow) IsBlocked() bool { return s.Blocked != "" && s.Blocked != NotBlocked }

// NotBlocked — значение поля Blocked для сеанса без блокировок.
const NotBlocked = "нет"

func (s SessionRow) SessionState() string {
	switch {
	case s.IsSleeping:
		return "Спящий"
	case s.DurationCurrentMs > 0:
		return "Выполняет вызов"
	case s.CallsLast5Min > 0:
		return "Активен за 5 мин"
	}
	return "Ожидает"
}

func (s SessionRow) SessionMemoryText() string {
	if s.MemoryCurrentMb >= 1024 {
		return num(s.MemoryCurrentMb/1024, 2) + " ГБ"
	}
	return num(s.MemoryCurrentMb, 1) + " МБ"
}

func (s SessionRow) SessionCpuTimeText() string {
	if s.CpuTimeCurrent > 0 {
		return num(s.CpuTimeCurrent, 0) + " мс"
	}
	return "0 мс"
}

func (s SessionRow) CurrentCallText() string {
	if s.DurationCurrentMs > 0 {
		return num(s.DurationCurrentMs/1000, 1) + " с"
	}
	return "—"
}

func (s SessionRow) CurrentDbText() string {
	if s.DurationDbCurrentMs > 0 {
		return num(s.DurationDbCurrentMs/1000, 1) + " с"
	}
	return "—"
}

func (s SessionRow) IoCurrentText() string {
	if s.ReadCurrent > 0 || s.WriteCurrent > 0 {
		return "R " + num(s.ReadCurrent/1048576, 1) + " / W " + num(s.WriteCurrent/1048576, 1) + " МБ"
	}
	return "—"
}

func (s SessionRow) Load5MinText() string {
	delta := num(s.MemoryLast5MinMb, 1)
	if delta == "0,0" || delta == "-0,0" {
		delta = "0,0"
	} else if s.MemoryLast5MinMb > 0 {
		delta = "+" + delta
	}
	return "вызовы " + itoa(s.CallsLast5Min) + " • CPU " + num(s.CpuTimeLast5Min, 0) + " мс • время " + num(s.DurationLast5MinMs/1000, 1) +
		" с • БД " + num(s.DurationDbLast5MinMs/1000, 1) + " с • ΔRAM " + delta + " МБ"
}

func (s SessionRow) Io5MinText() string {
	return "R " + num(s.ReadLast5Min/1024, 1) + " / W " + num(s.WriteLast5Min/1024, 1) + " КБ • сеть " + num(s.BytesLast5Min/1024, 1) +
		" КБ • СУБД " + num(s.DbBytesLast5Min/1024, 1) + " КБ"
}

func (s SessionRow) ProcessCpuText() string {
	if s.Pid > 0 {
		return num(s.ProcessCpu, 1) + " %"
	}
	return "—"
}

func (s SessionRow) ProcessMemoryText() string {
	if s.Pid > 0 {
		return FormatMb(s.ProcessMemoryMb)
	}
	return "—"
}

func (s SessionRow) EstimatedCpuText() string {
	if s.Pid > 0 {
		return "≈ " + num(s.EstimatedCpu, 1) + " %"
	}
	return "—"
}

func (s SessionRow) LastCallText() string {
	if s.LastCallMs > 0 {
		return num(s.LastCallMs, 0) + " мс"
	}
	return "—"
}

func (s SessionRow) PidText() string {
	if s.Pid > 0 {
		return itoa(s.Pid)
	}
	return "—"
}

// AlertRow — запись в списке тревог.
type AlertRow struct {
	At       time.Time
	Severity string
	Source   string
	Message  string
}

func (a AlertRow) TimeText() string { return a.At.Format("02.01.2006 15:04:05") }

// DiskSnapshot — нагрузка на физические диски (счётчики PhysicalDisk(_Total)).
type DiskSnapshot struct {
	Available        bool
	UsagePercent     float64
	ReadBytesPerSec  float64
	WriteBytesPerSec float64
	QueueLength      float64
}

// NetworkSnapshot — суммарная нагрузка на сетевые адаптеры.
type NetworkSnapshot struct {
	Available          bool
	ReceiveBytesPerSec float64
	SendBytesPerSec    float64
	UtilizationPercent float64
	InterfaceCount     int
}
