package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// dashedBox：虚线框（拖放区）。用矢量线段绘制（软件渲染下位图 raster 颜色显示不正确），
// 尺寸变化时重新生成线段，颜色在 Refresh 里从主题取。
func dashedBox(content fyne.CanvasObject) fyne.CanvasObject {
	b := &dashBorder{}
	b.ExtendBaseWidget(b)
	return container.NewStack(b, container.New(layout.NewCustomPaddedLayout(6, 6, 10, 10), content))
}

type dashBorder struct{ widget.BaseWidget }

func (d *dashBorder) CreateRenderer() fyne.WidgetRenderer { return &dashRenderer{} }

type dashRenderer struct {
	lines []fyne.CanvasObject
	size  fyne.Size
}

const (
	dashLen   = 7
	dashGap   = 5
	dashWidth = 1.5
	dashInset = 1
)

func (r *dashRenderer) Layout(s fyne.Size) {
	if s == r.size && len(r.lines) > 0 {
		return
	}
	r.size = s
	c := theme.Color(colDash)
	r.lines = r.lines[:0]
	add := func(x1, y1, x2, y2 float32) {
		l := canvas.NewLine(c)
		l.StrokeWidth = dashWidth
		l.Position1, l.Position2 = fyne.NewPos(x1, y1), fyne.NewPos(x2, y2)
		r.lines = append(r.lines, l)
	}
	l, t := float32(dashInset), float32(dashInset)
	rgt, btm := s.Width-dashInset, s.Height-dashInset
	for x := l; x < rgt; x += dashLen + dashGap { // 上下边
		e := min(x+dashLen, rgt)
		add(x, t, e, t)
		add(x, btm, e, btm)
	}
	for y := t; y < btm; y += dashLen + dashGap { // 左右边
		e := min(y+dashLen, btm)
		add(l, y, l, e)
		add(rgt, y, rgt, e)
	}
}

func (r *dashRenderer) MinSize() fyne.Size           { return fyne.NewSize(0, 0) }
func (r *dashRenderer) Objects() []fyne.CanvasObject { return r.lines }
func (r *dashRenderer) Destroy()                     {}
func (r *dashRenderer) Refresh() {
	c := theme.Color(colDash)
	for _, o := range r.lines {
		if l, ok := o.(*canvas.Line); ok {
			l.StrokeColor = c
			l.Refresh()
		}
	}
}
