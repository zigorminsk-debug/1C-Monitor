package monitor

import (
	"strings"
	"time"
)

const maxLogLines = 2000

// Log добавляет запись в журнал. Потокобезопасно; не вызывать, удерживая m.mu.
func (m *Monitor) Log(text string) {
	stamp := "[" + time.Now().Format("02.01.2006 15:04:05") + "] "
	m.logMu.Lock()
	for i, line := range strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r", ""), "\n"), "\n") {
		if i == 0 {
			line = stamp + line
		}
		m.logLines = append(m.logLines, line)
	}
	if len(m.logLines) > maxLogLines {
		m.logLines = append([]string(nil), m.logLines[len(m.logLines)-maxLogLines*4/5:]...)
	}
	m.logVersion++
	m.logMu.Unlock()
	m.notify()
}

// LogOnce записывает сообщение не чаще одного раза за repeatAfter для данного ключа.
func (m *Monitor) LogOnce(key, text string, repeatAfter time.Duration) {
	now := time.Now()
	key = strings.ToLower(key)
	m.logMu.Lock()
	if last, ok := m.logOnce[key]; ok && now.Sub(last) < repeatAfter {
		m.logMu.Unlock()
		return
	}
	if len(m.logOnce) > 500 {
		for k, at := range m.logOnce {
			if now.Sub(at) > time.Hour {
				delete(m.logOnce, k)
			}
		}
	}
	m.logOnce[key] = now
	m.logMu.Unlock()
	m.Log(text)
}

// LogLines возвращает строки журнала (старые — первыми) и номер версии, растущий при каждой записи.
func (m *Monitor) LogLines() ([]string, int) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	return append([]string(nil), m.logLines...), m.logVersion
}

// LogVersion возвращает номер версии журнала без копирования строк.
func (m *Monitor) LogVersion() int {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	return m.logVersion
}

// LogText возвращает журнал одним текстом.
func (m *Monitor) LogText() string {
	lines, _ := m.LogLines()
	return strings.Join(lines, "\n")
}
