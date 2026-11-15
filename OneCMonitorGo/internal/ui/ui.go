package ui

import (
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

// UI — главное окно программы.
type UI struct {
	app     fyne.App
	win     fyne.Window
	m       *monitor.Monitor
	pending atomic.Bool

	tabs     *container.AppTabs
	view     monitor.View
	updaters []func(view monitor.View)

	selectedPid     int
	selectedSession string

	// Части вкладки «Графики нагрузки», к которым обращаются тесты.
	loadCharts      []*loadChart
	loadMoment      *widget.Label
	loadExplanation *widget.Label
}

// New строит содержимое окна. Вызывается из главной горутины до запуска цикла приложения.
func New(app fyne.App, win fyne.Window, m *monitor.Monitor) *UI {
	u := &UI{app: app, win: win, m: m, view: m.View()}
	u.tabs = container.NewAppTabs(
		container.NewTabItem("Обзор", u.overviewTab()),
		container.NewTabItem("Графики нагрузки", u.loadTab()),
		container.NewTabItem("Пользователи и сеансы", u.sessionsTab()),
		container.NewTabItem("Администрирование", u.adminTab()),
		container.NewTabItem("Настройки", u.settingsTab()),
		container.NewTabItem("Журнал", u.logTab()),
	)
	content := container.NewBorder(u.header(), u.footer(), nil, nil, u.tabs)
	win.SetContent(container.NewPadded(content))
	u.Refresh()
	return u
}

// Attach подписывает окно на изменения монитора. Обработчик возвращается сразу: перерисовка
// выполняется в главной горутине, а несколько уведомлений подряд сливаются в одну перерисовку.
func (u *UI) Attach() {
	u.m.SetOnChange(func() {
		if u.pending.CompareAndSwap(false, true) {
			fyne.Do(func() {
				u.pending.Store(false)
				u.Refresh()
			})
		}
	})
}

// Refresh переносит состояние монитора на экран. Вызывается только из главной горутины.
func (u *UI) Refresh() {
	u.view = u.m.View()
	for _, update := range u.updaters {
		update(u.view)
	}
}

func (u *UI) onUpdate(update func(view monitor.View)) {
	u.updaters = append(u.updaters, update)
}

// asyncButton — кнопка длительной операции: на время выполнения блокируется.
func (u *UI) asyncButton(label string, action func()) *widget.Button {
	var button *widget.Button
	button = widget.NewButton(label, func() {
		button.Disable()
		go func() {
			defer fyne.Do(button.Enable)
			action()
		}()
	})
	return button
}

func (u *UI) link(text, uri string) *widget.Hyperlink {
	link := widget.NewHyperlink(text, nil)
	link.OnTapped = func() { go u.m.OpenURI(uri) }
	return link
}

func levelImportance(level monitor.Level) widget.Importance {
	switch level {
	case monitor.LevelOK:
		return widget.SuccessImportance
	case monitor.LevelBad:
		return widget.DangerImportance
	}
	return widget.WarningImportance
}

func (u *UI) header() fyne.CanvasObject {
	badgeBackground := canvas.NewRectangle(colPrimary)
	badgeBackground.CornerRadius = 10
	badgeBackground.SetMinSize(fyne.NewSize(52, 52))
	badgeText := canvas.NewText("1C", colText)
	badgeText.TextStyle, badgeText.TextSize, badgeText.Alignment = fyne.TextStyle{Bold: true}, 22, fyne.TextAlignCenter
	badge := container.NewVBox(container.NewStack(badgeBackground, container.NewCenter(badgeText)))

	title := boldLabel(u.view.ServerHeaderTitle)
	title.SizeName = theme.SizeNameSubHeadingText
	details := note(u.view.ServerHeaderDetails)
	details.SizeName = theme.SizeNameCaptionText
	status := boldLabel(u.view.Status)
	status.Wrapping = fyne.TextWrapWord

	refresh := u.asyncButton("Обновить", func() { u.m.Refresh(true) })
	refresh.Importance = widget.HighImportance
	export := u.asyncButton("Отчёт HTML", func() { u.m.ExportHTMLReport() })
	running := widget.NewCheck("Мониторинг", func(on bool) { u.m.SetRunning(on) })
	running.SetChecked(u.view.Running)
	buttons := container.NewVBox(container.NewHBox(refresh, export, running))

	u.onUpdate(func(view monitor.View) {
		if u.win != nil && u.win.Title() != view.WindowTitle {
			u.win.SetTitle(view.WindowTitle)
		}
		setText(title, view.ServerHeaderTitle)
		setText(details, view.ServerHeaderDetails)
		setText(status, view.Status)
		setImportance(status, levelImportance(view.StatusLevel))
		text := "Пауза"
		if view.Running {
			text = "Мониторинг"
		}
		if running.Text != text {
			running.Text = text
			running.Refresh()
		}
		if running.Checked != view.Running {
			running.SetChecked(view.Running)
		}
	})
	return container.NewVBox(card(container.NewBorder(nil, status, badge, buttons, container.NewVBox(title, details))))
}

func (u *UI) footer() fyne.CanvasObject {
	measurement := caption(u.view.Footer)
	measurement.Truncation = fyne.TextTruncateEllipsis
	u.onUpdate(func(view monitor.View) { setText(measurement, view.Footer) })
	contacts := container.NewHBox(caption("Разработчик: Zakharevich Igor"),
		u.link("csl.by", "https://csl.by"), u.link("info@csl.by", "mailto:info@csl.by"), u.link("+375 29 337 14 12", "tel:+375293371412"))
	return container.NewBorder(nil, nil, nil, contacts, measurement)
}

func (u *UI) overviewTab() fyne.CanvasObject {
	processesCard, processCount, _ := statCard("RPHOST", "процессов")
	memoryCard, memory, _ := statCard("ПАМЯТЬ", "рабочий набор")
	cpuCard, cpu, _ := statCard("CPU", "суммарно")
	sessionsCard, sessions, breakdown := statCard("СЕАНСЫ", "активные — • спящие —")
	alertsCard, alerts, _ := statCard("ТРЕВОГИ", "требуют внимания")
	stats := container.New(layout.NewGridLayout(5), processesCard, memoryCard, cpuCard, sessionsCard, alertsCard)

	diskUsage := boldLabel("—")
	diskUsage.SizeName = theme.SizeNameHeadingText
	diskQueue, diskIo := widget.NewLabel("очередь —"), widget.NewLabel("чтение — • запись —")
	diskIo.Truncation = fyne.TextTruncateEllipsis
	disk := card(container.NewVBox(caption("НАГРУЗКА НА ДИСК · PhysicalDisk (_Total)"),
		container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(150, 2*diskQueue.MinSize().Height), diskUsage), nil, container.NewVBox(diskQueue, diskIo))))
	hardware, network := wrapLabel(""), wrapLabel("")
	hardware.SizeName = theme.SizeNameCaptionText
	server := card(container.NewVBox(caption("ОБОРУДОВАНИЕ, НАКОПИТЕЛИ И СЕТЬ"), network, hardware))
	// Блок оборудования шире: в нём длинное описание накопителей и томов.
	info := container.New(&ratioLayout{first: 0.34}, disk, server)

	columns := []column{{"Риск", 60}, {"PID", 70}, {"Состояние процесса", 190}, {"CPU", 70}, {"RAM", 95}, {"Сеансы", 70},
		{"Ср. вызов", 90}, {"Ср. БД", 90}, {"Пользователи", 320}, {"Диагноз", 520}}
	table := newDataTable(columns, func() int { return len(u.view.Processes) }, func(row, col int) cellValue {
		p := u.view.Processes[row]
		switch col {
		case 0:
			importance := widget.SuccessImportance
			if p.RiskLevel() >= 2 {
				importance = widget.DangerImportance
			} else if p.RiskLevel() == 1 {
				importance = widget.WarningImportance
			}
			return cellValue{p.RiskText(), importance, true}
		case 1:
			return cellValue{text: itoa(p.Pid)}
		case 2:
			return cellValue{text: p.DrainStatus}
		case 3:
			return cellValue{text: p.CpuText()}
		case 4:
			return cellValue{text: p.MemoryText()}
		case 5:
			return cellValue{text: itoa(p.SessionCount)}
		case 6:
			return cellValue{text: p.AvgCallText()}
		case 7:
			return cellValue{text: p.AvgDbText()}
		case 8:
			return cellValue{text: p.Users}
		}
		return cellValue{text: p.Diagnosis}
	})
	table.onSelect = func(row int) {
		if row >= 0 && row < len(u.view.Processes) {
			u.selectedPid = u.view.Processes[row].Pid
			u.m.SelectProcess(u.selectedPid)
		}
	}
	autoDrain := note("")
	processes := card(container.NewBorder(
		container.NewBorder(nil, nil, heading("Процессы rphost"), caption("Красная оценка означает вероятный источник зависания")),
		autoDrain, nil, nil, table.Table))

	alertList := widget.NewList(
		func() int { return len(u.view.Alerts) },
		func() fyne.CanvasObject {
			// Время и уровень занимают столбцы постоянной ширины, сообщение — остальное место.
			at, severity, message := widget.NewLabel(""), boldLabel(""), widget.NewLabel("")
			at.Importance, message.Truncation = widget.LowImportance, fyne.TextTruncateEllipsis
			height := message.MinSize().Height
			left := container.NewHBox(container.NewGridWrap(fyne.NewSize(150, height), at), container.NewGridWrap(fyne.NewSize(120, height), severity))
			return container.NewBorder(nil, nil, left, nil, message)
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			if id >= len(u.view.Alerts) {
				return
			}
			alert := u.view.Alerts[id]
			objects := object.(*fyne.Container).Objects
			message := objects[0].(*widget.Label)
			left := objects[1].(*fyne.Container).Objects
			setText(left[0].(*fyne.Container).Objects[0].(*widget.Label), alert.TimeText())
			severity := left[1].(*fyne.Container).Objects[0].(*widget.Label)
			setText(severity, alert.Severity)
			importance := widget.WarningImportance
			if alert.Severity != "ВНИМАНИЕ" {
				importance = widget.DangerImportance
			}
			setImportance(severity, importance)
			setText(message, alert.Source+" · "+alert.Message)
		})
	noAlerts := caption("Тревог пока нет")
	lastAlerts := card(container.NewBorder(heading("Последние тревоги"), nil, nil, nil, container.NewStack(alertList, container.NewCenter(noAlerts))))

	split := container.NewVSplit(processes, lastAlerts)
	split.SetOffset(0.68)

	var shownProcesses []monitor.ProcessRow
	var shownAlerts []monitor.AlertRow
	u.onUpdate(func(view monitor.View) {
		setText(processCount, itoa(view.ProcessCount))
		setText(memory, view.TotalMemoryText)
		setText(cpu, view.TotalCpuText)
		setText(sessions, itoa(view.SessionCount))
		setText(breakdown, view.SessionBreakdownText)
		setText(alerts, itoa(view.AlertCount))
		importance := widget.MediumImportance
		if view.AlertCount > 0 {
			importance = widget.DangerImportance
		}
		setImportance(alerts, importance)
		setText(diskUsage, view.DiskUsageText)
		setText(diskQueue, view.DiskQueueText)
		setText(diskIo, view.DiskIoText)
		setText(hardware, view.ServerHardwareText)
		setText(network, view.NetworkLoadText)
		setText(autoDrain, view.AutoDrainStatus)

		if !sameSlice(shownProcesses, view.Processes) {
			shownProcesses = view.Processes
			// Строки пересортировываются по риску, поэтому выделение восстанавливается по PID.
			row := -1
			for i, p := range view.Processes {
				if p.Pid == u.selectedPid {
					row = i
				}
			}
			if row < 0 && u.selectedPid != 0 {
				u.selectedPid = 0
				u.m.SelectProcess(0)
			}
			table.markRow(row)
			table.Refresh()
		}
		if !sameSlice(shownAlerts, view.Alerts) {
			shownAlerts = view.Alerts
			alertList.Refresh()
			if len(view.Alerts) > 0 {
				noAlerts.Hide()
			} else {
				noAlerts.Show()
			}
		}
	})
	return container.NewBorder(container.NewVBox(stats, info), nil, nil, nil, split)
}

// sameSlice сообщает, что срезы указывают на одни и те же данные (монитор публикует новые срезы,
// а не изменяет прежние, поэтому таблицы перерисовываются только при новых данных).
func sameSlice[T any](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}

// ratioLayout делит ширину между двумя объектами в заданной пропорции.
type ratioLayout struct{ first float32 }

func (l *ratioLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) != 2 {
		return
	}
	padding := theme.Padding()
	width := (size.Width - padding) * l.first
	objects[0].Move(fyne.NewPos(0, 0))
	objects[0].Resize(fyne.NewSize(width, size.Height))
	objects[1].Move(fyne.NewPos(width+padding, 0))
	objects[1].Resize(fyne.NewSize(size.Width-width-padding, size.Height))
}

func (l *ratioLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var width, height float32
	for _, object := range objects {
		min := object.MinSize()
		width += min.Width
		height = fyne.Max(height, min.Height)
	}
	return fyne.NewSize(width+theme.Padding(), height)
}
