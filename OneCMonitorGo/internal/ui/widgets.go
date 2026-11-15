package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// card — панель со скруглённой рамкой.
func card(content fyne.CanvasObject) fyne.CanvasObject {
	background := canvas.NewRectangle(colCard)
	background.StrokeColor, background.StrokeWidth, background.CornerRadius = colBorder, 1, 8
	return container.NewStack(background, container.NewPadded(content))
}

func boldLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

// heading — заголовок раздела внутри панели.
func heading(text string) *widget.Label {
	label := boldLabel(text)
	label.SizeName = theme.SizeNameSubHeadingText
	return label
}

// caption — мелкая приглушённая подпись.
func caption(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.SizeName, label.Importance = theme.SizeNameCaptionText, widget.LowImportance
	return label
}

// note — приглушённый пояснительный текст с переносом строк.
func note(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping, label.Importance = fyne.TextWrapWord, widget.LowImportance
	return label
}

func wrapLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

func setText(label *widget.Label, text string) {
	if label.Text != text {
		label.SetText(text)
	}
}

func setImportance(label *widget.Label, importance widget.Importance) {
	if label.Importance != importance {
		label.Importance = importance
		label.Refresh()
	}
}

// statCard — плитка показателя: подпись, крупное значение и пояснение.
func statCard(title, hint string) (fyne.CanvasObject, *widget.Label, *widget.Label) {
	value := boldLabel("—")
	value.SizeName = theme.SizeNameHeadingText
	sub := caption(hint)
	sub.Truncation = fyne.TextTruncateEllipsis
	return card(container.NewVBox(caption(title), value, sub)), value, sub
}

type column struct {
	title string
	width float32
}

type cellValue struct {
	text       string
	importance widget.Importance
	bold       bool
}

// dataTable — таблица с заголовком и подсветкой выбранной строки. В дерево интерфейса
// помещается поле Table (сам виджет), а не эта обёртка.
type dataTable struct {
	*widget.Table
	selectedRow int
	onSelect    func(row int)
}

func newDataTable(columns []column, rows func() int, cell func(row, col int) cellValue) *dataTable {
	t := &dataTable{selectedRow: -1}
	t.Table = widget.NewTable(
		func() (int, int) { return rows(), len(columns) },
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.Truncation = fyne.TextTruncateEllipsis
			return container.NewStack(canvas.NewRectangle(color.Transparent), label)
		},
		func(id widget.TableCellID, object fyne.CanvasObject) {
			objects := object.(*fyne.Container).Objects
			background, label := objects[0].(*canvas.Rectangle), objects[1].(*widget.Label)
			var fill color.Color = color.Transparent
			if id.Row == t.selectedRow {
				fill = colRowMark
			}
			if background.FillColor != fill {
				background.FillColor = fill
				background.Refresh()
			}
			if id.Row >= rows() {
				return
			}
			value := cell(id.Row, id.Col)
			if label.Text != value.text || label.Importance != value.importance || label.TextStyle.Bold != value.bold {
				label.Text, label.Importance, label.TextStyle = value.text, value.importance, fyne.TextStyle{Bold: value.bold}
				label.Refresh()
			}
		})
	t.ShowHeaderRow = true
	t.CreateHeader = func() fyne.CanvasObject {
		label := boldLabel("")
		label.Truncation = fyne.TextTruncateEllipsis
		return label
	}
	t.UpdateHeader = func(id widget.TableCellID, object fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < len(columns) {
			setText(object.(*widget.Label), columns[id.Col].title)
		}
	}
	for i, c := range columns {
		t.SetColumnWidth(i, c.width)
	}
	t.OnSelected = func(id widget.TableCellID) {
		if id.Row == t.selectedRow {
			return
		}
		t.selectedRow = id.Row
		t.Refresh()
		if t.onSelect != nil {
			t.onSelect(id.Row)
		}
	}
	return t
}

// markRow подсвечивает строку без вызова обработчика выбора (-1 — снять выделение).
func (t *dataTable) markRow(row int) {
	if t.selectedRow == row {
		return
	}
	t.selectedRow = row
	t.UnselectAll()
}
