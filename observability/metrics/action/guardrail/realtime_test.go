package guardrail

import (
	"math"
	"testing"
	"time"

	runtimeguardrail "ai-designing/action/guardrail"
)

func TestRealtimeGuardrailMetricDefinitionsCoverRealtimeIDs(t *testing.T) {
	definitions := RealtimeGuardrailMetricDefinitions()
	if len(definitions) != 5 {
		t.Fatalf("definitions = %d, want 5", len(definitions))
	}

	wantIDs := []RealtimeGuardrailMetricID{
		RealtimeGuardrailMetricTriggerRate,
		RealtimeGuardrailMetricBlockRate,
		RealtimeGuardrailMetricFallbackRate,
		RealtimeGuardrailMetricErrorRate,
		RealtimeGuardrailMetricUserRetryOrEscalationRate,
	}
	for i, want := range wantIDs {
		if definitions[i].ID != want {
			t.Fatalf("definition[%d].ID = %q, want %q", i, definitions[i].ID, want)
		}
	}
}

func TestRealtimeGuardrailMetricDefinitionsExplainFieldsAndBoundaries(t *testing.T) {
	for _, definition := range RealtimeGuardrailMetricDefinitions() {
		if definition.DisplayName == "" {
			t.Fatalf("%s display name is empty", definition.ID)
		}
		if definition.Meaning == "" {
			t.Fatalf("%s meaning is empty", definition.ID)
		}
		if definition.Numerator == "" || definition.Denominator == "" {
			t.Fatalf("%s numerator/denominator should be documented", definition.ID)
		}
		if definition.Formula == "" {
			t.Fatalf("%s formula should be documented", definition.ID)
		}
		if len(definition.RequiredFields) == 0 {
			t.Fatalf("%s required fields should be documented", definition.ID)
		}
		if len(definition.Notes) == 0 {
			t.Fatalf("%s notes should explain realtime boundary", definition.ID)
		}
	}
}

func TestScoreRealtimeGuardrailMetricsCalculatesObservableMetrics(t *testing.T) {
	report := ScoreRealtimeGuardrailMetrics([]GuardrailDecisionEvent{
		{
			DecisionID:             "decision-1",
			Stage:                  GuardrailStageInput,
			Triggered:              true,
			Outcome:                GuardrailOutcomeBlock,
			RiskLevel:              runtimeguardrail.RiskLevelHigh,
			Categories:             []string{"jailbreak"},
			Latency:                100 * time.Millisecond,
			UserRetriedOrEscalated: true,
		},
		{
			DecisionID: "decision-2",
			Stage:      GuardrailStageOutput,
			Triggered:  true,
			Outcome:    GuardrailOutcomeFallback,
			RiskLevel:  runtimeguardrail.RiskLevelMedium,
			Categories: []string{"pii"},
			Latency:    300 * time.Millisecond,
		},
		{
			DecisionID: "decision-3",
			Stage:      GuardrailStageTool,
			Outcome:    GuardrailOutcomeAllow,
			RiskLevel:  runtimeguardrail.RiskLevelLow,
			Latency:    50 * time.Millisecond,
		},
		{
			DecisionID: "decision-4",
			Stage:      GuardrailStageTool,
			Triggered:  true,
			Outcome:    GuardrailOutcomeError,
			RiskLevel:  runtimeguardrail.RiskLevelHigh,
			Categories: []string{"tool_injection"},
			ErrorType:  "classifier_timeout",
			Latency:    900 * time.Millisecond,
		},
	})

	assertRealtimeGuardrailMetric(t, report, RealtimeGuardrailMetricTriggerRate, 3, 4, 3.0/4.0)
	assertRealtimeGuardrailMetric(t, report, RealtimeGuardrailMetricBlockRate, 1, 4, 1.0/4.0)
	assertRealtimeGuardrailMetric(t, report, RealtimeGuardrailMetricFallbackRate, 1, 4, 1.0/4.0)
	assertRealtimeGuardrailMetric(t, report, RealtimeGuardrailMetricErrorRate, 1, 4, 1.0/4.0)
	assertRealtimeGuardrailMetric(t, report, RealtimeGuardrailMetricUserRetryOrEscalationRate, 1, 2, 1.0/2.0)

	if report.CategoryCountByName["jailbreak"] != 1 || report.CategoryCountByName["pii"] != 1 || report.CategoryCountByName["tool_injection"] != 1 {
		t.Fatalf("category counts = %+v", report.CategoryCountByName)
	}
	if report.RiskLevelCount[runtimeguardrail.RiskLevelHigh] != 2 {
		t.Fatalf("risk level counts = %+v", report.RiskLevelCount)
	}
	if report.ErrorCountByType["classifier_timeout"] != 1 {
		t.Fatalf("error counts = %+v", report.ErrorCountByType)
	}
	if report.LatencyP95 != 900*time.Millisecond {
		t.Fatalf("latency p95 = %s, want 900ms", report.LatencyP95)
	}
}

func TestScoreRealtimeGuardrailMetricsMarksMissingDenominatorNotApplicable(t *testing.T) {
	report := ScoreRealtimeGuardrailMetrics(nil)

	result, ok := report.Result(RealtimeGuardrailMetricTriggerRate)
	if !ok {
		t.Fatalf("missing %s", RealtimeGuardrailMetricTriggerRate)
	}
	if result.Status != RealtimeGuardrailMetricStatusNotApplicable {
		t.Fatalf("status = %q, want %q", result.Status, RealtimeGuardrailMetricStatusNotApplicable)
	}
}

func assertRealtimeGuardrailMetric(t *testing.T, report RealtimeGuardrailMetricReport, metricID RealtimeGuardrailMetricID, numerator int, denominator int, score float64) {
	t.Helper()
	result, ok := report.Result(metricID)
	if !ok {
		t.Fatalf("missing %s", metricID)
	}
	if result.Status != RealtimeGuardrailMetricStatusScored {
		t.Fatalf("%s status = %q, want scored", metricID, result.Status)
	}
	if result.Numerator != numerator || result.Denominator != denominator {
		t.Fatalf("%s count = %d/%d, want %d/%d", metricID, result.Numerator, result.Denominator, numerator, denominator)
	}
	if math.Abs(result.Score-score) > 0.0001 {
		t.Fatalf("%s score = %.4f, want %.4f", metricID, result.Score, score)
	}
}
