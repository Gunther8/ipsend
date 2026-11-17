//go:build ignore

// 生成应用图标：go run assets/gen_icon.go
// 输出 assets/icon.png（256x256）：蓝色圆角方块 + 白色向右的箭头。
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	const n = 256
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	bg := color.RGBA{0x1f, 0x6f, 0xeb, 0xff}
	fg := color.RGBA{0xff, 0xff, 0xff, 0xff}
	r := 52.0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if a := roundRectCoverage(float64(x)+0.5, float64(y)+0.5, n, r); a > 0 {
				img.Set(x, y, blend(color.RGBA{}, bg, a))
			}
		}
	}
	// 箭头：杆 + 三角形箭头（超采样抗锯齿）
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			cov := 0.0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px := float64(x) + (float64(sx)+0.5)/4
					py := float64(y) + (float64(sy)+0.5)/4
					if inArrow(px, py) {
						cov += 1.0 / 16
					}
				}
			}
			if cov > 0 {
				base := img.RGBAAt(x, y)
				img.Set(x, y, blend(base, fg, cov))
			}
		}
	}
	f, err := os.Create("assets/icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	png.Encode(f, img)
}

func inArrow(x, y float64) bool {
	// 杆
	if x >= 56 && x <= 150 && y >= 108 && y <= 148 {
		return true
	}
	// 三角形 (140,64) (204,128) (140,192)
	if x >= 140 && x <= 204 {
		half := 64 - (x - 140)
		return math.Abs(y-128) <= half
	}
	return false
}

func roundRectCoverage(x, y float64, n int, r float64) float64 {
	cx := math.Max(r, math.Min(float64(n)-r, x))
	cy := math.Max(r, math.Min(float64(n)-r, y))
	d := math.Hypot(x-cx, y-cy)
	return math.Max(0, math.Min(1, r-d+0.5))
}

func blend(dst, src color.RGBA, a float64) color.RGBA {
	if dst.A == 0 {
		return color.RGBA{src.R, src.G, src.B, uint8(float64(src.A) * a)}
	}
	mix := func(d, s uint8) uint8 { return uint8(float64(d)*(1-a) + float64(s)*a) }
	return color.RGBA{mix(dst.R, src.R), mix(dst.G, src.G), mix(dst.B, src.B), dst.A}
}
