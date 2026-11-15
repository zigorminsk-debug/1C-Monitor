package ui

import (
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

var programColumns = []column{{"Программа", 190}, {"CPU", 75}, {"Память", 95}, {"Чтение", 95}, {"Запись", 95}}

var actionColumns = []column{
	{"Пользователь", 165}, {"База", 105}, {"rphost", 65}, {"Состояние", 130}, {"Вызов", 70}, {"СУБД", 70}, {"CPU*", 80},
	{"Что выполнялось", 380}, {"Запрос СУБД", 280}, {"Компьютер", 120}, {"Приложение", 110},
}

type loadRange struct {
	title string
	span  time.Duration // 0 — вся история
}

var loadRanges = []loadRange{{"5 минут", 5 * time.Minute}, {"15 минут", 15 * time.Minute}, {"1 час", time.Hour}, {"Вся история", 0}}

func cpuValue(p monitor.LoadPoint) float64   { return p.CpuTotal }
func cpu1CValue(p monitor.LoadPoint) float64 { return p.Cpu1C }
func memValue(p monitor.LoadPoint) float64   { return p.MemoryPercent() }
func mem1CValue(p monitor.LoadPoint) float64 { return p.Memory1CPercent() }
func diskValue(p monitor.LoadPoint) float64 {
	if !p.DiskOK {
		return -1
	}
	return p.DiskUsage
}

// measureGap оценивает обычный интервал между измерениями и возвращает разрыв, после которого
// линия графика прерывается (пауза мониторинга, зависший такт).
func measureGap(points []monitor.LoadPoint) time.Duration {
	var steps []time.Duration
	for i := len(points) - 1; i > 0 && len(steps) < 60; i-- {
		steps = append(steps, points[i].At.Sub(points[i-1].At))
	}
	if len(steps) == 0 {
		return 0
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i] < steps[j] })
	gap := steps[len(steps)/2] * 4
	if gap < 20*time.Second {
		gap = 20 * time.Second
	}
	return gap
}

// loadTab — вкладка «Графики нагрузки»: три графика и разбор выбранного момента.
func (u *UI) loadTab() fyne.CanvasObject {
	var (
		history  []monitor.LoadPoint // вся история монитора
		visible  []monitor.LoadPoint // точки выбранного диапазона
		selected *monitor.LoadPoint  // nil — показывается последнее измерение
		hovered  *monitor.LoadPoint
		shown    monitor.LoadPoint // точка, разобранная в нижней части
		span     = loadRanges[1].span
		// detailsReady — нижняя часть уже заполнена для точки shown.
		detailsReady bool
	)

	cpu := newLoadChart("Процессор", monitor.LoadPoint.CpuText,
		chartSeries{"весь сервер", colPrimary, cpuValue}, chartSeries{"процессы 1С", colWarning, cpu1CValue})
	memory := newLoadChart("Память", monitor.LoadPoint.MemoryText,
		chartSeries{"занято на сервере", colPrimary, memValue}, chartSeries{"процессы 1С", colWarning, mem1CValue})
	disk := newLoadChart("Диск", monitor.LoadPoint.DiskText, chartSeries{"время активности", colSuccess, diskValue})
	charts := []*loadChart{cpu, memory, disk}

	moment := boldLabel("")
	moment.Truncation = fyne.TextTruncateEllipsis
	explanation := wrapLabel("")
	explanation.Selectable = true
	u.loadCharts, u.loadMoment, u.loadExplanation = charts, moment, explanation
	programs := newDataTable(programColumns, func() int { return len(shown.Programs) }, func(row, col int) cellValue {
		p := shown.Programs[row]
		value := cellValue{}
		if p.OneC {
			value.importance, value.bold = widget.WarningImportance, col == 0
		}
		switch col {
		case 0:
			value.text = p.Label()
		case 1:
			value.text = p.CpuText()
		case 2:
			value.text = p.MemoryText()
		case 3:
			value.text = p.ReadText()
		default:
			value.text = p.WriteText()
		}
		return value
	})
	actions := newDataTable(actionColumns, func() int { return len(shown.Actions) }, func(row, col int) cellValue {
		a := shown.Actions[row]
		switch col {
		case 0:
			return cellValue{text: a.User(), bold: true}
		case 1:
			return cellValue{text: a.InfoBase}
		case 2:
			return cellValue{text: a.PidText()}
		case 3:
			if a.IsBlocked() {
				return cellValue{"Блокировка", widget.DangerImportance, true}
			}
			return cellValue{text: a.State}
		case 4:
			return cellValue{text: a.CallText()}
		case 5:
			return cellValue{text: a.DbText()}
		case 6:
			return cellValue{text: a.CpuText()}
		case 7:
			return cellValue{text: a.What()}
		case 8:
			return cellValue{text: a.DbRequest}
		case 9:
			return cellValue{text: a.Host}
		}
		return cellValue{text: a.AppID}
	})
	actionDetails := note("Выберите строку, чтобы увидеть действие полностью.")
	actionDetails.Selectable = true
	actions.onSelect = func(row int) {
		if row < 0 || row >= len(shown.Actions) {
			return
		}
		a := shown.Actions[row]
		text := a.User() + " · " + a.InfoBase + " · " + a.What()
		if a.Operation != "" && a.Operation != a.What() {
			text += "\nМетод RAC: " + a.Operation
		}
		if a.DbRequest != "" && a.DbRequest != a.What() {
			text += "\nЗапрос СУБД: " + a.DbRequest
		}
		if a.IsBlocked() {
			text += "\nБлокировка: " + a.Blocked
		}
		actionDetails.SetText(text)
	}
	programsTitle := caption("ПРОГРАММЫ WINDOWS")
	follow := widget.NewButton("К текущему моменту", nil)

	showDetails := func() {
		point := monitor.LoadPoint{}
		switch {
		case selected != nil:
			point = *selected
			setText(moment, "Выбран момент "+point.TimeText()+" — что нагружало сервер")
			follow.Enable()
		case len(history) > 0:
			point = history[len(history)-1]
			setText(moment, "Сейчас, "+point.TimeText()+" — щёлкните по пику на графике, чтобы разобрать его")
			follow.Disable()
		default:
			setText(moment, "Измерений пока нет")
			follow.Disable()
		}
		if detailsReady && point.At.Equal(shown.At) {
			return
		}
		shown, detailsReady = point, true
		lines := point.Explain()
		if point.At.IsZero() {
			lines = []string{"График появится после первого измерения."}
		}
		setText(explanation, strings.Join(lines, "\n"))
		title := "ПРОГРАММЫ WINDOWS"
		if !point.AllPrograms && !point.At.IsZero() {
			title = "ПРОГРАММЫ WINDOWS — список процессов недоступен, показаны только rphost"
		}
		setText(programsTitle, title)
		programs.markRow(-1)
		actions.markRow(-1)
		programs.Refresh()
		actions.Refresh()
		actionDetails.SetText("Выберите строку, чтобы увидеть действие полностью.")
	}
	showMarks := func() {
		for _, chart := range charts {
			chart.setMarks(selected, hovered)
		}
	}
	showCharts := func() {
		visible = history
		from, to := time.Time{}, time.Time{}
		if len(history) > 0 {
			to = history[len(history)-1].At
			from = history[0].At
			if span > 0 && to.Sub(from) > span {
				from = to.Add(-span)
				first := sort.Search(len(history), func(i int) bool { return !history[i].At.Before(from) })
				visible = history[first:]
			}
			if to.Sub(from) < time.Minute {
				from = to.Add(-time.Minute) // в первые секунды работы ось не растягивается на пару точек
			}
		}
		gap := measureGap(visible)
		for _, chart := range charts {
			chart.setData(visible, from, to, gap)
		}
		showMarks()
	}
	selectPoint := func(point *monitor.LoadPoint) {
		selected = point
		showMarks()
		showDetails()
	}
	follow.OnTapped = func() { selectPoint(nil) }

	for _, chart := range charts {
		chart.onPick = func(source *loadChart, at time.Time, tolerance time.Duration) {
			if i := peakNear(visible, at, tolerance, source.series[0].value); i >= 0 {
				point := visible[i]
				selectPoint(&point)
			}
		}
		chart.onHover = func(at time.Time, inside bool) {
			var point *monitor.LoadPoint
			if i := nearestPoint(visible, at); inside && i >= 0 {
				point = &visible[i]
			}
			if (point == nil) != (hovered == nil) || (point != nil && !point.At.Equal(hovered.At)) {
				hovered = point
				showMarks()
			}
		}
	}

	peak := func(label string, value func(monitor.LoadPoint) float64) *widget.Button {
		return widget.NewButton(label, func() {
			best := -1
			for i := range visible {
				if value(visible[i]) >= 0 && (best < 0 || value(visible[i]) > value(visible[best])) {
					best = i
				}
			}
			if best >= 0 {
				point := visible[best]
				selectPoint(&point)
			}
		})
	}
	titles := make([]string, len(loadRanges))
	for i, r := range loadRanges {
		titles[i] = r.title
	}
	ranges := widget.NewSelect(titles, nil)
	ranges.SetSelectedIndex(1)
	ranges.OnChanged = func(string) {
		if i := ranges.SelectedIndex(); i >= 0 {
			span = loadRanges[i].span
			showCharts()
		}
	}

	u.onUpdate(func(monitor.View) {
		latest := u.m.LoadHistory()
		if len(latest) == len(history) && (len(latest) == 0 || latest[len(latest)-1].At.Equal(history[len(history)-1].At)) {
			return
		}
		history = latest
		hovered = nil
		showCharts()
		showDetails()
	})

	toolbar := container.NewBorder(nil, nil,
		container.NewHBox(widget.NewLabel("Период:"), ranges, peak("Пик процессора", cpuValue), peak("Пик памяти", memValue), peak("Пик диска", diskValue), follow),
		nil, moment)
	hint := caption("Щелчок по графику выбирает ближайший пик. CPU и память Windows считает по процессам; доля CPU пользователя внутри rphost (CPU*) — оценка. Действия 1С берутся из последнего опроса RAC и техжурнала. Хранятся последние 4320 измерений (6 часов при шаге 5 секунд), до закрытия программы.")
	hint.Truncation = fyne.TextTruncateEllipsis

	explanationScroll := container.NewVScroll(explanation)
	left := card(container.NewBorder(caption("ВЫВОД"), nil, nil, nil, explanationScroll))
	middle := card(container.NewBorder(programsTitle, nil, nil, nil, programs.Table))
	detailsScroll := container.NewVScroll(actionDetails)
	detailsScroll.SetMinSize(fyne.NewSize(0, 54))
	right := card(container.NewBorder(caption("ДЕЙСТВИЯ 1С — СЕАНСЫ, РАБОТАВШИЕ В ЭТОТ МОМЕНТ"), detailsScroll, nil, nil, actions.Table))
	tables := container.NewHSplit(middle, right)
	tables.SetOffset(0.44)
	details := container.NewHSplit(left, tables)
	details.SetOffset(0.3)

	body := container.NewVSplit(card(container.NewGridWithRows(3, cpu, memory, disk)), details)
	body.SetOffset(0.6)
	showCharts()
	showDetails()
	return container.NewBorder(container.NewVBox(toolbar, hint), nil, nil, nil, body)
}
