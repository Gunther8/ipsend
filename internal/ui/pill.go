package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// pill：胶囊标签（状态）、IP 小卡片（可点击复制）、幽灵小字按钮。
// 文字直接用 canvas.Text 绘制，不带 Label 的内边距，便于精确控制高度和垂直居中。

type pillKind int

const (
	pillNeutral pillKind = iota // 浅底 + 细边框
	pillAccent                  // 蓝色描边 + 浅蓝底
	pillSuccess
	pillWarning
	pillDanger
	pillGhost // 无底无框的灰色小字（可点击）
)

type pill struct {
	widget.BaseWidget
	text  string
	icon  fyne.Resource
	kind  pillKind
	big   bool // 正文字号（IP 卡片）；默认小字号
	onTap func()

	bg    *themedRect
	txt   *canvas.Text
	iconW *widget.Icon
}

func newPill(text string, kind pillKind, icon fyne.Resource, onTap func()) *pill {
	p := &pill{text: text, kind: kind, icon: icon, onTap: onTap}
	p.ExtendBaseWidget(p)
	return p
}

func (p *pill) set(text string, kind pillKind) {
	p.text, p.kind = text, kind
	p.Refresh()
}

func (p *pill) Tapped(*fyne.PointEvent) {
	if p.onTap != nil {
		p.onTap()
	}
}

func (p *pill) Cursor() desktop.Cursor {
	if p.onTap != nil {
		return desktop.PointerCursor
	}
	return desktop.DefaultCursor
}

func (p *pill) style() (bg, stroke, fg fyne.ThemeColorName) {
	switch p.kind {
	case pillAccent:
		return colAccentBg, theme.ColorNamePrimary, theme.ColorNamePrimary
	case pillSuccess:
		return colSuccessBg, "", theme.ColorNameSuccess
	case pillWarning:
		return colWarningBg, "", theme.ColorNameWarning
	case pillDanger:
		return colDangerBg, "", theme.ColorNameError
	case pillGhost:
		return "", "", theme.ColorNameDisabled
	}
	return colNeutralBg, colChipLine, theme.ColorNameForeground
}

func (p *pill) CreateRenderer() fyne.WidgetRenderer {
	p.bg = newThemedRect(colNeutralBg, 999)
	p.txt = canvas.NewText("", color.White)
	p.iconW = widget.NewIcon(nil)
	row := container.New(layout.NewCustomPaddedHBoxLayout(5), container.NewCenter(p.txt))
	if p.icon != nil {
		row.Add(container.NewCenter(sized(p.iconW, 14)))
	}
	vpad, hpad := float32(4), float32(10)
	if p.big {
		vpad = 6
	}
	content := container.New(layout.NewCustomPaddedLayout(vpad, vpad, hpad, hpad), row)
	r := &pillRenderer{p: p, objs: []fyne.CanvasObject{p.bg, content}, content: content}
	r.Refresh()
	return r
}

type pillRenderer struct {
	p       *pill
	objs    []fyne.CanvasObject
	content fyne.CanvasObject
}

func (r *pillRenderer) Layout(s fyne.Size) {
	r.p.bg.Resize(s)
	r.content.Resize(s)
}
func (r *pillRenderer) MinSize() fyne.Size           { return r.content.MinSize() }
func (r *pillRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *pillRenderer) Destroy()                     {}
func (r *pillRenderer) Refresh() {
	bg, stroke, fg := r.p.style()
	if bg == "" {
		r.p.bg.Hide()
	} else {
		r.p.bg.Show()
		r.p.bg.fill, r.p.bg.stroke = bg, stroke
		r.p.bg.Refresh()
	}
	r.p.txt.Text = r.p.text
	r.p.txt.Color = theme.Color(fg)
	if r.p.big {
		r.p.txt.TextSize = theme.TextSize()
	} else {
		r.p.txt.TextSize = theme.CaptionTextSize()
	}
	r.p.txt.Refresh()
	if r.p.icon != nil {
		name := theme.ColorNamePlaceHolder
		if r.p.kind == pillAccent {
			name = theme.ColorNamePrimary
		}
		r.p.iconW.SetResource(theme.NewColoredResource(r.p.icon, name))
	}
}
