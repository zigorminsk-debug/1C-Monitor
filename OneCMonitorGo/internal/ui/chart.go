package ui

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/image/vector"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

// Поля графика в единицах Fyne: подписи оси Y слева, времени — снизу, заголовок сверху.
const (
	chartLeft   float32 = 46
	chartRight  float32 = 10
	chartTop    float32 = 24
	chartBottom float32 = 18
	chartTicks          = 5
)

// chartSeries — линия графика. value возвращает процент (0–100) либо отрицательное число, если данных нет.
type chartSeries struct {
	name  string
	color color.NRGBA
	value func(p monitor.LoadPoint) float64
}

// loadChart — график одного ресурса во времени. Шкала Y всегда 0–100 %. Щелчок сообщает время
// под указателем, движение мыши — время для подсказки; выбор точки делает вкладка.
type loadChart struct {
	widget.BaseWidget

	series  []chartSeries
	summary func(p monitor.LoadPoint) string

	points   []monitor.LoadPoint // видимый диапазон, от старых к новым
	from, to time.Time
	gap      time.Duration // разрыв между измерениями, после которого линия прерывается

	selected, hovered *monitor.LoadPoint

	onPick  func(chart *loadChart, at time.Time, tolerance time.Duration)
	onHover func(at time.Time, inside bool)

	raster               *canvas.Raster
	title, info          *canvas.Text
	legend               []*canvas.Text
	yLabels              [3]*canvas.Text
	xLabels              [chartTicks]*canvas.Text
	hoverLine, pickLine  *canvas.Line
	hoverDots, pickDots  []*canvas.Circle
	lastSize             fyne.Size
	lastFrom, lastTo     time.Time
	lastCount, lastWidth int
}

func newLoadChart(title string, summary func(p monitor.LoadPoint) string, series ...chartSeries) *loadChart {
	c := &loadChart{series: series, summary: summary}
	text := func(value string, col color.Color, size float32, bold bool) *canvas.Text {
		t := canvas.NewText(value, col)
		t.TextSize, t.TextStyle = size, fyne.TextStyle{Bold: bold}
		return t
	}
	c.title = text(title, colText, 13, true)
	c.info = text("", colMuted, 12, false)
	c.info.Alignment = fyne.TextAlignTrailing
	for _, s := range series {
		c.legend = append(c.legend, text("▬ "+s.name, s.color, 12, false))
		c.hoverDots = append(c.hoverDots, newDot(s.color))
		c.pickDots = append(c.pickDots, newDot(s.color))
	}
	for i, value := range []string{"100 %", "50 %", "0"} {
		c.yLabels[i] = text(value, colMuted, 11, false)
		c.yLabels[i].Alignment = fyne.TextAlignTrailing
	}
	for i := range c.xLabels {
		c.xLabels[i] = text("", colMuted, 11, false)
		c.xLabels[i].Alignment = fyne.TextAlignCenter
	}
	c.hoverLine, c.pickLine = canvas.NewLine(colMuted), canvas.NewLine(colWarning)
	c.hoverLine.StrokeWidth, c.pickLine.StrokeWidth = 1, 2
	c.hoverLine.Hide()
	c.pickLine.Hide()
	c.raster = canvas.NewRaster(c.draw)
	c.ExtendBaseWidget(c)
	return c
}

func newDot(col color.Color) *canvas.Circle {
	dot := canvas.NewCircle(col)
	dot.StrokeColor, dot.StrokeWidth = colText, 1.5
	dot.Resize(fyne.NewSize(9, 9))
	dot.Hide()
	return dot
}

func (c *loadChart) MinSize() fyne.Size { return fyne.NewSize(320, 58) }

func (c *loadChart) Cursor() desktop.Cursor { return desktop.CrosshairCursor }

func (c *loadChart) CreateRenderer() fyne.WidgetRenderer {
	objects := []fyne.CanvasObject{c.raster, c.title, c.info}
	for _, t := range c.legend {
		objects = append(objects, t)
	}
	for _, t := range c.yLabels {
		objects = append(objects, t)
	}
	for _, t := range c.xLabels {
		objects = append(objects, t)
	}
	objects = append(objects, c.hoverLine, c.pickLine)
	for i := range c.series {
		objects = append(objects, c.hoverDots[i], c.pickDots[i])
	}
	return &chartRenderer{chart: c, objects: objects}
}

type chartRenderer struct {
	chart   *loadChart
	objects []fyne.CanvasObject
}

func (r *chartRenderer) Destroy()                     {}
func (r *chartRenderer) MinSize() fyne.Size           { return r.chart.MinSize() }
func (r *chartRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *chartRenderer) Layout(size fyne.Size)        { r.chart.layout(size) }
func (r *chartRenderer) Refresh() {
	r.chart.layout(r.chart.Size())
	canvas.Refresh(r.chart)
}

func (c *loadChart) plot() (x, y, w, h float32) {
	size := c.Size()
	return chartLeft, chartTop, fyne.Max(size.Width-chartLeft-chartRight, 1), fyne.Max(size.Height-chartTop-chartBottom, 1)
}

// setData задаёт видимые точки и границы оси времени.
func (c *loadChart) setData(points []monitor.LoadPoint, from, to time.Time, gap time.Duration) {
	c.points, c.from, c.to, c.gap = points, from, to, gap
	c.Refresh()
}

// setMarks задаёт выбранную точку и точку под указателем (nil — нет).
func (c *loadChart) setMarks(selected, hovered *monitor.LoadPoint) {
	c.selected, c.hovered = selected, hovered
	c.layoutMarks()
}

func (c *loadChart) timeX(at time.Time) (float32, bool) {
	span := c.to.Sub(c.from)
	if span <= 0 || at.Before(c.from) || at.After(c.to) {
		return 0, false
	}
	x, _, w, _ := c.plot()
	return x + float32(float64(at.Sub(c.from))/float64(span))*w, true
}

func (c *loadChart) xTime(position float32) (time.Time, bool) {
	x, _, w, _ := c.plot()
	span := c.to.Sub(c.from)
	if span <= 0 || position < x-4 || position > x+w+4 {
		return time.Time{}, false
	}
	ratio := math.Max(0, math.Min(1, float64((position-x)/w)))
	return c.from.Add(time.Duration(ratio * float64(span))), true
}

func (c *loadChart) valueY(value float64) float32 {
	_, y, _, h := c.plot()
	return y + h*float32(1-math.Max(0, math.Min(100, value))/100)
}

func (c *loadChart) layout(size fyne.Size) {
	x, y, w, h := c.plot()
	changed := size != c.lastSize || !c.from.Equal(c.lastFrom) || !c.to.Equal(c.lastTo) || len(c.points) != c.lastCount
	c.lastSize, c.lastFrom, c.lastTo, c.lastCount = size, c.from, c.to, len(c.points)

	c.raster.Move(fyne.NewPos(x, y))
	c.raster.Resize(fyne.NewSize(w, h))
	if changed {
		c.raster.Refresh()
	}

	left := float32(4)
	place := func(t *canvas.Text) {
		min := t.MinSize()
		t.Move(fyne.NewPos(left, 3))
		t.Resize(min)
		left += min.Width + 14
	}
	place(c.title)
	for _, t := range c.legend {
		place(t)
	}
	c.info.Move(fyne.NewPos(left, 3))
	c.info.Resize(fyne.NewSize(fyne.Max(size.Width-chartRight-left, 0), c.info.MinSize().Height))

	for i, t := range c.yLabels {
		height := t.MinSize().Height
		// Верхняя и нижняя подписи прижаты к границам графика и не заходят на заголовок и ось времени.
		t.Move(fyne.NewPos(0, y+(h-height)*float32(i)/2))
		t.Resize(fyne.NewSize(chartLeft-6, height))
	}
	span := c.to.Sub(c.from)
	format := "15:04:05"
	if span > 2*time.Hour {
		format = "15:04"
	}
	for i, t := range c.xLabels {
		ratio := float32(i) / float32(chartTicks-1)
		text := ""
		if span > 0 {
			text = c.from.Add(time.Duration(float64(span) * float64(ratio))).Format(format)
		}
		if t.Text != text {
			t.Text = text
			t.Refresh()
		}
		// Крайние подписи прижаты к границам графика, чтобы не выходить за него.
		const width = 70
		position := x + w*ratio - width/2
		switch i {
		case 0:
			t.Alignment, position = fyne.TextAlignLeading, x
		case chartTicks - 1:
			t.Alignment, position = fyne.TextAlignTrailing, x+w-width
		}
		t.Move(fyne.NewPos(position, y+h+2))
		t.Resize(fyne.NewSize(width, t.MinSize().Height))
	}
	c.layoutMarks()
}

func (c *loadChart) layoutMarks() {
	_, y, _, h := c.plot()
	mark := func(point *monitor.LoadPoint, line *canvas.Line, dots []*canvas.Circle) {
		position, ok := float32(0), false
		if point != nil {
			position, ok = c.timeX(point.At)
		}
		if !ok {
			if line.Visible() {
				line.Hide()
			}
			for _, dot := range dots {
				if dot.Visible() {
					dot.Hide()
				}
			}
			return
		}
		line.Position1, line.Position2 = fyne.NewPos(position, y), fyne.NewPos(position, y+h)
		line.Show()
		line.Refresh()
		for i, s := range c.series {
			value := s.value(*point)
			if value < 0 {
				dots[i].Hide()
				continue
			}
			dots[i].Move(fyne.NewPos(position-4.5, c.valueY(value)-4.5))
			dots[i].Show()
			dots[i].Refresh()
		}
	}
	mark(c.hovered, c.hoverLine, c.hoverDots)
	mark(c.selected, c.pickLine, c.pickDots)

	// Справа от заголовка — значения под указателем, иначе выбранной точки, иначе последнего измерения.
	shown := c.hovered
	if shown == nil {
		shown = c.selected
	}
	if shown == nil && len(c.points) > 0 {
		shown = &c.points[len(c.points)-1]
	}
	text := "нет измерений"
	if shown != nil {
		text = shown.At.Format("15:04:05") + " · " + c.summary(*shown)
	}
	if c.info.Text != text {
		c.info.Text = text
		c.info.Refresh()
	}
}

func (c *loadChart) Tapped(event *fyne.PointEvent) {
	at, ok := c.xTime(event.Position.X)
	if !ok || c.onPick == nil {
		return
	}
	_, _, w, _ := c.plot()
	// Щелчок рядом с пиком выбирает сам пик: допуск — 7 единиц экрана в каждую сторону.
	c.onPick(c, at, time.Duration(float64(c.to.Sub(c.from))*7/float64(w)))
}

func (c *loadChart) MouseIn(event *desktop.MouseEvent) { c.MouseMoved(event) }

func (c *loadChart) MouseMoved(event *desktop.MouseEvent) {
	if c.onHover != nil {
		at, ok := c.xTime(event.Position.X)
		c.onHover(at, ok)
	}
}

func (c *loadChart) MouseOut() {
	if c.onHover != nil {
		c.onHover(time.Time{}, false)
	}
}

// nearestPoint возвращает индекс измерения, ближайшего по времени (-1 — измерений нет).
func nearestPoint(points []monitor.LoadPoint, at time.Time) int {
	if len(points) == 0 {
		return -1
	}
	i := sort.Search(len(points), func(i int) bool { return !points[i].At.Before(at) })
	if i == len(points) {
		return i - 1
	}
	if i > 0 && at.Sub(points[i-1].At) < points[i].At.Sub(at) {
		return i - 1
	}
	return i
}

// peakNear возвращает индекс измерения с наибольшим значением в пределах допуска от указанного
// времени; если в допуск ничего не попало — ближайшее по времени.
func peakNear(points []monitor.LoadPoint, at time.Time, tolerance time.Duration, value func(monitor.LoadPoint) float64) int {
	best := -1
	first := sort.Search(len(points), func(i int) bool { return !points[i].At.Before(at.Add(-tolerance)) })
	for i := first; i < len(points) && !points[i].At.After(at.Add(tolerance)); i++ {
		if best < 0 || value(points[i]) > value(points[best]) {
			best = i
		}
	}
	if best < 0 {
		return nearestPoint(points, at)
	}
	return best
}

type chartVertex struct{ x, y float32 }

// chartPaths строит ломаные для линии графика в пикселях. Если измерений больше, чем столбцов
// по два пикселя, в столбце остаётся наибольшее значение: пики при сжатии не теряются.
func chartPaths(points []monitor.LoadPoint, from, to time.Time, gap time.Duration, value func(monitor.LoadPoint) float64, width, height int) [][]chartVertex {
	span := float64(to.Sub(from))
	if span <= 0 || width < 2 || height < 2 {
		return nil
	}
	columns := width / 2
	bucketed := len(points) > columns
	var paths [][]chartVertex
	var current []chartVertex
	flush := func() {
		if len(current) > 0 {
			paths = append(paths, current)
			current = nil
		}
	}
	var previous time.Time
	lastColumn := -1
	for _, point := range points {
		v := value(point)
		if point.At.Before(from) || point.At.After(to) {
			continue
		}
		if v < 0 || (gap > 0 && !previous.IsZero() && point.At.Sub(previous) > gap) {
			flush()
			lastColumn = -1
		}
		previous = point.At
		if v < 0 {
			continue
		}
		ratio := float64(point.At.Sub(from)) / span
		x := float32(ratio * float64(width-1))
		y := float32((1 - math.Min(100, v)/100) * float64(height-1))
		if bucketed {
			column := int(ratio * float64(columns-1))
			if column == lastColumn && len(current) > 0 {
				if last := &current[len(current)-1]; y < last.y {
					last.y = y
				}
				continue
			}
			lastColumn = column
			x = float32(column*2 + 1)
		}
		current = append(current, chartVertex{x, y})
	}
	flush()
	return paths
}

// draw рисует сетку и линии. Вызывается Fyne с размером области графика в пикселях.
func (c *loadChart) draw(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(colGrid), image.Point{}, draw.Src)
	if width < 2 || height < 2 {
		return img
	}
	_, _, w, _ := c.plot()
	scale := float32(width) / w
	line := image.NewUniform(colBorder)
	for i := 0; i <= 4; i++ {
		y := i * (height - 1) / 4
		draw.Draw(img, image.Rect(0, y, width, y+1), line, image.Point{}, draw.Src)
	}
	for i := 0; i < chartTicks; i++ {
		x := i * (width - 1) / (chartTicks - 1)
		draw.Draw(img, image.Rect(x, 0, x+1, height), line, image.Point{}, draw.Src)
	}

	for index, s := range c.series {
		paths := chartPaths(c.points, c.from, c.to, c.gap, s.value, width, height)
		if len(paths) == 0 {
			continue
		}
		// Заливка под первой линией показывает общий уровень; остальные рисуются только линией.
		if index == 0 {
			fill := vector.NewRasterizer(width, height)
			for _, path := range paths {
				fill.MoveTo(path[0].x, float32(height))
				for _, v := range path {
					fill.LineTo(v.x, v.y)
				}
				fill.LineTo(path[len(path)-1].x+1, float32(height))
				fill.ClosePath()
			}
			shade := s.color
			shade.A = 0x38
			fill.Draw(img, img.Bounds(), image.NewUniform(shade), image.Point{})
		}
		stroke := vector.NewRasterizer(width, height)
		half := fyne.Max(0.9*scale, 0.75)
		for _, path := range paths {
			if len(path) == 1 {
				addDisc(stroke, path[0], half*2)
				continue
			}
			for i := 1; i < len(path); i++ {
				addSegment(stroke, path[i-1], path[i], half)
			}
		}
		stroke.Draw(img, img.Bounds(), image.NewUniform(s.color), image.Point{})
	}
	return img
}

// addSegment добавляет отрезок заданной полутолщины. Все отрезки обходятся в одну сторону,
// поэтому перекрытия в местах стыков не образуют просветов.
func addSegment(r *vector.Rasterizer, a, b chartVertex, half float32) {
	dx, dy := b.x-a.x, b.y-a.y
	length := float32(math.Hypot(float64(dx), float64(dy)))
	if length == 0 {
		addDisc(r, a, half)
		return
	}
	nx, ny := -dy/length*half, dx/length*half
	// Отрезок слегка продлён в обе стороны, чтобы закрыть стык с соседним.
	ex, ey := dx/length*half*0.5, dy/length*half*0.5
	r.MoveTo(a.x-ex+nx, a.y-ey+ny)
	r.LineTo(b.x+ex+nx, b.y+ey+ny)
	r.LineTo(b.x+ex-nx, b.y+ey-ny)
	r.LineTo(a.x-ex-nx, a.y-ey-ny)
	r.ClosePath()
}

func addDisc(r *vector.Rasterizer, center chartVertex, radius float32) {
	// Обход в ту же сторону, что и у отрезков.
	const sides = 10
	for i := 0; i < sides; i++ {
		angle := -2 * math.Pi * float64(i) / sides
		x, y := center.x+radius*float32(math.Sin(angle)), center.y-radius*float32(math.Cos(angle))
		if i == 0 {
			r.MoveTo(x, y)
		} else {
			r.LineTo(x, y)
		}
	}
	r.ClosePath()
}
