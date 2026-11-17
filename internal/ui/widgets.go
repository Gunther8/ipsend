package ui

import (
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// 本文件是界面用到的几个自绘小组件。颜色都在渲染器的 Refresh 里从主题取，系统切换浅色/深色时会自动跟着变。

// ---------- themedRect：随主题变色的圆角矩形 ----------

type themedRect struct {
	widget.BaseWidget
	fill, stroke fyne.ThemeColorName // stroke 为空表示无边框
	radius       float32
	min          fyne.Size
}

func newThemedRect(fill fyne.ThemeColorName, radius float32) *themedRect {
	r := &themedRect{fill: fill, radius: radius}
	r.ExtendBaseWidget(r)
	return r
}

func (r *themedRect) setFill(c fyne.ThemeColorName) {
	if r.fill != c {
		r.fill = c
		r.Refresh()
	}
}

func (r *themedRect) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(color.Transparent)
	rd := &rectRenderer{w: r, rect: rect}
	rd.Refresh()
	return rd
}

type rectRenderer struct {
	w    *themedRect
	rect *canvas.Rectangle
}

func (r *rectRenderer) Layout(s fyne.Size)           { r.rect.Resize(s) }
func (r *rectRenderer) MinSize() fyne.Size           { return r.w.min }
func (r *rectRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.rect} }
func (r *rectRenderer) Destroy()                     {}
func (r *rectRenderer) Refresh() {
	r.rect.FillColor = theme.Color(r.w.fill)
	r.rect.CornerRadius = r.w.radius
	if r.w.stroke != "" {
		r.rect.StrokeColor = theme.Color(r.w.stroke)
		r.rect.StrokeWidth = 1
	} else {
		r.rect.StrokeWidth = 0
	}
	r.rect.Refresh()
}

// ---------- 卡片 ----------

// caption 是卡片左上角的小标题：图标 + 灰色小字。
func caption(icon fyne.Resource, text string) fyne.CanvasObject {
	l := widget.NewLabel(text)
	l.SizeName = theme.SizeNameCaptionText
	l.Importance = widget.LowImportance
	ic := widget.NewIcon(theme.NewColoredResource(icon, theme.ColorNamePlaceHolder))
	return container.New(layout.NewCustomPaddedHBoxLayout(2), container.NewCenter(sized(ic, 16)), l)
}

// card：白色（深色模式下深灰）圆角卡片，header 可为 nil。
func card(header fyne.CanvasObject, content fyne.CanvasObject) fyne.CanvasObject {
	body := content
	if header != nil {
		body = container.NewBorder(header, nil, nil, nil, content)
	}
	return container.NewStack(newThemedRect(colCard, 10), container.New(layout.NewCustomPaddedLayout(6, 10, 12, 12), body))
}

// sized 给对象一个固定的最小尺寸（方形）。
func sized(o fyne.CanvasObject, s float32) fyne.CanvasObject {
	return container.New(&fixedLayout{fyne.NewSize(s, s)}, o)
}

type fixedLayout struct{ size fyne.Size }

func (f *fixedLayout) Layout(objs []fyne.CanvasObject, s fyne.Size) {
	for _, o := range objs {
		o.Resize(s)
		o.Move(fyne.NewPos(0, 0))
	}
}
func (f *fixedLayout) MinSize([]fyne.CanvasObject) fyne.Size { return f.size }

// minHeight 让内容至少有 h 的高度。
func minHeight(o fyne.CanvasObject, h float32) fyne.CanvasObject {
	spacer := canvas.NewRectangle(color.Transparent)
	spacer.SetMinSize(fyne.NewSize(0, h))
	return container.NewStack(spacer, o)
}

// ---------- thinBar：细进度条 ----------

type thinBar struct {
	widget.BaseWidget
	value float64
	fill  fyne.ThemeColorName
}

func newThinBar(fill fyne.ThemeColorName) *thinBar {
	b := &thinBar{fill: fill}
	b.ExtendBaseWidget(b)
	return b
}

func (b *thinBar) SetValue(v float64) {
	v = math.Max(0, math.Min(1, v))
	if v != b.value {
		b.value = v
		b.Refresh()
	}
}

func (b *thinBar) setFill(c fyne.ThemeColorName) {
	if b.fill != c {
		b.fill = c
		b.Refresh()
	}
}

func (b *thinBar) CreateRenderer() fyne.WidgetRenderer {
	r := &barRenderer{b: b, track: canvas.NewRectangle(color.Transparent), bar: canvas.NewRectangle(color.Transparent)}
	r.Refresh()
	return r
}

type barRenderer struct {
	b          *thinBar
	track, bar *canvas.Rectangle
	size       fyne.Size
}

func (r *barRenderer) Layout(s fyne.Size) {
	r.size = s
	r.track.Resize(s)
	r.bar.Resize(fyne.NewSize(float32(float64(s.Width)*r.b.value), s.Height))
}
func (r *barRenderer) MinSize() fyne.Size           { return fyne.NewSize(20, 6) }
func (r *barRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.track, r.bar} }
func (r *barRenderer) Destroy()                     {}
func (r *barRenderer) Refresh() {
	r.track.FillColor = theme.Color(colBarTrack)
	r.track.CornerRadius = 3
	r.bar.FillColor = theme.Color(r.b.fill)
	r.bar.CornerRadius = 3
	r.Layout(r.size)
	r.track.Refresh()
	r.bar.Refresh()
}

// ---------- accentRow：左侧带色条的行（接收任务） ----------

func accentRow(bar *themedRect, content fyne.CanvasObject) fyne.CanvasObject {
	bar.min = fyne.NewSize(3, 0)
	bg := newThemedRect(colField, 6)
	return container.NewStack(bg, container.NewBorder(nil, nil, bar, nil,
		container.New(layout.NewCustomPaddedLayout(2, 6, 10, 8), content)))
}

// mutedLabel 灰色小字。
func mutedLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.SizeName = theme.SizeNameCaptionText
	l.Importance = widget.LowImportance
	return l
}
