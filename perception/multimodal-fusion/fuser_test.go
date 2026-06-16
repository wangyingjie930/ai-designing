package multimodalfusion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeImageTextExtractor 是测试用 OCR 桩，避免依赖外部服务。
type fakeImageTextExtractor struct {
	text string
	err  error
}

// ExtractImageText 返回测试预置的 OCR 文本或错误。
func (e fakeImageTextExtractor) ExtractImageText(context.Context, ImageTextExtractionRequest) (string, error) {
	if e.err != nil {
		return "", e.err
	}
	return e.text, nil
}

func TestFuserRoutesTablePDFLogAndImage(t *testing.T) {
	fuser := NewFuser(FuserConfig{
		PDFExtractor: StaticPDFExtractor{Extract: PDFExtract{
			TOC:      "1. Summary",
			KeyPages: []PDFPage{{Page: 3, Text: "Revenue reached 5800 亿 with 20% YoY growth."}},
		}},
		ImageTokenEstimate: 100,
	})

	result, err := fuser.Fuse(context.Background(), []ModalityInput{
		{Type: ModalityTable, Source: "sales.csv", Payload: Table{Header: []string{"year", "revenue"}, Rows: [][]string{{"2024", "5800"}}, TotalRows: 1}},
		{Type: ModalityPDF, Source: "report.pdf", Payload: "report.pdf", Hint: "industry report"},
		{Type: ModalityLog, Source: "app.log", Payload: "info ok\nERROR request_id=1 timeout"},
		{Type: ModalityImage, Source: "chart.png", Hint: "市场规模趋势图"},
	})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if len(result.Content) != 4 {
		t.Fatalf("content len = %d, want 4", len(result.Content))
	}
	if result.FusionTrace[0].Method != "table_to_markdown" {
		t.Fatalf("table method = %s", result.FusionTrace[0].Method)
	}
	if !strings.Contains(result.Content[1].Text, "[Page 3]") {
		t.Fatalf("pdf summary missing page marker: %s", result.Content[1].Text)
	}
	if !strings.Contains(result.Content[2].Text, "ERROR request_id=1 timeout") {
		t.Fatalf("log summary missing error line: %s", result.Content[2].Text)
	}
	if result.Content[3].Type != "image_ref" {
		t.Fatalf("image block type = %s", result.Content[3].Type)
	}
	if result.FusionTrace[3].Method != "vision_reference" {
		t.Fatalf("image method = %s, want vision_reference", result.FusionTrace[3].Method)
	}
}

// TestFuserCanOCRImageWhenRequested 验证显式 OCR 模式会先把图片转换成文本块。
func TestFuserCanOCRImageWhenRequested(t *testing.T) {
	fuser := NewFuser(FuserConfig{
		ImageTextExtractor: fakeImageTextExtractor{text: "图中写着 ARR 7000 万，增长率 24%。"},
	})

	result, err := fuser.Fuse(context.Background(), []ModalityInput{{
		Type:            ModalityImage,
		Source:          "https://cdn.example.com/chart.png",
		Payload:         "https://cdn.example.com/chart.png",
		Hint:            "截图数字",
		ImageProcessing: ImageProcessingOCR,
	}})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if result.Content[0].Type != "text" {
		t.Fatalf("block type = %s, want text", result.Content[0].Type)
	}
	if !strings.Contains(result.Content[0].Text, "ARR 7000 万") {
		t.Fatalf("OCR text missing: %s", result.Content[0].Text)
	}
	if result.FusionTrace[0].Method != "image_ocr" {
		t.Fatalf("method = %s, want image_ocr", result.FusionTrace[0].Method)
	}
}

// TestFuserFallsBackToVisionWhenOCRFails 验证 OCR 不可用时不会丢图，而是回退到视觉输入。
func TestFuserFallsBackToVisionWhenOCRFails(t *testing.T) {
	fuser := NewFuser(FuserConfig{
		ImageTextExtractor: fakeImageTextExtractor{err: errors.New("ocr service down")},
	})

	result, err := fuser.Fuse(context.Background(), []ModalityInput{{
		Type:            ModalityImage,
		Source:          "https://cdn.example.com/chart.png",
		Payload:         "https://cdn.example.com/chart.png",
		ImageProcessing: ImageProcessingOCR,
	}})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if result.Content[0].Type != "image_ref" {
		t.Fatalf("block type = %s, want image_ref", result.Content[0].Type)
	}
	if !strings.Contains(result.FusionTrace[0].Warning, "ocr_failed_fallback_to_vision") {
		t.Fatalf("warning = %s, want OCR fallback warning", result.FusionTrace[0].Warning)
	}
}

func TestCriticalChartSpec(t *testing.T) {
	spec := DefaultCriticalChartSpec()
	if !spec.ShouldKeepAsImage("2026 市场规模趋势图", nil) {
		t.Fatal("expected market size trend chart to be kept as image")
	}
	if spec.ShouldKeepAsImage("普通纯文本说明", nil) {
		t.Fatal("plain text hint should not force image preservation")
	}
}
