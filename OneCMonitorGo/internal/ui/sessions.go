package ui

import (
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

func itoa(n int) string { return strconv.Itoa(n) }

var sessionColumns = []column{
	{"Состояние", 135}, {"Пользователь", 170}, {"База", 130}, {"Приложение", 120}, {"Компьютер", 130}, {"PID rphost", 85},
	{"Память вызова RAC", 160}, {"CPU-время RAC", 125}, {"Текущий вызов", 125}, {"Вызов СУБД", 105}, {"I/O сеанса", 150},
	{"Нагрузка за 5 минут", 430}, {"I/O за 5 минут", 380}, {"CPU rphost", 85}, {"RAM rphost", 95}, {"CPU пользователя*", 150},
	{"Начало", 150}, {"Активность", 150}, {"Метод/операция RAC", 260}, {"Документ/контекст ТЖ", 420}, {"Последний вызов ТЖ", 160},
	{"Запрос СУБД", 300}, {"Блокировка", 220},
}

func sessionCell(s monitor.SessionRow, col int) cellValue {
	switch col {
	case 0:
		switch {
		case s.IsBlocked():
			return cellValue{"Блокировка", widget.DangerImportance, true}
		case s.IsSleeping:
			return cellValue{s.SessionState(), widget.LowImportance, false}
		}
		return cellValue{s.SessionState(), widget.SuccessImportance, false}
	case 1:
		return cellValue{text: s.UserName, bold: true}
	case 2:
		return cellValue{text: s.InfoBase}
	case 3:
		return cellValue{text: s.AppID}
	case 4:
		return cellValue{text: s.Host}
	case 5:
		return cellValue{text: s.PidText()}
	case 6:
		return cellValue{text: s.SessionMemoryText()}
	case 7:
		return cellValue{text: s.SessionCpuTimeText()}
	case 8:
		return cellValue{text: s.CurrentCallText()}
	case 9:
		return cellValue{text: s.CurrentDbText()}
	case 10:
		return cellValue{text: s.IoCurrentText()}
	case 11:
		return cellValue{text: s.Load5MinText()}
	case 12:
		return cellValue{text: s.Io5MinText()}
	case 13:
		return cellValue{text: s.ProcessCpuText()}
	case 14:
		return cellValue{text: s.ProcessMemoryText()}
	case 15:
		return cellValue{text: s.EstimatedCpuText()}
	case 16:
		return cellValue{text: s.StartedAt}
	case 17:
		return cellValue{text: s.LastActiveAt}
	case 18:
		return cellValue{text: s.Operation}
	case 19:
		return cellValue{text: s.TechContext}
	case 20:
		return cellValue{text: s.LastCallText()}
	case 21:
		return cellValue{text: s.DbRequest}
	}
	if s.IsBlocked() {
		return cellValue{s.Blocked, widget.DangerImportance, true}
	}
	return cellValue{text: s.Blocked}
}

func sessionMatches(s monitor.SessionRow, filter string) bool {
	if filter == "" {
		return true
	}
	for _, value := range []string{s.UserName, s.InfoBase, s.Host, s.AppID, s.PidText(), s.TechContext, s.Operation} {
		if strings.Contains(strings.ToLower(value), filter) {
			return true
		}
	}
	return false
}

func orDash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "—"
	}
	return text
}

func sessionDetails(s monitor.SessionRow) string {
	return "Пользователь: " + s.UserName + " · база: " + s.InfoBase + " · компьютер: " + orDash(s.Host) + " · приложение: " + orDash(s.AppID) +
		" · PID rphost: " + s.PidText() + " · сеанс № " + orDash(s.SessionNumber) + " · " + s.SessionState() +
		"\nНачало: " + orDash(s.StartedAt) + " · последняя активность: " + orDash(s.LastActiveAt) + " · блокировка: " + orDash(s.Blocked) +
		"\nНагрузка за 5 минут: " + s.Load5MinText() +
		"\nМетод/операция RAC: " + orDash(s.Operation) +
		"\nДокумент/контекст ТЖ: " + orDash(s.TechContext) + " (последний вызов " + s.LastCallText() + ")" +
		"\nЗапрос СУБД: " + orDash(s.DbRequest)
}

func (u *UI) sessionsTab() fyne.CanvasObject {
	var rows []monitor.SessionRow
	filter := ""

	hint := note("CPU/RAM учитываются Windows для процесса rphost. «CPU пользователя» — оценочная доля среди сеансов процесса. Точный документ и строка кода доступны только через технологический журнал 1С.")
	counters := boldLabel("")
	shown := caption("показаны все")
	search := widget.NewEntry()
	search.SetPlaceHolder("Поиск: пользователь, база, компьютер, приложение, PID, контекст")

	details := wrapLabel("Выберите сеанс, чтобы увидеть полный текст операции, контекста техжурнала и запроса СУБД.")
	details.Selectable = true
	detailsScroll := container.NewVScroll(details)
	detailsScroll.SetMinSize(fyne.NewSize(0, 118))

	table := newDataTable(sessionColumns, func() int { return len(rows) }, func(row, col int) cellValue { return sessionCell(rows[row], col) })
	table.StickyColumnCount = 2
	table.onSelect = func(row int) {
		if row >= 0 && row < len(rows) {
			u.selectedSession = rows[row].SessionUUID
			details.SetText(sessionDetails(rows[row]))
		}
	}

	var source []monitor.SessionRow
	rebuild := func() {
		rows = rows[:0:0]
		selected := -1
		for _, s := range source {
			if !sessionMatches(s, filter) {
				continue
			}
			if s.SessionUUID == u.selectedSession && u.selectedSession != "" {
				selected = len(rows)
			}
			rows = append(rows, s)
		}
		if selected >= 0 {
			details.SetText(sessionDetails(rows[selected]))
		}
		table.markRow(selected)
		table.Refresh()
		if len(rows) == len(source) {
			setText(shown, "показаны все")
		} else {
			setText(shown, "по фильтру: "+itoa(len(rows)))
		}
	}
	search.OnChanged = func(text string) {
		filter = strings.ToLower(strings.TrimSpace(text))
		rebuild()
	}
	u.onUpdate(func(view monitor.View) {
		setText(counters, "Активные: "+itoa(view.ActiveSessionCount)+"  •  спящие: "+itoa(view.SleepingSessionCount)+"  •  всего сеансов: "+itoa(view.SessionCount))
		if !sameSlice(source, view.Sessions) {
			source = view.Sessions
			rebuild()
		}
	})
	top := container.NewVBox(card(hint), container.NewBorder(nil, nil, counters, shown, search))
	bottom := card(container.NewBorder(caption("ПОДРОБНОСТИ ВЫБРАННОГО СЕАНСА"), nil, nil, nil, detailsScroll))
	return container.NewBorder(top, bottom, nil, nil, card(table.Table))
}
