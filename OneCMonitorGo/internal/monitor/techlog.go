package monitor

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TechLogEvent — событие технологического журнала 1С, у которого заполнено свойство Context.
type TechLogEvent struct {
	At         time.Time
	InfoBase   string
	SessionID  string
	User       string
	Context    string
	DurationMs float64
}

// Событие начинается строкой «мм:сс.микросекунды-длительность,ИМЯ,уровень,свойство=значение,…»
// и может занимать несколько строк: значения в кавычках (Context, Sql) содержат переводы строк.
var techLogEventStart = regexp.MustCompile(`^(\d{2}):(\d{2})\.(\d+)-(\d+),`)

// ReadTechLogTail читает события с контекстом из последних tailBytes байт файла.
func ReadTechLogTail(file string, tailBytes int64) ([]TechLogEvent, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fromMiddle := info.Size() > tailBytes
	if fromMiddle {
		if _, err := f.Seek(-tailBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, tailBytes+1))
	if err != nil {
		return nil, err
	}
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	if fromMiddle {
		// первая строка после перехода в середину файла неполная
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	} else {
		text = strings.TrimPrefix(text, "\uFEFF")
	}
	return ParseTechLog(text, techLogFileHour(file, info.ModTime())), nil
}

// Имя файла техжурнала — ггММддчч.log; минуты и секунды записаны в самих событиях.
func techLogFileHour(file string, modified time.Time) time.Time {
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if t, err := time.ParseInLocation("06010215", name, time.Local); err == nil {
		return t
	}
	return time.Date(modified.Year(), modified.Month(), modified.Day(), modified.Hour(), 0, 0, 0, modified.Location())
}

// ParseTechLog разбирает текст техжурнала и возвращает события с непустым контекстом.
func ParseTechLog(text string, fileHour time.Time) []TechLogEvent {
	var events []TechLogEvent
	var current []string
	flush := func() {
		if current == nil {
			return
		}
		if ev, ok := parseTechLogEvent(strings.Join(current, "\n"), fileHour); ok {
			events = append(events, ev)
		}
		current = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if techLogEventStart.MatchString(line) {
			flush()
			current = []string{line}
		} else if current != nil {
			current = append(current, line) // продолжение многострочного события
		}
	}
	flush()
	return events
}

func parseTechLogEvent(text string, fileHour time.Time) (TechLogEvent, bool) {
	if !strings.Contains(strings.ToLower(text), "context=") {
		return TechLogEvent{}, false
	}
	header := techLogEventStart.FindStringSubmatch(text)
	if header == nil {
		return TechLogEvent{}, false
	}
	properties := parseTechLogProperties(text, len(header[0]))
	context := summarizeContext(properties["context"])
	if context == "" {
		return TechLogEvent{}, false
	}
	minutes, _ := strconv.Atoi(header[1])
	seconds, _ := strconv.Atoi(header[2])
	at := fileHour.Add(time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second)
	if fraction, err := strconv.ParseFloat("0."+header[3], 64); err == nil {
		at = at.Add(time.Duration(fraction * float64(time.Second)))
	}
	microseconds, _ := strconv.ParseFloat(header[4], 64)
	return TechLogEvent{
		At:         at,
		InfoBase:   strings.TrimSpace(properties["p:processname"]),
		SessionID:  strings.TrimSpace(properties["sessionid"]),
		User:       strings.TrimSpace(properties["usr"]),
		Context:    context,
		DurationMs: microseconds / 1000,
	}, true
}

// parseTechLogProperties разбирает «ключ=значение» через запятую. Значение может быть в одинарных
// или двойных кавычках (кавычка внутри удваивается) и тогда может содержать запятые и переводы строк.
// Ключи возвращаются в нижнем регистре.
func parseTechLogProperties(text string, start int) map[string]string {
	result := map[string]string{}
	i, n := start, len(text)
	for i < n {
		eq := strings.IndexByte(text[i:], '=')
		if eq < 0 {
			break
		}
		eq += i
		if comma := strings.IndexByte(text[i:], ','); comma >= 0 && comma+i < eq {
			i += comma + 1 // поле без «=»: имя события, уровень
			continue
		}
		key := strings.ToLower(strings.TrimSpace(text[i:eq]))
		i = eq + 1
		var value string
		if i < n && (text[i] == '\'' || text[i] == '"') {
			quote := text[i]
			i++
			var buffer strings.Builder
			for i < n {
				if text[i] == quote {
					if i+1 < n && text[i+1] == quote {
						buffer.WriteByte(quote)
						i += 2
						continue
					}
					i++
					break
				}
				buffer.WriteByte(text[i])
				i++
			}
			value = buffer.String()
			if next := strings.IndexByte(text[i:], ','); next < 0 {
				i = n
			} else {
				i += next + 1
			}
		} else {
			end := strings.IndexByte(text[i:], ',')
			if end < 0 {
				end = n
			} else {
				end += i
			}
			value = strings.TrimSpace(text[i:end])
			i = end + 1
		}
		if key != "" {
			result[key] = value
		}
	}
	return result
}

// summarizeContext сворачивает многострочный стек контекста в «точка входа → самая глубокая строка кода».
func summarizeContext(context string) string {
	var lines []string
	for _, line := range strings.Split(context, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	summary := lines[0]
	if last := lines[len(lines)-1]; len(lines) > 1 && last != lines[0] {
		summary = lines[0] + " → " + last
	}
	if runes := []rune(summary); len(runes) > 500 {
		summary = string(runes[:500]) + "…"
	}
	return summary
}

// TechLogIndex ищет последнее событие техжурнала для сеанса. Номер сеанса (SessionID) уникален
// только в пределах информационной базы, поэтому сопоставление всегда учитывает базу
// (p:processName) либо пару «номер + пользователь».
type TechLogIndex struct {
	byInfoBaseAndSession map[string]TechLogEvent
	byInfoBaseAndUser    map[string]TechLogEvent
	bySessionAndUser     map[string]TechLogEvent
}

// NewTechLogIndex создаёт пустой индекс.
func NewTechLogIndex() *TechLogIndex {
	return &TechLogIndex{map[string]TechLogEvent{}, map[string]TechLogEvent{}, map[string]TechLogEvent{}}
}

func techKey(first, second string) string {
	return strings.ToLower(first) + "\x1f" + strings.ToLower(second)
}

// В индексе остаётся самое позднее событие, независимо от порядка чтения файлов.
func putLatest(m map[string]TechLogEvent, key string, item TechLogEvent) {
	if existing, ok := m[key]; !ok || !item.At.Before(existing.At) {
		m[key] = item
	}
}

// Add добавляет событие в индекс.
func (x *TechLogIndex) Add(item TechLogEvent) {
	if item.InfoBase != "" && item.SessionID != "" {
		putLatest(x.byInfoBaseAndSession, techKey(item.InfoBase, item.SessionID), item)
	}
	if item.InfoBase != "" && item.User != "" {
		putLatest(x.byInfoBaseAndUser, techKey(item.InfoBase, item.User), item)
	}
	if item.SessionID != "" && item.User != "" {
		putLatest(x.bySessionAndUser, techKey(item.SessionID, item.User), item)
	}
}

// Find ищет событие: по базе и номеру сеанса, затем по номеру сеанса и пользователю, затем по базе и пользователю.
func (x *TechLogIndex) Find(infoBase, sessionNumber, user string) (TechLogEvent, bool) {
	infoBase, sessionNumber, user = strings.TrimSpace(infoBase), strings.TrimSpace(sessionNumber), strings.TrimSpace(user)
	if infoBase != "" && sessionNumber != "" {
		if ev, ok := x.byInfoBaseAndSession[techKey(infoBase, sessionNumber)]; ok {
			return ev, true
		}
	}
	if sessionNumber != "" && user != "" {
		if ev, ok := x.bySessionAndUser[techKey(sessionNumber, user)]; ok {
			return ev, true
		}
	}
	if infoBase != "" && user != "" {
		if ev, ok := x.byInfoBaseAndUser[techKey(infoBase, user)]; ok {
			return ev, true
		}
	}
	return TechLogEvent{}, false
}
