package multimodalfusion

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	defaultMaxTextBytes = 96 * 1024
	defaultMaxTableRows = 30
)

// ParserConfig controls local file parsing limits.
type ParserConfig struct {
	MaxTextBytes int
	MaxTableRows int
	PDFExtractor PDFExtractor
}

// FileParser normalizes user files into modality inputs before fusion.
type FileParser struct {
	maxTextBytes int
	maxTableRows int
	pdfExtractor PDFExtractor
}

// NewFileParser creates a parser with conservative defaults.
func NewFileParser(config ParserConfig) *FileParser {
	maxTextBytes := config.MaxTextBytes
	if maxTextBytes <= 0 {
		maxTextBytes = defaultMaxTextBytes
	}
	maxTableRows := config.MaxTableRows
	if maxTableRows <= 0 {
		maxTableRows = defaultMaxTableRows
	}
	return &FileParser{
		maxTextBytes: maxTextBytes,
		maxTableRows: maxTableRows,
		pdfExtractor: config.PDFExtractor,
	}
}

// BuildInputs turns file requests into typed modality inputs.
func (p *FileParser) BuildInputs(ctx context.Context, request ReportAnalysisRequest) ([]ModalityInput, error) {
	if len(request.Files) == 0 {
		return nil, fmt.Errorf("at least one file or inline content is required")
	}

	maxTextBytes := firstPositive(request.MaxTextBytes, p.maxTextBytes)
	maxTableRows := firstPositive(request.MaxTableRows, p.maxTableRows)

	inputs := make([]ModalityInput, 0, len(request.Files)*3)
	for _, file := range request.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		kind := detectKind(file)
		if kind == ModalityPDF && file.Path != "" && file.InlineContent == "" {
			pdfInputs, err := p.expandPDF(ctx, file)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, pdfInputs...)
			continue
		}

		payload, metadata, err := p.loadPayload(file, kind, maxTextBytes, maxTableRows)
		if err != nil {
			return nil, err
		}

		inputs = append(inputs, ModalityInput{
			Type:            kind,
			Payload:         payload,
			Source:          sourceName(file),
			Hint:            file.Hint,
			KeepAsImage:     file.KeepAsImage,
			ImageProcessing: file.ImageProcessing,
			Metadata:        mergeMetadata(file.Metadata, metadata),
		})
	}

	return inputs, nil
}

// expandPDF implements the document pattern: body text, key charts, and tables become separate inputs.
func (p *FileParser) expandPDF(ctx context.Context, file FileInput) ([]ModalityInput, error) {
	extractor := p.pdfExtractor
	if extractor == nil {
		extractor = FakePDFExtractor{}
	}

	source := sourceName(file)
	extract, err := extractor.ExtractPDF(ctx, file.Path, file.Hint)
	if err != nil {
		return nil, err
	}

	body := extract
	body.Tables = nil
	body.Images = nil

	inputs := []ModalityInput{{
		Type:     ModalityPDF,
		Payload:  body,
		Source:   source,
		Hint:     pdfBodyHint(file),
		Metadata: pdfBaseMetadata(file.Metadata, extract),
	}}

	for idx, image := range extract.Images {
		if !image.KeepAsImage {
			continue
		}
		metadata := mergeMetadata(file.Metadata, map[string]string{
			"format":      "pdf_image",
			"page":        strconv.Itoa(image.Page),
			"image_index": strconv.Itoa(idx + 1),
			"kind":        firstNonEmpty(image.Kind, "critical_chart"),
		})
		if image.Bytes > 0 {
			metadata["bytes"] = strconv.Itoa(image.Bytes)
		}
		inputs = append(inputs, ModalityInput{
			Type:            ModalityImage,
			Payload:         firstNonEmpty(image.Path, source),
			Source:          firstNonEmpty(image.Path, source),
			Hint:            pdfImageHint(image, idx),
			KeepAsImage:     true,
			ImageProcessing: file.ImageProcessing,
			Metadata:        metadata,
		})
	}

	for idx, table := range extract.Tables {
		metadata := mergeMetadata(file.Metadata, map[string]string{
			"format":      "pdf_table",
			"page":        strconv.Itoa(table.Page),
			"table_index": strconv.Itoa(idx + 1),
		})
		inputs = append(inputs, ModalityInput{
			Type:     ModalityTable,
			Payload:  table.Data,
			Source:   source,
			Hint:     pdfTableHint(table, idx),
			Metadata: metadata,
		})
	}

	return inputs, nil
}

// detectKind keeps source classification explicit but allows extension-based fallback.
func detectKind(file FileInput) ModalityType {
	if file.Kind != "" {
		return file.Kind
	}
	ref := fileReference(file)
	if isImageDataURL(ref) {
		return ModalityImage
	}
	ext := sourceExt(ref)
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff":
		return ModalityImage
	case ".csv", ".tsv":
		return ModalityTable
	case ".log":
		return ModalityLog
	case ".pdf":
		return ModalityPDF
	case ".wav", ".mp3", ".m4a", ".aac", ".ogg":
		return ModalityAudio
	default:
		return ModalityText
	}
}

// loadPayload delays heavyweight parsing until the chosen modality actually needs it.
func (p *FileParser) loadPayload(file FileInput, kind ModalityType, maxTextBytes int, maxTableRows int) (any, map[string]string, error) {
	if file.InlineContent != "" {
		return p.inlinePayload(file, kind, maxTableRows)
	}
	ref := fileReference(file)
	if ref == "" {
		return nil, nil, fmt.Errorf("file path or inline_content is required")
	}

	switch kind {
	case ModalityImage:
		return ref, imageSourceMetadata(ref), nil
	case ModalityAudio, ModalityPDF:
		return ref, nil, nil
	case ModalityTable, ModalitySQLResult:
		table, err := readCSVLike(ref, maxTableRows)
		if err != nil {
			return nil, nil, err
		}
		return table, map[string]string{"format": "table"}, nil
	case ModalityLog, ModalityText:
		text, truncated, err := readTextFile(ref, maxTextBytes)
		if err != nil {
			return nil, nil, err
		}
		metadata := map[string]string{}
		if truncated {
			metadata["truncated"] = "true"
		}
		return text, metadata, nil
	default:
		text, truncated, err := readTextFile(ref, maxTextBytes)
		if err != nil {
			return nil, nil, err
		}
		metadata := map[string]string{}
		if truncated {
			metadata["truncated"] = "true"
		}
		return text, metadata, nil
	}
}

// inlinePayload handles pasted content without touching the filesystem.
func (p *FileParser) inlinePayload(file FileInput, kind ModalityType, maxTableRows int) (any, map[string]string, error) {
	switch kind {
	case ModalityTable, ModalitySQLResult:
		table, err := parseCSVLike(strings.NewReader(file.InlineContent), delimiterForPath(fileReference(file)), maxTableRows)
		if err != nil {
			return nil, nil, err
		}
		return table, map[string]string{"format": "table", "inline": "true"}, nil
	case ModalityImage, ModalityAudio, ModalityPDF:
		return file.InlineContent, map[string]string{"inline": "true"}, nil
	default:
		return file.InlineContent, map[string]string{"inline": "true"}, nil
	}
}

// readTextFile reads text-like files with a hard upper bound.
func readTextFile(path string, maxBytes int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	limit := int64(maxBytes + 1)
	b, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", false, err
	}
	truncated := len(b) > maxBytes
	if truncated {
		b = b[:maxBytes]
	}
	text := string(b)
	if truncated {
		text += "\n\n[truncated by file parser]"
	}
	return text, truncated, nil
}

// readCSVLike parses CSV/TSV into a bounded table sample.
func readCSVLike(path string, maxRows int) (Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return Table{}, err
	}
	defer f.Close()
	return parseCSVLike(f, delimiterForPath(path), maxRows)
}

// parseCSVLike preserves rows and reports how many total data rows existed.
func parseCSVLike(r io.Reader, comma rune, maxRows int) (Table, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.Comma = comma

	records, err := reader.ReadAll()
	if err != nil {
		return Table{}, err
	}
	if len(records) == 0 {
		return Table{}, nil
	}

	table := Table{Header: records[0]}
	totalRows := len(records) - 1
	table.TotalRows = totalRows
	keepRows := min(totalRows, maxRows)
	if keepRows > 0 {
		table.Rows = records[1 : keepRows+1]
	}
	return table, nil
}

// delimiterForPath uses TSV delimiters only when the source declares it.
func delimiterForPath(path string) rune {
	if strings.EqualFold(sourceExt(path), ".tsv") {
		return '\t'
	}
	return ','
}

// sourceName returns a stable source label for trace and citations.
func sourceName(file FileInput) string {
	ref := fileReference(file)
	if ref != "" {
		return ref
	}
	if file.Hint != "" {
		return "inline:" + file.Hint
	}
	return "inline"
}

// mergeMetadata combines caller metadata with parser-produced metadata.
func mergeMetadata(base map[string]string, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// pdfBaseMetadata links every extracted block back to the original PDF source.
func pdfBaseMetadata(base map[string]string, extract PDFExtract) map[string]string {
	metadata := map[string]string{"format": "pdf_body"}
	if extract.PageCount > 0 {
		metadata["page_count"] = strconv.Itoa(extract.PageCount)
	}
	return mergeMetadata(base, metadata)
}

// pdfBodyHint preserves the business context while making the PDF body role explicit.
func pdfBodyHint(file FileInput) string {
	if strings.TrimSpace(file.Hint) == "" {
		return "PDF 主体：抽取 TOC、章节摘要和关键页"
	}
	return file.Hint + "；PDF 主体：抽取 TOC、章节摘要和关键页"
}

// pdfImageHint gives critical page/chart images stable citations for model reasoning.
func pdfImageHint(image PDFImage, index int) string {
	caption := strings.TrimSpace(image.Caption)
	if caption == "" {
		caption = fmt.Sprintf("关键图表 %d", index+1)
	}
	if image.Page > 0 {
		return fmt.Sprintf("图 %d/page %d: %s", index+1, image.Page, caption)
	}
	return fmt.Sprintf("图 %d: %s", index+1, caption)
}

// pdfTableHint gives extracted markdown tables stable citations for fact checks.
func pdfTableHint(table PDFTable, index int) string {
	caption := strings.TrimSpace(table.Caption)
	if caption == "" {
		caption = fmt.Sprintf("抽取表格 %d", index+1)
	}
	if table.Page > 0 {
		return fmt.Sprintf("表 %d/page %d: %s", index+1, table.Page, caption)
	}
	return fmt.Sprintf("表 %d: %s", index+1, caption)
}

// firstNonEmpty keeps metadata fallbacks terse at call sites.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// firstPositive picks a request override before falling back to config.
func firstPositive(v int, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
