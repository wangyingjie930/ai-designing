package multimodalfusion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const fakePDFPageCount = 6

// PDFExtractor abstracts PDF parsing so tests or upstream systems can inject prepared content.
type PDFExtractor interface {
	ExtractPDF(ctx context.Context, path string, hint string) (PDFExtract, error)
}

// FakePDFExtractor returns deterministic PDF content without reading or parsing the PDF file.
type FakePDFExtractor struct {
	Extract PDFExtract
	Err     error
}

// ExtractPDF 只返回预置或模拟内容，避免默认链路执行真实 PDF 解析造成影响面扩大。
func (e FakePDFExtractor) ExtractPDF(ctx context.Context, path string, hint string) (PDFExtract, error) {
	if err := ctx.Err(); err != nil {
		return PDFExtract{}, err
	}
	if e.Err != nil {
		return PDFExtract{}, e.Err
	}
	if hasPDFExtractContent(e.Extract) {
		return e.Extract, nil
	}
	return BuildFakePDFExtract(path, hint), nil
}

// StaticPDFExtractor keeps the old test injection name while sharing the fake extractor behavior.
type StaticPDFExtractor struct {
	Extract PDFExtract
	Err     error
}

// ExtractPDF 兼容已有测试和调用方，通过显式预置结果绕过默认模拟数据。
func (e StaticPDFExtractor) ExtractPDF(ctx context.Context, path string, hint string) (PDFExtract, error) {
	return FakePDFExtractor{Extract: e.Extract, Err: e.Err}.ExtractPDF(ctx, path, hint)
}

// BuildFakePDFExtract 生成稳定的 PDF 抽取样例，用于验证正文、图片和表格的融合链路。
func BuildFakePDFExtract(path string, hint string) PDFExtract {
	source := fakePDFSourceName(path)
	sceneHint := strings.TrimSpace(hint)
	if sceneHint == "" {
		sceneHint = "多模态融合测试报告"
	}

	return PDFExtract{
		PageCount: fakePDFPageCount,
		TOC: strings.Join([]string{
			"1. 摘要",
			"2. 市场规模趋势",
			"3. 客群结构与流程",
			"4. 渠道转化表",
			"5. 风险提示",
		}, "\n"),
		SectionSummaries: []PDFSectionSummary{
			{Page: 1, Heading: "摘要", Summary: fmt.Sprintf("%s 是一份 fake PDF 抽取样例，来源文件为 %s。", sceneHint, source)},
			{Page: 2, Heading: "市场规模趋势", Summary: "保留一张关键趋势图，供 vision 模型判断走势和拐点。"},
			{Page: 4, Heading: "渠道转化表", Summary: "结构化表格用于数字核查，避免只从图片估算精确数值。"},
			{Page: 5, Heading: "风险提示", Summary: "模拟内容只用于链路测试，不代表真实 PDF 事实。"},
		},
		KeyPages: []PDFPage{
			{Page: 1, Text: fmt.Sprintf("核心结论：%s。该 fake PDF 抽取固定返回正文、关键图表和结构化表格，方便测试 Agent 的多模态融合行为。", sceneHint)},
			{Page: 2, Text: "图 1 市场规模增长趋势图显示，2024 到 2026 年样例市场规模从 120 增至 210，趋势明显上行。"},
			{Page: 5, Text: "风险提示：本文件不做真实 PDF 解析；数字、图表和页码均为测试桩生成。"},
		},
		Tables: []PDFTable{
			{
				Page:    4,
				Caption: "表 1 渠道转化指标",
				Data: Table{
					Header: []string{"渠道", "线索数", "转化率", "备注"},
					Rows: [][]string{
						{"自然流量", "1200", "18%", "稳定增长"},
						{"销售外呼", "860", "24%", "高意向"},
						{"合作渠道", "430", "15%", "样本较小"},
					},
					TotalRows: 3,
				},
			},
		},
		Images: []PDFImage{
			{
				Page:        2,
				Path:        fakePDFImagePath(path, "market-trend.png"),
				Caption:     "图 1 市场规模增长趋势图",
				Kind:        "critical_chart_page",
				KeepAsImage: true,
			},
			{
				Page:        3,
				Path:        fakePDFImagePath(path, "segment-bar.png"),
				Caption:     "图 2 客群结构对比柱状图",
				Kind:        "critical_chart_page",
				KeepAsImage: true,
			},
			{
				Page:        6,
				Path:        fakePDFImagePath(path, "decorative-logo.png"),
				Caption:     "页脚品牌装饰图",
				Kind:        "decorative_image",
				KeepAsImage: false,
			},
		},
		Warnings: []string{"fake_pdf_extract: real PDF parsing disabled"},
	}
}

// hasPDFExtractContent 判断调用方是否显式注入了抽取结果，避免覆盖测试自定义样例。
func hasPDFExtractContent(extract PDFExtract) bool {
	return extract.PageCount > 0 ||
		extract.TOC != "" ||
		len(extract.SectionSummaries) > 0 ||
		len(extract.KeyPages) > 0 ||
		len(extract.Tables) > 0 ||
		len(extract.Images) > 0 ||
		len(extract.Warnings) > 0
}

// fakePDFSourceName 统一 fake 抽取里的来源文案，让测试输出可读且稳定。
func fakePDFSourceName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "inline fake pdf"
	}
	return filepath.Base(path)
}

// fakePDFImagePath 优先关联同一 testdata 目录下的图片，找不到时返回稳定的相对路径。
func fakePDFImagePath(pdfPath string, imageName string) string {
	candidates := fakePDFImageCandidates(pdfPath, imageName)
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

// fakePDFImageCandidates 收敛测试素材查找路径，支持从工程根或包目录运行测试。
func fakePDFImageCandidates(pdfPath string, imageName string) []string {
	var candidates []string
	if pdfPath != "" {
		dir := filepath.Dir(pdfPath)
		parent := filepath.Dir(dir)
		candidates = append(candidates,
			filepath.Join(parent, "images", imageName),
			filepath.Join(dir, "images", imageName),
		)
	}
	candidates = append(candidates,
		filepath.Join("testdata", "images", imageName),
		filepath.Join("perception", "multimodal-fusion", "testdata", "images", imageName),
	)
	return candidates
}
