// 命令 genicon 生成应用图标与菜单栏状态图标。
//
// 图标是纯几何构形，用程序绘制而非位图缩放，
// 保证从 16 像素的菜单栏到 1024 像素的访达预览都锐利。
//
//	go run ./cmd/genicon
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// 品牌色：磷光绿信号，与控制台界面一致。
var (
	signal     = rgb(0x40, 0xe0, 0x8f)
	signalLit  = rgb(0x62, 0xf5, 0xac)
	surfaceTop = rgb(0x16, 0x23, 0x26)
	surfaceBot = rgb(0x05, 0x09, 0x0a)
)

// 菜单栏各状态的颜色，取中间调以便在浅色与深色菜单栏上都看得清。
var trayColors = map[string][3]float64{
	"running": rgb(0x2f, 0xb8, 0x74),
	"warn":    rgb(0xe0, 0x92, 0x20),
	"bad":     rgb(0xdd, 0x4b, 0x3e),
	"off":     rgb(0x8b, 0x8b, 0x8b),
}

// 隧道行首状态点的颜色。
var dotColors = map[string][3]float64{
	"ok":  rgb(0x2f, 0xb8, 0x74),
	"bad": rgb(0xdd, 0x4b, 0x3e),
	"off": rgb(0xa8, 0xa8, 0xa8),
}

// icnsEntries 是 ICNS 容器中各尺寸对应的类型标识（macOS 10.7 起支持内嵌 PNG）。
var icnsEntries = []struct {
	osType string
	px     int
}{
	{"icp4", 16},
	{"icp5", 32},
	{"ic11", 32},   // 16pt @2x
	{"ic12", 64},   // 32pt @2x
	{"ic07", 128},  // 128pt
	{"ic13", 256},  // 128pt @2x
	{"ic08", 256},  // 256pt
	{"ic14", 512},  // 256pt @2x
	{"ic09", 512},  // 512pt
	{"ic10", 1024}, // 512pt @2x
}

func main() {
	buildDir := "build"
	trayDir := filepath.Join("internal", "tray", "assets")

	for _, dir := range []string{buildDir, trayDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			exit(err)
		}
	}

	// 同一尺寸只渲染一次，多个类型共用同一份 PNG 数据。
	cache := map[int][]byte{}
	var chunks []icnsChunk
	for _, e := range icnsEntries {
		data, ok := cache[e.px]
		if !ok {
			img := render(e.px, appIconSample)
			var err error
			if data, err = encodePNG(img); err != nil {
				exit(err)
			}
			cache[e.px] = data
		}
		chunks = append(chunks, icnsChunk{osType: e.osType, data: data})
	}

	icnsPath := filepath.Join(buildDir, "AppIcon.icns")
	if err := os.WriteFile(icnsPath, buildICNS(chunks), 0o644); err != nil {
		exit(err)
	}
	fmt.Printf("应用图标  %d 个尺寸 -> %s\n", len(cache), icnsPath)

	// 预览图方便肉眼确认效果
	preview := render(512, appIconSample)
	if err := writePNG(filepath.Join(buildDir, "icon-preview.png"), preview); err != nil {
		exit(err)
	}

	for state, c := range trayColors {
		col := c
		img := render(44, func(x, y, size float64) ([3]float64, float64) {
			return trayIconSample(x, y, size, col)
		})
		if err := writePNG(filepath.Join(trayDir, "tray-"+state+".png"), img); err != nil {
			exit(err)
		}
	}
	fmt.Printf("菜单栏图标 %d 个状态 -> %s\n", len(trayColors), trayDir)

	// 每条隧道前面的状态点，绿=可访问、红=本机没服务、灰=客户端已停
	for state, c := range dotColors {
		col := c
		img := render(32, func(x, y, size float64) ([3]float64, float64) {
			return dotSample(x, y, size, col)
		})
		if err := writePNG(filepath.Join(trayDir, "dot-"+state+".png"), img); err != nil {
			exit(err)
		}
	}
	fmt.Printf("隧道状态点 %d 个 -> %s\n", len(dotColors), trayDir)
}

// ---------- ICNS 容器 ----------

type icnsChunk struct {
	osType string
	data   []byte
}

// buildICNS 按 ICNS 规范组装：魔数 + 总长度，随后是若干「类型 + 长度 + 数据」块。
func buildICNS(chunks []icnsChunk) []byte {
	total := 8
	for _, c := range chunks {
		total += 8 + len(c.data)
	}

	out := make([]byte, 0, total)
	out = append(out, 'i', 'c', 'n', 's')
	out = appendUint32(out, uint32(total))
	for _, c := range chunks {
		out = append(out, c.osType...)
		out = appendUint32(out, uint32(8+len(c.data)))
		out = append(out, c.data...)
	}
	return out
}

func appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// render 以 4×4 超采样绘制图像，获得平滑边缘。
func render(size int, sample func(x, y, size float64) ([3]float64, float64)) *image.NRGBA {
	const sub = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	fsize := float64(size)

	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var sr, sg, sb, sa float64
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					x := float64(px) + (float64(sx)+0.5)/sub
					y := float64(py) + (float64(sy)+0.5)/sub
					c, a := sample(x, y, fsize)
					// 按 alpha 预乘后累加，避免边缘出现深色描边
					sr += c[0] * a
					sg += c[1] * a
					sb += c[2] * a
					sa += a
				}
			}
			n := float64(sub * sub)
			alpha := sa / n
			out := color.NRGBA{A: uint8(clamp01(alpha)*255 + 0.5)}
			if alpha > 0 {
				out.R = to8(sr / sa)
				out.G = to8(sg / sa)
				out.B = to8(sb / sa)
			}
			img.SetNRGBA(px, py, out)
		}
	}
	return img
}

// appIconSample 绘制应用图标：深色圆角方块内的同心信号环。
func appIconSample(x, y, size float64) ([3]float64, float64) {
	cx, cy := size/2, size/2

	// 苹果风格的超椭圆外形，四周留出图标规范要求的边距
	half := size * 0.82 / 2
	dx, dy := (x-cx)/half, (y-cy)/half
	const power = 5.0
	if math.Pow(math.Abs(dx), power)+math.Pow(math.Abs(dy), power) > 1 {
		return [3]float64{}, 0
	}

	// 底色自上而下渐深
	t := clamp01((y - (cy - half)) / (2 * half))
	col := mix(surfaceTop, surfaceBot, t)

	// 顶部内缘高光，给方块一点厚度感
	edgeDepth := 1 - math.Pow(math.Abs(dx), power) - math.Pow(math.Abs(dy), power)
	if edgeDepth < 0.16 && y < cy {
		col = mix(col, rgb(0x3a, 0x4c, 0x50), (0.16-edgeDepth)/0.16*0.5)
	}

	// 中心辉光，让信号环有发亮的观感
	dist := math.Hypot(x-cx, y-cy)
	glow := math.Exp(-(dist*dist)/(2*math.Pow(size*0.11, 2))) * 0.22
	col = add(col, scale(signal, glow))

	// 同心信号环：内圈亮、外圈迅速转淡，形成向外扩散的观感
	ringWidth := size * 0.028
	for _, r := range []struct{ radius, alpha float64 }{
		{0.170, 0.95},
		{0.268, 0.40},
		{0.366, 0.15},
	} {
		if math.Abs(dist-r.radius*size) <= ringWidth/2 {
			col = mix(col, signal, r.alpha)
		}
	}

	// 圆心实点，小尺寸下最先被识别的部分
	if dist <= size*0.072 {
		col = signalLit
	}

	return col, 1
}

// trayIconSample 绘制菜单栏图标：单环加圆心，用颜色表示状态。
func trayIconSample(x, y, size float64, c [3]float64) ([3]float64, float64) {
	cx, cy := size/2, size/2
	dist := math.Hypot(x-cx, y-cy)

	ringRadius := size * 0.30
	ringWidth := size * 0.10
	if math.Abs(dist-ringRadius) <= ringWidth/2 {
		return c, 1
	}
	if dist <= size*0.115 {
		return c, 1
	}
	return [3]float64{}, 0
}

// dotSample 绘制实心圆点，用作菜单项前的状态标记。
func dotSample(x, y, size float64, c [3]float64) ([3]float64, float64) {
	if math.Hypot(x-size/2, y-size/2) <= size*0.30 {
		return c, 1
	}
	return [3]float64{}, 0
}

// ---------- 颜色与数值工具 ----------

func rgb(r, g, b uint8) [3]float64 {
	return [3]float64{float64(r) / 255, float64(g) / 255, float64(b) / 255}
}

func mix(a, b [3]float64, t float64) [3]float64 {
	t = clamp01(t)
	return [3]float64{
		a[0] + (b[0]-a[0])*t,
		a[1] + (b[1]-a[1])*t,
		a[2] + (b[2]-a[2])*t,
	}
}

func add(a, b [3]float64) [3]float64 {
	return [3]float64{clamp01(a[0] + b[0]), clamp01(a[1] + b[1]), clamp01(a[2] + b[2])}
}

func scale(a [3]float64, f float64) [3]float64 {
	return [3]float64{a[0] * f, a[1] * f, a[2] * f}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func to8(v float64) uint8 { return uint8(clamp01(v)*255 + 0.5) }

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, "生成图标失败:", err)
	os.Exit(1)
}
