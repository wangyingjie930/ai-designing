package multimodalfusion

import (
	"context"
	"strings"
	"testing"
)

func TestFakePDFExtractorBuildsFixtureExtract(t *testing.T) {
	extract, err := FakePDFExtractor{}.ExtractPDF(context.Background(), "testdata/reports/fake-market-report.pdf", "行业测试")
	if err != nil {
		t.Fatalf("ExtractPDF() error = %v", err)
	}
	if extract.PageCount != fakePDFPageCount {
		t.Fatalf("page count = %d, want %d", extract.PageCount, fakePDFPageCount)
	}
	if len(extract.KeyPages) == 0 || !strings.Contains(extract.KeyPages[0].Text, "行业测试") {
		t.Fatalf("key pages missing hint: %+v", extract.KeyPages)
	}
	if len(extract.Tables) != 1 || extract.Tables[0].Data.TotalRows != 3 {
		t.Fatalf("tables = %+v, want one fixture table", extract.Tables)
	}
	if len(extract.Images) != 3 {
		t.Fatalf("images len = %d, want critical images plus decorative sample", len(extract.Images))
	}
	if !extract.Images[0].KeepAsImage || extract.Images[2].KeepAsImage {
		t.Fatalf("image keep flags = %+v", extract.Images)
	}
	if !strings.Contains(strings.Join(extract.Warnings, ","), "real PDF parsing disabled") {
		t.Fatalf("warnings = %+v", extract.Warnings)
	}
}

func TestStaticPDFExtractorReturnsConfiguredExtract(t *testing.T) {
	want := PDFExtract{KeyPages: []PDFPage{{Page: 9, Text: "configured extract"}}}
	got, err := StaticPDFExtractor{Extract: want}.ExtractPDF(context.Background(), "ignored.pdf", "")
	if err != nil {
		t.Fatalf("ExtractPDF() error = %v", err)
	}
	if got.KeyPages[0].Page != 9 || got.KeyPages[0].Text != "configured extract" {
		t.Fatalf("got = %+v, want configured extract", got)
	}
}
