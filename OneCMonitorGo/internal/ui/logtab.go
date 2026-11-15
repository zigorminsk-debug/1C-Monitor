package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

func (u *UI) logTab() fyne.CanvasObject {
	lines, version := u.m.LogLines()
	monospace := fyne.TextStyle{Monospace: true}

	selected := widget.NewLabelWithStyle("Выберите строку, чтобы увидеть её полностью.", fyne.TextAlignLeading, monospace)
	selected.Wrapping, selected.Selectable = fyne.TextWrapWord, true
	selectedScroll := container.NewVScroll(selected)
	selectedScroll.SetMinSize(fyne.NewSize(0, 76))

	list := widget.NewList(
		func() int { return len(lines) },
		func() fyne.CanvasObject {
			label := widget.NewLabelWithStyle("", fyne.TextAlignLeading, monospace)
			label.Truncation = fyne.TextTruncateEllipsis
			return label
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			if id < len(lines) {
				setText(object.(*widget.Label), lines[id])
			}
		})
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(lines) {
			selected.SetText(lines[id])
		}
	}

	follow := widget.NewCheck("Автопрокрутка", nil)
	follow.SetChecked(true)
	count := caption("")
	copyAll := widget.NewButton("Копировать журнал", func() { u.app.Clipboard().SetContent(u.m.LogText()) })
	openFolder := widget.NewButton("Открыть папку данных", func() { go u.m.OpenURI(u.m.DataDir()) })
	toolbar := container.NewBorder(nil, nil, container.NewHBox(copyAll, openFolder, follow), count)

	show := func() {
		list.UnselectAll()
		list.Refresh()
		setText(count, "строк: "+itoa(len(lines)))
		if follow.Checked && len(lines) > 0 {
			list.ScrollToBottom()
		}
	}
	show()
	u.onUpdate(func(monitor.View) {
		if current := u.m.LogVersion(); current != version {
			lines, version = u.m.LogLines()
			show()
		}
	})
	bottom := card(container.NewBorder(caption("ВЫБРАННАЯ СТРОКА"), nil, nil, nil, selectedScroll))
	return container.NewBorder(toolbar, bottom, nil, nil, card(list))
}
