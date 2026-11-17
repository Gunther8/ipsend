package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"ipsend/assets"
)

var (
	fontRegular = fyne.NewStaticResource("NotoSansSC-Regular.ttf", assets.FontRegular)
	fontBold    = fyne.NewStaticResource("NotoSansSC-Bold.ttf", assets.FontBold)
	iconRes     = fyne.NewStaticResource("icon.png", assets.Icon)
)

// 自定义颜色名（供自绘组件使用，随浅色/深色切换）。
const (
	colCard      fyne.ThemeColorName = "ipsend.card"
	colField     fyne.ThemeColorName = "ipsend.field"
	colDash      fyne.ThemeColorName = "ipsend.dash"
	colBarTrack  fyne.ThemeColorName = "ipsend.barTrack"
	colAccentBg  fyne.ThemeColorName = "ipsend.accentBg"
	colSuccessBg fyne.ThemeColorName = "ipsend.successBg"
	colWarningBg fyne.ThemeColorName = "ipsend.warningBg"
	colDangerBg  fyne.ThemeColorName = "ipsend.dangerBg"
	colNeutralBg fyne.ThemeColorName = "ipsend.neutralBg"
	colChipLine  fyne.ThemeColorName = "ipsend.chipLine"
)

func hex(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

// 浅色 / 深色两套配色，主色取图标的蓝色。
var palette = map[fyne.ThemeColorName][2]color.Color{
	theme.ColorNamePrimary:           {hex(0x1F6FEB), hex(0x4C8DF6)},
	theme.ColorNameBackground:        {hex(0xF2F3F5), hex(0x141619)},
	theme.ColorNameButton:            {hex(0xFFFFFF), hex(0x2A2E34)},
	theme.ColorNameInputBackground:   {hex(0xF7F8FA), hex(0x262A2F)},
	theme.ColorNameInputBorder:       {hex(0xDADDE2), hex(0x363B42)},
	theme.ColorNameSeparator:         {hex(0xE3E5E8), hex(0x2C3036)},
	theme.ColorNameMenuBackground:    {hex(0xFFFFFF), hex(0x22262B)},
	theme.ColorNameOverlayBackground: {hex(0xFFFFFF), hex(0x1E2125)},
	theme.ColorNameSuccess:           {hex(0x1E8E3E), hex(0x5CC27A)},
	theme.ColorNameWarning:           {hex(0xB26A00), hex(0xF0B44C)},
	theme.ColorNameError:             {hex(0xC62828), hex(0xF07070)},
	theme.ColorNamePlaceHolder:       {hex(0x8A9099), hex(0x7C838D)},
	// Fyne 的灰色小字（LowImportance）用的是 Disabled 色，默认在深色下太暗，调到中灰
	theme.ColorNameDisabled: {hex(0x868C95), hex(0x8A919B)},

	colCard:      {hex(0xFFFFFF), hex(0x1E2125)},
	colField:     {hex(0xF7F8FA), hex(0x262A2F)},
	colDash:      {hex(0xC4C8CE), hex(0x434951)},
	colBarTrack:  {hex(0xE6E8EB), hex(0x30353B)},
	colAccentBg:  {hex(0xE8F0FE), hex(0x1B2A44)},
	colSuccessBg: {hex(0xE6F4EA), hex(0x1B3A26)},
	colWarningBg: {hex(0xFDF1DC), hex(0x3D2E12)},
	colDangerBg:  {hex(0xFCE8E8), hex(0x3F1D1D)},
	colNeutralBg: {hex(0xF7F8FA), hex(0x262A2F)},
	colChipLine:  {hex(0xDADDE2), hex(0x3A3F46)},
}

// cnTheme：内嵌中文字体 + ipsend 配色 + 稍大的圆角，其余沿用 Fyne 默认主题。
type cnTheme struct{ fyne.Theme }

func newTheme() fyne.Theme { return cnTheme{theme.DefaultTheme()} }

func (t cnTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if c, ok := palette[n]; ok {
		if v == theme.VariantDark {
			return c[1]
		}
		return c[0]
	}
	return t.Theme.Color(n, v)
}

func (t cnTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 8
	case theme.SizeNameInputBorder:
		return 1
	}
	return t.Theme.Size(n)
}

func (t cnTheme) Font(s fyne.TextStyle) fyne.Resource {
	switch {
	case s.Monospace || s.Symbol:
		return t.Theme.Font(s)
	case s.Bold:
		return fontBold
	default:
		return fontRegular
	}
}
