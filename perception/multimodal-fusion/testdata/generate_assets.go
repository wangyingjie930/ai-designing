//go:build ignore

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

var (
	ink        = color.RGBA{R: 35, G: 45, B: 60, A: 255}
	grid       = color.RGBA{R: 220, G: 226, B: 234, A: 255}
	blue       = color.RGBA{R: 36, G: 99, B: 235, A: 255}
	green      = color.RGBA{R: 20, G: 184, B: 166, A: 255}
	orange     = color.RGBA{R: 245, G: 158, B: 11, A: 255}
	rose       = color.RGBA{R: 225, G: 29, B: 72, A: 255}
	background = color.RGBA{R: 248, G: 250, B: 252, A: 255}
)

type point struct {
	X int
	Y int
}

func main() {
	root := testdataDir()
	must(os.MkdirAll(filepath.Join(root, "images"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "reports"), 0o755))
	must(writeMarketTrend(filepath.Join(root, "images", "market-trend.png")))
	must(writeSegmentBar(filepath.Join(root, "images", "segment-bar.png")))
	must(writeProcessDiagram(filepath.Join(root, "images", "process-diagram.png")))
	must(writeDecorativeLogo(filepath.Join(root, "images", "decorative-logo.png")))
	must(writeFakePDF(filepath.Join(root, "reports", "fake-market-report.pdf")))
}

// testdataDir 定位当前生成脚本所在目录，保证从工程根或任意目录运行都能写到同一批素材。
func testdataDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot locate generator")
	}
	return filepath.Dir(file)
}

// must 让生成脚本失败时尽早退出，避免留下半套测试素材。
func must(err error) {
	if err != nil {
		panic(err)
	}
}

// writeMarketTrend 生成折线趋势图，用于测试关键图表 image_ref。
func writeMarketTrend(path string) error {
	img := newCanvas(640, 360)
	drawGrid(img, 72, 40, 540, 260)
	drawLine(img, point{X: 72, Y: 300}, point{X: 592, Y: 300}, ink)
	drawLine(img, point{X: 72, Y: 300}, point{X: 72, Y: 40}, ink)

	points := []point{
		{X: 110, Y: 248},
		{X: 210, Y: 220},
		{X: 310, Y: 178},
		{X: 410, Y: 132},
		{X: 510, Y: 92},
	}
	for i := 1; i < len(points); i++ {
		drawThickLine(img, points[i-1], points[i], blue, 4)
	}
	for _, p := range points {
		fillCircle(img, p, 8, orange)
	}
	return writePNG(path, img)
}

// writeSegmentBar 生成分组柱状图，用于测试结构对比类视觉输入。
func writeSegmentBar(path string) error {
	img := newCanvas(640, 360)
	drawGrid(img, 72, 40, 540, 260)
	drawLine(img, point{X: 72, Y: 300}, point{X: 592, Y: 300}, ink)
	drawLine(img, point{X: 72, Y: 300}, point{X: 72, Y: 40}, ink)

	values := []int{150, 210, 125, 180}
	colors := []color.Color{blue, green, orange, rose}
	for i, value := range values {
		x := 120 + i*110
		rect := image.Rect(x, 300-value, x+54, 300)
		draw.Draw(img, rect, &image.Uniform{C: colors[i]}, image.Point{}, draw.Src)
	}
	return writePNG(path, img)
}

// writeProcessDiagram 生成流程图，用于单独测试空间关系明显的图片输入。
func writeProcessDiagram(path string) error {
	img := newCanvas(640, 360)
	boxes := []image.Rectangle{
		image.Rect(58, 130, 178, 210),
		image.Rect(260, 130, 380, 210),
		image.Rect(462, 130, 582, 210),
	}
	colors := []color.Color{blue, green, orange}
	for i, box := range boxes {
		draw.Draw(img, box, &image.Uniform{C: colors[i]}, image.Point{}, draw.Src)
		drawRectBorder(img, box, ink)
		if i > 0 {
			drawArrow(img, point{X: boxes[i-1].Max.X + 18, Y: 170}, point{X: box.Min.X - 18, Y: 170}, ink)
		}
	}
	return writePNG(path, img)
}

// writeDecorativeLogo 生成装饰图，用于验证 fake PDF 抽取不会保留无业务信号的图片。
func writeDecorativeLogo(path string) error {
	img := newCanvas(320, 180)
	fillCircle(img, point{X: 92, Y: 90}, 46, blue)
	fillCircle(img, point{X: 150, Y: 90}, 46, green)
	fillCircle(img, point{X: 208, Y: 90}, 46, orange)
	return writePNG(path, img)
}

// writeFakePDF 生成可打开的占位 PDF；解析内容仍由 FakePDFExtractor 固定返回。
func writeFakePDF(path string) error {
	content := "BT\n/F1 18 Tf\n72 720 Td\n(Fake PDF fixture - real extraction disabled) Tj\n0 -30 Td\n(Use FakePDFExtractor for multimodal fusion tests.) Tj\nET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for i, object := range objects {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// newCanvas 创建统一背景的测试画布，让图片在截图中容易区分。
func newCanvas(width int, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
	return img
}

// writePNG 写出 PNG 文件，保持 fixture 体积小且无需外部图片库。
func writePNG(path string, img image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}

// drawGrid 绘制轻量网格，帮助测试图表类图片的视觉结构。
func drawGrid(img *image.RGBA, left int, top int, width int, height int) {
	for i := 0; i <= 4; i++ {
		y := top + i*height/4
		drawLine(img, point{X: left, Y: y}, point{X: left + width, Y: y}, grid)
	}
	for i := 0; i <= 5; i++ {
		x := left + i*width/5
		drawLine(img, point{X: x, Y: top}, point{X: x, Y: top + height}, grid)
	}
}

// drawRectBorder 给流程图节点加边框，避免色块边界不清。
func drawRectBorder(img *image.RGBA, rect image.Rectangle, c color.Color) {
	drawLine(img, point{X: rect.Min.X, Y: rect.Min.Y}, point{X: rect.Max.X, Y: rect.Min.Y}, c)
	drawLine(img, point{X: rect.Max.X, Y: rect.Min.Y}, point{X: rect.Max.X, Y: rect.Max.Y}, c)
	drawLine(img, point{X: rect.Max.X, Y: rect.Max.Y}, point{X: rect.Min.X, Y: rect.Max.Y}, c)
	drawLine(img, point{X: rect.Min.X, Y: rect.Max.Y}, point{X: rect.Min.X, Y: rect.Min.Y}, c)
}

// drawArrow 绘制流程图箭头，保留空间流向信息。
func drawArrow(img *image.RGBA, from point, to point, c color.Color) {
	drawThickLine(img, from, to, c, 3)
	angle := math.Atan2(float64(to.Y-from.Y), float64(to.X-from.X))
	for _, delta := range []float64{math.Pi * 0.82, -math.Pi * 0.82} {
		end := point{
			X: to.X + int(math.Cos(angle+delta)*18),
			Y: to.Y + int(math.Sin(angle+delta)*18),
		}
		drawThickLine(img, to, end, c, 3)
	}
}

// drawThickLine 用多条相邻线近似粗线，满足测试图表的清晰度需求。
func drawThickLine(img *image.RGBA, from point, to point, c color.Color, width int) {
	for offset := -width / 2; offset <= width/2; offset++ {
		drawLine(img, point{X: from.X, Y: from.Y + offset}, point{X: to.X, Y: to.Y + offset}, c)
		drawLine(img, point{X: from.X + offset, Y: from.Y}, point{X: to.X + offset, Y: to.Y}, c)
	}
}

// drawLine 使用 Bresenham 算法绘制基础线段，避免引入绘图库依赖。
func drawLine(img *image.RGBA, from point, to point, c color.Color) {
	dx := abs(to.X - from.X)
	dy := -abs(to.Y - from.Y)
	sx := -1
	if from.X < to.X {
		sx = 1
	}
	sy := -1
	if from.Y < to.Y {
		sy = 1
	}
	err := dx + dy
	x := from.X
	y := from.Y
	for {
		if image.Pt(x, y).In(img.Bounds()) {
			img.Set(x, y, c)
		}
		if x == to.X && y == to.Y {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += sx
		}
		if e2 <= dx {
			err += dx
			y += sy
		}
	}
}

// fillCircle 绘制图表节点和装饰图圆形，便于区分不同视觉元素。
func fillCircle(img *image.RGBA, center point, radius int, c color.Color) {
	r2 := radius * radius
	for y := center.Y - radius; y <= center.Y+radius; y++ {
		for x := center.X - radius; x <= center.X+radius; x++ {
			if (x-center.X)*(x-center.X)+(y-center.Y)*(y-center.Y) <= r2 && image.Pt(x, y).In(img.Bounds()) {
				img.Set(x, y, c)
			}
		}
	}
}

// abs 返回整数绝对值，供线段绘制算法复用。
func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
