package multimodalfusion

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const defaultImageTokenEstimate = 1400

// FuserConfig controls modality-specific compacting behavior.
type FuserConfig struct {
	PDFExtractor       PDFExtractor
	ImageTokenEstimate int
	MaxTableRows       int
	ImageTextExtractor ImageTextExtractor
	ImageProcessing    ImageProcessingMode
}

// Fuser routes each modality to the cheapest sufficient representation.
type Fuser struct {
	pdfExtractor       PDFExtractor
	imageTokenEstimate int
	maxTableRows       int
	imageTextExtractor ImageTextExtractor
	imageProcessing    ImageProcessingMode
}

// NewFuser creates a multimodal fuser with observable trace output.
func NewFuser(config FuserConfig) *Fuser {
	imageTokenEstimate := config.ImageTokenEstimate
	if imageTokenEstimate <= 0 {
		imageTokenEstimate = defaultImageTokenEstimate
	}
	maxTableRows := config.MaxTableRows
	if maxTableRows <= 0 {
		maxTableRows = defaultMaxTableRows
	}
	imageProcessing := normalizeImageProcessingMode(config.ImageProcessing)
	return &Fuser{
		pdfExtractor:       config.PDFExtractor,
		imageTokenEstimate: imageTokenEstimate,
		maxTableRows:       maxTableRows,
		imageTextExtractor: config.ImageTextExtractor,
		imageProcessing:    imageProcessing,
	}
}

// Fuse transforms heterogeneous inputs into compact content blocks and trace.
func (f *Fuser) Fuse(ctx context.Context, inputs []ModalityInput) (*FusionResult, error) {
	blocks := make([]FusionBlock, 0, len(inputs))
	events := make([]FusionEvent, 0, len(inputs))

	for _, input := range inputs {
		start := time.Now()
		block, event, err := f.fuseOne(ctx, input)
		if err != nil {
			return nil, err
		}
		event.ProcessingMS = time.Since(start).Milliseconds()
		event.Timestamp = time.Now().UTC()
		blocks = append(blocks, block)
		events = append(events, event)
	}

	result := &FusionResult{
		Content:             blocks,
		TotalTokensEstimate: sumTokens(events),
		FusionTrace:         events,
	}
	result.Health = HealthCheck(events)
	return result, nil
}

// fuseOne performs one modality conversion and records the method used.
func (f *Fuser) fuseOne(ctx context.Context, input ModalityInput) (FusionBlock, FusionEvent, error) {
	event := FusionEvent{
		Modality: input.Type,
		Source:   input.Source,
		BytesIn:  payloadSize(input.Payload),
	}

	if input.KeepAsImage || input.Type == ModalityImage {
		return f.fuseImage(ctx, input, event)
	}

	switch input.Type {
	case ModalityText:
		text := fmt.Sprint(input.Payload)
		event.Method = "direct_text"
		event.TokensOut = estimateTokens(text)
		return textBlock(input, text), event, nil
	case ModalityTable, ModalitySQLResult:
		text := f.tablePayloadToMarkdown(input.Payload)
		event.Method = "table_to_markdown"
		event.TokensOut = estimateTokens(text)
		return textBlock(input, text), event, nil
	case ModalityPDF:
		text, warning, err := f.pdfToSummary(ctx, input)
		if err != nil {
			return FusionBlock{}, event, err
		}
		event.Method = "pdf_body_summary"
		event.Warning = warning
		event.TokensOut = estimateTokens(text)
		return textBlock(input, text), event, nil
	case ModalityLog:
		text := compactLog(fmt.Sprint(input.Payload), input.Hint)
		event.Method = "log_prefilter_summary"
		event.TokensOut = estimateTokens(text)
		return textBlock(input, text), event, nil
	case ModalityAudio:
		text := fmt.Sprintf("[audio reference] source=%s hint=%s. Configure STT before asking the model to reason over transcript details.", input.Source, input.Hint)
		event.Method = "audio_reference"
		event.Warning = "stt_not_configured"
		event.TokensOut = estimateTokens(text)
		return textBlock(input, text), event, nil
	default:
		text := fmt.Sprint(input.Payload)
		event.Method = "fallback_string"
		event.TokensOut = estimateTokens(text)
		return textBlock(input, text), event, nil
	}
}

// fuseImage 在 OCR 和 vision 引用之间做路由，默认让模型直接看图片。
func (f *Fuser) fuseImage(ctx context.Context, input ModalityInput, event FusionEvent) (FusionBlock, FusionEvent, error) {
	mode := resolveImageProcessingMode(input.ImageProcessing, f.imageProcessing)
	if shouldTryImageOCR(mode, input, f.imageTextExtractor) {
		block, ocrEvent, err := f.imageToOCRText(ctx, input, event)
		if err == nil {
			return block, ocrEvent, nil
		}
		event.Warning = "ocr_failed_fallback_to_vision: " + err.Error()
	}
	block, visionEvent := f.imageToVisionBlock(input, event)
	return block, visionEvent, nil
}

// imageToOCRText 调用可注入的 OCR 服务，把图片先转成可核查文本。
func (f *Fuser) imageToOCRText(ctx context.Context, input ModalityInput, event FusionEvent) (FusionBlock, FusionEvent, error) {
	if f.imageTextExtractor == nil {
		return FusionBlock{}, event, fmt.Errorf("image OCR extractor is not configured")
	}
	imageURI := imageReference(input)
	text, err := f.imageTextExtractor.ExtractImageText(ctx, ImageTextExtractionRequest{
		URI:      imageURI,
		MIMEType: imageMIMEType(imageURI),
		Source:   input.Source,
		Hint:     input.Hint,
		Metadata: input.Metadata,
	})
	if err != nil {
		return FusionBlock{}, event, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return FusionBlock{}, event, fmt.Errorf("image OCR returned empty text")
	}

	ocrInput := input
	ocrInput.Metadata = mergeMetadata(input.Metadata, map[string]string{
		"image_processing": "ocr",
		"image_uri":        imageURI,
	})
	ocrText := fmt.Sprintf("[Image OCR text]\nsource=%s\nuri=%s\nhint=%s\n\n%s", input.Source, imageURI, input.Hint, text)
	event.Method = "image_ocr"
	event.TokensOut = estimateTokens(ocrText)
	return textBlock(ocrInput, ocrText), event, nil
}

// imageToVisionBlock 保留图片引用，后续 Agent 会把它转换成模型可见的多模态输入。
func (f *Fuser) imageToVisionBlock(input ModalityInput, event FusionEvent) (FusionBlock, FusionEvent) {
	imageURI := imageReference(input)
	text := fmt.Sprintf("[image retained for vision] uri=%s source=%s hint=%s", imageURI, input.Source, input.Hint)
	event.Method = "vision_reference"
	event.TokensOut = f.imageTokenEstimate
	return FusionBlock{
		Type:     "image_ref",
		Modality: ModalityImage,
		Text:     text,
		URI:      imageURI,
		MIMEType: imageMIMEType(imageURI),
		Source:   input.Source,
		Hint:     input.Hint,
		Metadata: mergeMetadata(input.Metadata, map[string]string{"image_processing": "vision"}),
	}, event
}

// resolveImageProcessingMode 优先使用单个输入的策略，再回退到 fuser 默认策略。
func resolveImageProcessingMode(inputMode ImageProcessingMode, fallback ImageProcessingMode) ImageProcessingMode {
	if normalized := normalizeImageProcessingMode(inputMode); normalized != ImageProcessingAuto {
		return normalized
	}
	return normalizeImageProcessingMode(fallback)
}

// normalizeImageProcessingMode 把未知值收敛为 auto，避免工具入参拼写错误直接打断链路。
func normalizeImageProcessingMode(mode ImageProcessingMode) ImageProcessingMode {
	switch ImageProcessingMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ImageProcessingVision:
		return ImageProcessingVision
	case ImageProcessingOCR:
		return ImageProcessingOCR
	default:
		return ImageProcessingAuto
	}
}

// shouldTryImageOCR 只有显式 OCR，或 auto 且配置了 OCR 服务并且不是强保留图片时才转文字。
func shouldTryImageOCR(mode ImageProcessingMode, input ModalityInput, extractor ImageTextExtractor) bool {
	if mode == ImageProcessingOCR {
		return true
	}
	return mode == ImageProcessingAuto && extractor != nil && !input.KeepAsImage
}

// pdfToSummary consumes a pre-extracted PDF body or falls back to local extraction.
func (f *Fuser) pdfToSummary(ctx context.Context, input ModalityInput) (string, string, error) {
	switch payload := input.Payload.(type) {
	case PDFExtract:
		text := buildPDFSummary(payload, input.Hint)
		return text, strings.Join(payload.Warnings, "; "), nil
	case *PDFExtract:
		if payload == nil {
			return "[empty PDF extract]", "", nil
		}
		text := buildPDFSummary(*payload, input.Hint)
		return text, strings.Join(payload.Warnings, "; "), nil
	}

	extractor := f.pdfExtractor
	if extractor == nil {
		extractor = FakePDFExtractor{}
	}

	extract, err := extractor.ExtractPDF(ctx, fmt.Sprint(input.Payload), input.Hint)
	if err != nil {
		return "", "", err
	}
	text := buildPDFSummary(extract, input.Hint)
	warning := strings.Join(extract.Warnings, "; ")
	return text, warning, nil
}

// tablePayloadToMarkdown turns tables into compact markdown samples.
func (f *Fuser) tablePayloadToMarkdown(payload any) string {
	switch table := payload.(type) {
	case Table:
		return tableToMarkdown(table, f.maxTableRows)
	case *Table:
		if table == nil {
			return "[empty table]"
		}
		return tableToMarkdown(*table, f.maxTableRows)
	case []map[string]any:
		return mapsToMarkdown(table, f.maxTableRows)
	default:
		return fmt.Sprint(payload)
	}
}

// HealthCheck flags suspicious token distribution by modality.
func HealthCheck(events []FusionEvent) map[string]string {
	if len(events) == 0 {
		return map[string]string{"status": "no fusion events"}
	}

	byModality := map[ModalityType]int{}
	total := 0
	for _, event := range events {
		byModality[event.Modality] += event.TokensOut
		total += event.TokensOut
	}
	if total == 0 {
		total = 1
	}

	report := map[string]string{}
	if ratio := float64(byModality[ModalityImage]) / float64(total); ratio > 0.5 {
		report["image_token_overshoot"] = fmt.Sprintf("image tokens = %.1f%%; check whether tables or decorative screenshots should become markdown or be dropped", ratio*100)
	}
	if ratio := float64(byModality[ModalityLog]) / float64(total); ratio > 0.4 {
		report["log_token_overshoot"] = fmt.Sprintf("log tokens = %.1f%%; prefilter rules may be too loose", ratio*100)
	}
	if ratio := float64(byModality[ModalityPDF]) / float64(total); ratio > 0.7 {
		report["pdf_token_overshoot"] = fmt.Sprintf("pdf tokens = %.1f%%; key-page extraction may be too broad", ratio*100)
	}
	if len(report) == 0 {
		report["status"] = "ok"
	}
	return report
}

// buildPDFSummary preserves TOC, key pages, tables, and extraction warnings.
func buildPDFSummary(extract PDFExtract, hint string) string {
	var parts []string
	if hint != "" {
		parts = append(parts, "[Business hint]\n"+hint)
	}
	if extract.TOC != "" {
		parts = append(parts, "[PDF TOC]\n"+extract.TOC)
	}
	for _, section := range extract.SectionSummaries {
		var sectionParts []string
		if section.Heading != "" {
			sectionParts = append(sectionParts, "heading: "+section.Heading)
		}
		if section.Summary != "" {
			sectionParts = append(sectionParts, "summary: "+section.Summary)
		}
		if len(sectionParts) > 0 {
			parts = append(parts, fmt.Sprintf("[Section summary page %d]\n%s", section.Page, strings.Join(sectionParts, "\n")))
		}
	}
	for _, page := range extract.KeyPages {
		text := strings.TrimSpace(page.Text)
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[Page %d]\n%s", page.Page, text))
	}
	for idx, table := range extract.Tables {
		label := fmt.Sprintf("Extracted table %d", idx+1)
		if table.Page > 0 {
			label += fmt.Sprintf(" page %d", table.Page)
		}
		if table.Caption != "" {
			label += ": " + table.Caption
		}
		parts = append(parts, fmt.Sprintf("[%s]\n%s", label, tableToMarkdown(table.Data, defaultMaxTableRows)))
	}
	if len(extract.Warnings) > 0 {
		parts = append(parts, "[Extraction warnings]\n- "+strings.Join(extract.Warnings, "\n- "))
	}
	if len(parts) == 0 {
		return "[empty PDF extract]"
	}
	return strings.Join(parts, "\n\n")
}

// compactLog keeps the lines most likely to affect diagnosis or report facts.
func compactLog(content string, hint string) string {
	lines := strings.Split(content, "\n")
	keywords := []string{"error", "exception", "fatal", "panic", "warn", "timeout", "failed", "denied", "slow", "trace", "request_id"}
	var kept []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, keyword := range keywords {
			if strings.Contains(lower, keyword) {
				kept = append(kept, line)
				break
			}
		}
		if len(kept) >= 80 {
			break
		}
	}
	if len(kept) == 0 {
		limit := min(len(lines), 40)
		kept = lines[:limit]
	}
	prefix := "[Log structured summary]"
	if hint != "" {
		prefix += "\nhint: " + hint
	}
	return prefix + "\n" + strings.Join(kept, "\n")
}

// tableToMarkdown converts bounded table samples to markdown with a total-row marker.
func tableToMarkdown(table Table, maxRows int) string {
	if len(table.Header) == 0 {
		return "[empty table]"
	}
	rows := table.Rows
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}

	var b strings.Builder
	b.WriteString("| ")
	b.WriteString(strings.Join(table.Header, " | "))
	b.WriteString(" |\n|")
	for range table.Header {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, row := range rows {
		b.WriteString("| ")
		cells := make([]string, len(table.Header))
		for i := range table.Header {
			if i < len(row) {
				cells[i] = sanitizeMarkdownCell(row[i])
			}
		}
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString(" |\n")
	}
	if table.TotalRows > len(rows) {
		b.WriteString(fmt.Sprintf("\n_(%d rows total, showing top %d)_", table.TotalRows, len(rows)))
	}
	return b.String()
}

// mapsToMarkdown supports already-structured SQL result rows.
func mapsToMarkdown(rows []map[string]any, maxRows int) string {
	if len(rows) == 0 {
		return "[empty result]"
	}
	cols := make([]string, 0, len(rows[0]))
	for col := range rows[0] {
		cols = append(cols, col)
	}
	sort.Strings(cols)

	table := Table{Header: cols, TotalRows: len(rows)}
	limit := min(len(rows), maxRows)
	for _, row := range rows[:limit] {
		cells := make([]string, len(cols))
		for i, col := range cols {
			cells[i] = fmt.Sprint(row[col])
		}
		table.Rows = append(table.Rows, cells)
	}
	return tableToMarkdown(table, maxRows)
}

// textBlock wraps text output with source metadata.
func textBlock(input ModalityInput, text string) FusionBlock {
	return FusionBlock{
		Type:     "text",
		Modality: input.Type,
		Text:     text,
		Source:   input.Source,
		Hint:     input.Hint,
		Metadata: input.Metadata,
	}
}

// imageReference prefers the extracted asset path carried by the payload.
func imageReference(input ModalityInput) string {
	switch payload := input.Payload.(type) {
	case string:
		if strings.TrimSpace(payload) != "" {
			return payload
		}
	case PDFImage:
		if payload.Path != "" {
			return payload.Path
		}
	}
	return input.Source
}

// imageMIMEType labels local rendered assets for multimodal tool consumers.
func imageMIMEType(uri string) string {
	switch strings.ToLower(strings.TrimPrefix(sourceExt(uri), ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

// sumTokens aggregates token estimates across events.
func sumTokens(events []FusionEvent) int {
	total := 0
	for _, event := range events {
		total += event.TokensOut
	}
	return total
}

// estimateTokens uses a simple language-agnostic character estimate.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 2
	if tokens < 1 {
		return 1
	}
	return tokens
}

// payloadSize gives trace users a rough byte-size signal before compaction.
func payloadSize(payload any) int {
	switch v := payload.(type) {
	case string:
		return len(v)
	case []byte:
		return len(v)
	case Table:
		return len(tableToMarkdown(v, len(v.Rows)))
	case *Table:
		if v == nil {
			return 0
		}
		return len(tableToMarkdown(*v, len(v.Rows)))
	case PDFExtract:
		return len(buildPDFSummary(v, ""))
	case *PDFExtract:
		if v == nil {
			return 0
		}
		return len(buildPDFSummary(*v, ""))
	default:
		return len(fmt.Sprint(payload))
	}
}

// sanitizeMarkdownCell keeps generated table markdown valid.
func sanitizeMarkdownCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.TrimSpace(value)
}
