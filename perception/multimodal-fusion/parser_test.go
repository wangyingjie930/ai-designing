package multimodalfusion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileParserBuildInputs(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "metrics.csv")
	if err := os.WriteFile(csvPath, []byte("year,revenue\n2024,5800\n2025,7000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("核心论点：增长加速"), 0o644); err != nil {
		t.Fatal(err)
	}

	parser := NewFileParser(ParserConfig{MaxTableRows: 1})
	inputs, err := parser.BuildInputs(context.Background(), ReportAnalysisRequest{
		Files: []FileInput{
			{Path: csvPath, Hint: "财务表格"},
			{Path: textPath, Hint: "正文摘要"},
		},
	})
	if err != nil {
		t.Fatalf("BuildInputs() error = %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("inputs len = %d, want 2", len(inputs))
	}
	if inputs[0].Type != ModalityTable {
		t.Fatalf("first type = %s, want table", inputs[0].Type)
	}
	table, ok := inputs[0].Payload.(Table)
	if !ok {
		t.Fatalf("first payload type = %T, want Table", inputs[0].Payload)
	}
	if len(table.Rows) != 1 || table.TotalRows != 2 {
		t.Fatalf("rows len = %d total = %d", len(table.Rows), table.TotalRows)
	}
	if inputs[1].Type != ModalityText {
		t.Fatalf("second type = %s, want text", inputs[1].Type)
	}
}

// TestFileParserBuildInputsAcceptsImageURL 验证带 query 的远程图片 URL 会进入图片链路。
func TestFileParserBuildInputsAcceptsImageURL(t *testing.T) {
	parser := NewFileParser(ParserConfig{})

	inputs, err := parser.BuildInputs(context.Background(), ReportAnalysisRequest{
		Files: []FileInput{{
			URL:             "https://cdn.example.com/reports/chart.png?token=abc",
			Hint:            "远程市场规模图",
			ImageProcessing: ImageProcessingVision,
		}},
	})
	if err != nil {
		t.Fatalf("BuildInputs() error = %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs len = %d, want 1", len(inputs))
	}
	if inputs[0].Type != ModalityImage {
		t.Fatalf("type = %s, want image", inputs[0].Type)
	}
	if inputs[0].Payload != "https://cdn.example.com/reports/chart.png?token=abc" {
		t.Fatalf("payload = %v, want url", inputs[0].Payload)
	}
	if inputs[0].Metadata["source_type"] != "url" {
		t.Fatalf("metadata = %+v, want source_type=url", inputs[0].Metadata)
	}
	if inputs[0].ImageProcessing != ImageProcessingVision {
		t.Fatalf("image processing = %s, want vision", inputs[0].ImageProcessing)
	}
}

func TestFileParserExpandsPDFIntoBodyImagesAndTables(t *testing.T) {
	parser := NewFileParser(ParserConfig{
		PDFExtractor: StaticPDFExtractor{Extract: PDFExtract{
			PageCount:        22,
			TOC:              "1. 摘要",
			SectionSummaries: []PDFSectionSummary{{Page: 1, Heading: "摘要", Summary: "行业增长"}},
			KeyPages:         []PDFPage{{Page: 1, Text: "市场规模 5800 亿"}},
			Images: []PDFImage{{
				Page:        2,
				Path:        "output/report-agent/assets/chart.png",
				Caption:     "市场规模趋势图",
				Kind:        "critical_chart_page",
				Bytes:       1024,
				KeepAsImage: true,
			}},
			Tables: []PDFTable{{
				Page:    3,
				Caption: "财务数据",
				Data: Table{
					Header:    []string{"year", "revenue"},
					Rows:      [][]string{{"2024", "5800"}},
					TotalRows: 1,
				},
			}},
		}},
	})

	inputs, err := parser.BuildInputs(context.Background(), ReportAnalysisRequest{
		Files: []FileInput{{Path: "report.pdf", Kind: ModalityPDF, Hint: "行业研报"}},
	})
	if err != nil {
		t.Fatalf("BuildInputs() error = %v", err)
	}
	if len(inputs) != 3 {
		t.Fatalf("inputs len = %d, want 3", len(inputs))
	}
	if inputs[0].Type != ModalityPDF {
		t.Fatalf("body type = %s, want pdf", inputs[0].Type)
	}
	body, ok := inputs[0].Payload.(PDFExtract)
	if !ok {
		t.Fatalf("body payload type = %T, want PDFExtract", inputs[0].Payload)
	}
	if len(body.Images) != 0 || len(body.Tables) != 0 {
		t.Fatal("body payload should not duplicate image/table blocks")
	}
	if inputs[1].Type != ModalityImage || !inputs[1].KeepAsImage {
		t.Fatalf("image input = %+v, want kept image", inputs[1])
	}
	if inputs[2].Type != ModalityTable {
		t.Fatalf("table type = %s, want table", inputs[2].Type)
	}
}

func TestFileParserUsesFakePDFExtractorByDefault(t *testing.T) {
	parser := NewFileParser(ParserConfig{})

	inputs, err := parser.BuildInputs(context.Background(), ReportAnalysisRequest{
		Files: []FileInput{{Path: "testdata/reports/fake-market-report.pdf", Kind: ModalityPDF, Hint: "默认 fake 抽取"}},
	})
	if err != nil {
		t.Fatalf("BuildInputs() error = %v", err)
	}
	if len(inputs) != 4 {
		t.Fatalf("inputs len = %d, want body + two critical images + table", len(inputs))
	}
	body, ok := inputs[0].Payload.(PDFExtract)
	if !ok {
		t.Fatalf("body payload type = %T, want PDFExtract", inputs[0].Payload)
	}
	if len(body.Warnings) != 1 || body.Warnings[0] != "fake_pdf_extract: real PDF parsing disabled" {
		t.Fatalf("body warnings = %+v", body.Warnings)
	}
	if inputs[1].Type != ModalityImage || inputs[2].Type != ModalityImage {
		t.Fatalf("image inputs = %+v %+v", inputs[1], inputs[2])
	}
	if inputs[3].Type != ModalityTable {
		t.Fatalf("last type = %s, want table", inputs[3].Type)
	}
}
