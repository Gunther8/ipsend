// Package assets 嵌入字体与图标。
//
// 字体：Noto Sans SC（SIL OFL 1.1，见 OFL.txt），已截取 GB2312 常用字子集。
package assets

import _ "embed"

//go:embed NotoSansSC-Regular.ttf
var FontRegular []byte

//go:embed NotoSansSC-Bold.ttf
var FontBold []byte

//go:embed icon.png
var Icon []byte
