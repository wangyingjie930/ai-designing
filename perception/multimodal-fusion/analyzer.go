package multimodalfusion

import (
	"context"
	"strings"
)

// AnalyzerConfig wires parser, fuser, and business chart policy.
type AnalyzerConfig struct {
	Parser            *FileParser
	Fuser             *Fuser
	ParserConfig      ParserConfig
	FuserConfig       FuserConfig
	CriticalChartSpec CriticalChartSpec
}

// ReportAnalyzer prepares fused report context for the ADK agent.
type ReportAnalyzer struct {
	parser            *FileParser
	fuser             *Fuser
	criticalChartSpec CriticalChartSpec
}

// NewReportAnalyzer creates a report analyzer with defaults from the multimodal-fusion pattern.
func NewReportAnalyzer(config AnalyzerConfig) *ReportAnalyzer {
	parser := config.Parser
	if parser == nil {
		parserConfig := config.ParserConfig
		if parserConfig.PDFExtractor == nil {
			parserConfig.PDFExtractor = config.FuserConfig.PDFExtractor
		}
		if parserConfig.PDFExtractor == nil {
			parserConfig.PDFExtractor = FakePDFExtractor{}
		}
		parser = NewFileParser(parserConfig)
	}
	fuser := config.Fuser
	if fuser == nil {
		fuserConfig := config.FuserConfig
		if fuserConfig.PDFExtractor == nil {
			fuserConfig.PDFExtractor = FakePDFExtractor{}
		}
		fuser = NewFuser(fuserConfig)
	}
	spec := config.CriticalChartSpec
	if len(spec.PatternKeywords) == 0 {
		spec = DefaultCriticalChartSpec()
	}
	return &ReportAnalyzer{
		parser:            parser,
		fuser:             fuser,
		criticalChartSpec: spec,
	}
}

// PrepareReportContext parses files, fuses modalities, and returns the fixed report contract.
func (a *ReportAnalyzer) PrepareReportContext(ctx context.Context, request ReportAnalysisRequest) (*PreparedReportContext, error) {
	inputs, err := a.parser.BuildInputs(ctx, request)
	if err != nil {
		return nil, err
	}
	for i := range inputs {
		if inputs[i].Type == ModalityImage && !inputs[i].KeepAsImage {
			inputs[i].KeepAsImage = a.criticalChartSpec.ShouldKeepAsImage(inputs[i].Hint, inputs[i].Metadata)
		}
	}

	fused, err := a.fuser.Fuse(ctx, inputs)
	if err != nil {
		return nil, err
	}

	trace := fused.FusionTrace
	if !request.IncludeTrace {
		trace = nil
	}

	return &PreparedReportContext{
		Report:              request.Report,
		Task:                request.Task,
		Content:             fused.Content,
		TotalTokensEstimate: fused.TotalTokensEstimate,
		FusionTrace:         trace,
		Health:              fused.Health,
		OutputContract: []string{
			"核心论点摘要：给出 5-8 条结论，避免复述原文目录。",
			"数字事实核查：抽取所有关键数字结论，逐条给出来源页码、图表或表格引用；找不到引用时标为 low confidence。",
			"行动/销售要点：结合目标受众输出 5-7 条可直接使用的要点。",
			"风险与缺口：列出无法确认、需要补充原图或原表的数据点。",
		},
		FactCheckRequirement: []string{
			"精确数字优先使用表格、CSV、SQL 结果或 PDF 正文，不要只凭图像读数做计算。",
			"图表用于判断趋势、空间关系和图例含义；涉及同比、环比、CAGR 时必须基于结构化数字。",
			"每个数字 claim 都要带 source/page/chart/table/ref 和 confidence。",
		},
	}, nil
}

// DefaultCriticalChartSpec returns finance/report keywords that usually need spatial context.
func DefaultCriticalChartSpec() CriticalChartSpec {
	return CriticalChartSpec{PatternKeywords: []string{
		"市场规模", "市占率", "增长趋势", "营收", "利润率", "毛利率",
		"roe", "渗透率", "用户数", "arpu", "估值", "pe", "pb", "ps",
		"架构图", "流程图", "热力图", "双y轴", "趋势图",
	}}
}

// ShouldKeepAsImage decides whether layout or spatial relation is likely the signal.
func (s CriticalChartSpec) ShouldKeepAsImage(hint string, metadata map[string]string) bool {
	target := hint
	for k, v := range metadata {
		target += " " + k + " " + v
	}
	target = strings.ToLower(target)
	for _, keyword := range s.PatternKeywords {
		if strings.Contains(target, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}
