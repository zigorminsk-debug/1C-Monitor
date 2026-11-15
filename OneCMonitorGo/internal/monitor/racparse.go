package monitor

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Record — одна запись вывода rac.exe: пары «ключ : значение», записи разделены пустой строкой.
type Record map[string]string

// Get возвращает значение или пустую строку.
func (r Record) Get(key string) string { return r[key] }

// ParseRecords разбирает текстовый вывод rac.exe.
func ParseRecords(text string) []Record {
	var list []Record
	var cur Record
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			if len(cur) > 0 {
				list = append(list, cur)
			}
			cur = nil
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 1 {
			continue
		}
		if cur == nil {
			cur = Record{}
		}
		cur[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	if len(cur) > 0 {
		list = append(list, cur)
	}
	return list
}

// norm приводит идентификатор к сравнимому виду: без кавычек, фигурных скобок и в нижнем регистре.
func norm(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), `"'{}`))
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func toInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func toNum(s string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(s, ",", ".")), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return n
}

var racDateLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05", "02.01.2006 15:04:05", "2006-01-02"}

// parseRacDate разбирает дату rac (локальное время сервера). При ошибке возвращает нулевое время.
func parseRacDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range racDateLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local()
	}
	return time.Time{}
}

func isTrue(s string) bool {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "yes") || strings.EqualFold(s, "true") || strings.EqualFold(s, "on") {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// RacProcess — показатели рабочего процесса из «rac process list».
type RacProcess struct {
	Pid         int
	ProcessUUID string
	MemoryMb    float64
	AvgCallMs   float64
	AvgDbMs     float64
}

// InfoBaseRef — информационная база кластера.
type InfoBaseRef struct {
	UUID                string
	Name                string
	ScheduledJobsDenied bool
}

// RacSnapshot — снимок состояния кластера. После публикации не изменяется.
type RacSnapshot struct {
	Processes map[int]RacProcess
	InfoBases map[string]InfoBaseRef // ключ — UUID в нижнем регистре
	Sessions  []SessionRow
}

type connectionLink struct {
	Pid         int
	Operation   string
	DbRequest   string
	Host        string
	InfoBase    string
	Application string
	ConnectedAt time.Time
	ConnID      int
}

func isMeaningfulID(id string) bool {
	return id != "" && id != "0" && !strings.Contains(id, "00000000-0000-0000-0000-000000000000")
}

// BuildSnapshot собирает снимок кластера из текстового вывода четырёх команд rac.exe:
// infobase summary list, process list, connection list и session list.
func BuildSnapshot(infoBaseOutput, processOutput, connectionOutput, sessionListOutput string) *RacSnapshot {
	ibRecords := ParseRecords(infoBaseOutput)
	ibNames := map[string]string{}
	for _, b := range ibRecords {
		if id := norm(b.Get("infobase")); id != "" {
			if _, seen := ibNames[id]; !seen {
				ibNames[id] = b.Get("name")
			}
		}
	}
	pr := ParseRecords(processOutput)
	cn := ParseRecords(connectionOutput)
	ss := ParseRecords(sessionListOutput)

	procUUIDToPid := map[string]int{}
	for _, p := range pr {
		if pid := toInt(p.Get("pid")); pid > 0 {
			if _, seen := procUUIDToPid[norm(p.Get("process"))]; !seen {
				procUUIDToPid[norm(p.Get("process"))] = pid
			}
		}
	}

	links := map[string]*connectionLink{}
	var candidates []*connectionLink
	addLink := func(kind, key, id string, link *connectionLink) {
		if isMeaningfulID(id) {
			links[kind+":"+key] = link
		}
	}
	for _, c := range cn {
		pid := toInt(c.Get("pid"))
		if pid <= 0 {
			pid = procUUIDToPid[norm(c.Get("process"))]
		}
		if pid <= 0 {
			pid = toInt(c.Get("process-id"))
		}
		link := &connectionLink{
			Pid:         pid,
			Operation:   orDefault(c.Get("current-service-call"), c.Get("current-service-name")),
			DbRequest:   c.Get("current-dbms-request"),
			Host:        norm(c.Get("host")),
			InfoBase:    norm(c.Get("infobase")),
			Application: norm(c.Get("application")),
			ConnectedAt: parseRacDate(c.Get("connected-at")),
			ConnID:      toInt(c.Get("conn-id")),
		}
		candidates = append(candidates, link)
		for _, field := range []struct{ kind, name string }{{"uuid", "connection"}, {"id", "conn-id"}, {"id", "connection-id"}} {
			key := norm(c.Get(field.name))
			addLink(field.kind, key, key, link)
		}
		// session-number соединения — номер обслуживаемого сеанса; он уникален только в пределах базы.
		number := norm(c.Get("session-number"))
		addLink("session", link.InfoBase+":"+number, number, link)
	}

	findLink := func(s Record) *connectionLink {
		// Номер самого сеанса в session list называется session-id.
		sessionNumber := norm(orDefault(s.Get("session-id"), s.Get("session-number")))
		keys := []struct{ kind, id, scope string }{
			{"uuid", norm(s.Get("connection")), ""},
			{"id", norm(s.Get("conn-id")), ""},
			{"id", norm(s.Get("connection-id")), ""},
			{"session", sessionNumber, norm(s.Get("infobase")) + ":"},
		}
		for _, k := range keys {
			if isMeaningfulID(k.id) {
				if found, ok := links[k.kind+":"+k.scope+k.id]; ok {
					return found
				}
			}
		}
		// У ожидающего сеанса поля connection/process бывают равны нулевому UUID. Тогда связь
		// восстанавливается по базе, клиентскому компьютеру, приложению и времени открытия.
		host, infobase, app := norm(s.Get("host")), norm(s.Get("infobase")), norm(s.Get("app-id"))
		started := parseRacDate(s.Get("started-at"))
		var matched []*connectionLink
		for _, c := range candidates {
			if c.Pid > 0 && c.Host == host && c.InfoBase == infobase && c.Application == app {
				matched = append(matched, c)
			}
		}
		distance := func(c *connectionLink) float64 {
			if started.IsZero() || c.ConnectedAt.IsZero() {
				return math.MaxFloat64
			}
			return math.Abs(c.ConnectedAt.Sub(started).Seconds())
		}
		if !started.IsZero() {
			var closeEnough []*connectionLink
			for _, c := range matched {
				if !c.ConnectedAt.IsZero() && distance(c) <= 5 {
					closeEnough = append(closeEnough, c)
				}
			}
			if len(closeEnough) == 0 {
				return nil
			}
			matched = closeEnough
		}
		if len(matched) == 0 {
			return nil
		}
		sort.SliceStable(matched, func(i, j int) bool {
			a, b := matched[i], matched[j]
			if (a.ConnID > 0) != (b.ConnID > 0) {
				return a.ConnID > 0
			}
			return distance(a) < distance(b)
		})
		return matched[0]
	}

	result := &RacSnapshot{Processes: map[int]RacProcess{}, InfoBases: map[string]InfoBaseRef{}}
	for _, b := range ibRecords {
		uuid := norm(b.Get("infobase"))
		if uuid == "" {
			continue
		}
		result.InfoBases[uuid] = InfoBaseRef{UUID: uuid, Name: orDefault(b.Get("name"), uuid), ScheduledJobsDenied: isTrue(b.Get("scheduled-jobs-deny"))}
	}
	for _, p := range pr {
		pid := toInt(p.Get("pid"))
		if pid <= 0 {
			continue
		}
		result.Processes[pid] = RacProcess{
			Pid: pid, ProcessUUID: norm(p.Get("process")),
			MemoryMb:  toNum(p.Get("memory-size")) / 1024,
			AvgCallMs: toNum(p.Get("avg-call-time")) * 1000,
			AvgDbMs:   toNum(p.Get("avg-db-call-time")) * 1000,
		}
	}
	for _, s := range ss {
		link := findLink(s)
		pid := 0
		linkOperation, linkDbRequest := "", ""
		if link != nil {
			pid, linkOperation, linkDbRequest = link.Pid, link.Operation, link.DbRequest
		}
		if pid <= 0 {
			pid = procUUIDToPid[norm(s.Get("process"))]
		}
		if pid <= 0 {
			pid = toInt(s.Get("pid"))
		}
		b1, b2 := s.Get("blocked-by-dbms"), s.Get("blocked-by-ls")
		blocked := NotBlocked
		if isTrue(b1) || isTrue(b2) {
			blocked = "да (DBMS=" + b1 + ", LS=" + b2 + ")"
		}
		result.Sessions = append(result.Sessions, SessionRow{
			SessionUUID:   norm(s.Get("session")),
			UserName:      orDefault(s.Get("user-name"), UnknownUser),
			InfoBase:      orDefault(ibNames[norm(s.Get("infobase"))], norm(s.Get("infobase"))),
			AppID:         s.Get("app-id"),
			Host:          s.Get("host"),
			Pid:           pid,
			SessionNumber: orDefault(s.Get("session-id"), orDefault(s.Get("session-number"), s.Get("session"))),
			StartedAt:     s.Get("started-at"),
			LastActiveAt:  s.Get("last-active-at"),
			IsSleeping:    isTrue(s.Get("hibernate")),

			MemoryCurrentMb:      toNum(s.Get("memory-current")) / 1048576,
			MemoryLast5MinMb:     toNum(s.Get("memory-last-5min")) / 1048576,
			CpuTimeCurrent:       toNum(s.Get("cpu-time-current")),
			CpuTimeLast5Min:      toNum(s.Get("cpu-time-last-5min")),
			DurationCurrentMs:    toNum(s.Get("duration-current")),
			DurationDbCurrentMs:  toNum(s.Get("duration-current-dbms")),
			DurationLast5MinMs:   toNum(s.Get("duration-last-5min")),
			DurationDbLast5MinMs: toNum(s.Get("duration-last-5min-dbms")),
			CallsLast5Min:        toInt(s.Get("calls-last-5min")),
			BytesLast5Min:        toNum(s.Get("bytes-last-5min")),
			DbBytesLast5Min:      toNum(s.Get("dbms-bytes-last-5min")),
			ReadCurrent:          toNum(s.Get("read-current")),
			WriteCurrent:         toNum(s.Get("write-current")),
			ReadLast5Min:         toNum(s.Get("read-last-5min")),
			WriteLast5Min:        toNum(s.Get("write-last-5min")),

			Operation: orDefault(s.Get("current-service-call"), orDefault(s.Get("current-service-name"), linkOperation)),
			DbRequest: orDefault(s.Get("current-dbms-request"), linkDbRequest),
			Blocked:   blocked,
		})
	}
	return result
}

// UnknownUser подставляется, когда RAC не сообщает имя пользователя сеанса.
const UnknownUser = "<не указан>"

// EndpointCandidates возвращает адрес RAS и его вариант localhost ↔ 127.0.0.1:
// на части серверов localhost разрешается в IPv6, а RAS слушает только IPv4.
func EndpointCandidates(configured string) []string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = "127.0.0.1:1545"
	}
	result := []string{configured}
	lower := strings.ToLower(configured)
	if strings.HasPrefix(lower, "localhost:") {
		result = append(result, "127.0.0.1:"+configured[strings.IndexByte(configured, ':')+1:])
	} else if strings.HasPrefix(lower, "127.0.0.1:") {
		result = append(result, "localhost:"+configured[strings.IndexByte(configured, ':')+1:])
	}
	return result
}
