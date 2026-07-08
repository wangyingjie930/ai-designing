package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	cotv2 "ai-designing/reasoning/cot_v2"
)

type promptEvidenceCallback struct {
	chunks []promptEvidenceChunk
}

type promptEvidenceChunk struct {
	Index       int
	Text        string
	SourceID    string
	ContentHash string
}

func newPromptEvidenceCallback(question string) evidenceCallback {
	callback := promptEvidenceCallback{chunks: buildPromptEvidenceChunks(question)}
	return callback.Resolve
}

// buildPromptEvidenceChunks 把本次任务输入转换为本地检索语料；EvidenceRef 指向输入片段，不伪造外部 DB 证据。
func buildPromptEvidenceChunks(question string) []promptEvidenceChunk {
	normalized := strings.TrimSpace(question)
	if normalized == "" {
		return nil
	}
	rawChunks := []string{normalized}
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*• \t")
		if line != "" && line != normalized {
			rawChunks = append(rawChunks, line)
		}
	}
	chunks := make([]promptEvidenceChunk, 0, len(rawChunks))
	for index, text := range rawChunks {
		hash := shortHash(text)
		chunks = append(chunks, promptEvidenceChunk{
			Index:       index,
			Text:        text,
			SourceID:    fmt.Sprintf("user_prompt:%s:chunk:%d", hash, index),
			ContentHash: "sha256:" + fullHash(text),
		})
	}
	return chunks
}

func (p promptEvidenceCallback) Resolve(_ context.Context, req evidenceCallbackRequest) (evidenceCallbackResponse, error) {
	switch req.Source {
	case evidenceSourceRetrieval:
		chunk, ok := p.bestChunk(req)
		if !ok {
			return evidenceCallbackResponse{}, nil
		}
		return evidenceCallbackResponse{EvidenceRefs: []cotv2.EvidenceRef{{
			SourceID:    chunk.SourceID,
			SourceType:  "user_prompt",
			Version:     fmt.Sprintf("chunk-%d", chunk.Index),
			ContentHash: chunk.ContentHash,
		}}}, nil
	case evidenceSourceLog:
		return evidenceCallbackResponse{}, nil
	case evidenceSourceTool:
		if len(req.PriorEvidenceRefs) == 0 {
			return evidenceCallbackResponse{}, fmt.Errorf("tool callback requires prior evidence refs for predicate %q", req.Step.Predicate)
		}
		return evidenceCallbackResponse{
			Action: firstNonEmpty(strings.TrimSpace(req.Step.EvidenceQuery), "local_evidence_tool"),
			EvidenceRefs: []cotv2.EvidenceRef{{
				SourceID:    "tool_result:" + shortHash(toolEvidenceKey(req)),
				SourceType:  "tool_result",
				Version:     "local",
				ContentHash: "sha256:" + fullHash(toolEvidenceKey(req)),
			}},
		}, nil
	default:
		return evidenceCallbackResponse{}, nil
	}
}

func (p promptEvidenceCallback) bestChunk(req evidenceCallbackRequest) (promptEvidenceChunk, bool) {
	if len(p.chunks) == 0 {
		return promptEvidenceChunk{}, false
	}
	query := strings.Join([]string{
		req.Step.ClaimText,
		req.Step.Subject,
		req.Step.Predicate,
		req.Step.EvidenceQuery,
	}, " ")
	queryTerms := significantTerms(query)
	bestIndex := 0
	bestScore := -1
	for index, chunk := range p.chunks {
		score := overlapScore(queryTerms, significantTerms(chunk.Text))
		if score > bestScore {
			bestIndex = index
			bestScore = score
		}
	}
	if bestScore <= 0 {
		return promptEvidenceChunk{}, false
	}
	return p.chunks[bestIndex], true
}

var asciiTokenPattern = regexp.MustCompile(`[a-zA-Z0-9_:%.-]+`)

func significantTerms(text string) map[string]struct{} {
	normalized := strings.ToLower(strings.TrimSpace(text))
	terms := make(map[string]struct{})
	for _, token := range asciiTokenPattern.FindAllString(normalized, -1) {
		if len([]rune(token)) >= 2 {
			terms[token] = struct{}{}
		}
	}
	cjk := make([]rune, 0, len(normalized))
	for _, value := range normalized {
		if unicode.Is(unicode.Han, value) {
			cjk = append(cjk, value)
		}
	}
	for size := 2; size <= 4; size++ {
		for index := 0; index+size <= len(cjk); index++ {
			terms[string(cjk[index:index+size])] = struct{}{}
		}
	}
	return terms
}

func overlapScore(left map[string]struct{}, right map[string]struct{}) int {
	score := 0
	for term := range left {
		if _, ok := right[term]; ok {
			score++
		}
	}
	return score
}

func toolEvidenceKey(req evidenceCallbackRequest) string {
	parts := []string{req.Step.StepID, req.Step.Predicate, req.Step.EvidenceQuery}
	for _, ref := range req.PriorEvidenceRefs {
		parts = append(parts, ref.SourceType, ref.SourceID, ref.Version, ref.ContentHash)
	}
	return strings.Join(parts, "|")
}

func shortHash(text string) string {
	return fullHash(text)[:12]
}

func fullHash(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}
