package tool

import (
	"math"
	"testing"
	"time"
)

func TestRealtimeToolMetricDefinitionsCoverRealtimeIDs(t *testing.T) {
	definitions := RealtimeToolMetricDefinitions()
	if len(definitions) != 5 {
		t.Fatalf("definitions = %d, want 5", len(definitions))
	}

	wantIDs := []RealtimeToolMetricID{
		RealtimeToolMetricSchemaValidRate,
		RealtimeToolMetricSuccessRate,
		RealtimeToolMetricTimeoutRate,
		RealtimeToolMetricErrorRate,
		RealtimeToolMetricResultUsedRate,
	}
	for i, want := range wantIDs {
		if definitions[i].ID != want {
			t.Fatalf("definition[%d].ID = %q, want %q", i, definitions[i].ID, want)
		}
	}
}

func TestRealtimeToolMetricDefinitionsExplainFieldsAndBoundaries(t *testing.T) {
	for _, definition := range RealtimeToolMetricDefinitions() {
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

func TestScoreRealtimeToolMetricsCalculatesObservableMetrics(t *testing.T) {
	report := ScoreRealtimeToolMetrics([]ToolCallEvent{
		{
			CallID:              "call-1",
			ToolName:            "lookup_customer",
			SchemaChecked:       true,
			SchemaValid:         true,
			ExecutionStatus:     ToolExecutionStatusSuccess,
			Latency:             100 * time.Millisecond,
			FinalAnswerUsedFact: true,
		},
		{
			CallID:          "call-2",
			ToolName:        "lookup_order",
			SchemaChecked:   true,
			ExecutionStatus: ToolExecutionStatusTimeout,
			ErrorType:       "timeout",
			Latency:         900 * time.Millisecond,
		},
		{
			CallID:          "call-3",
			ToolName:        "create_ticket",
			SchemaChecked:   true,
			SchemaValid:     true,
			ExecutionStatus: ToolExecutionStatusError,
			ErrorType:       "permission_denied",
			Latency:         300 * time.Millisecond,
		},
	})

	assertRealtimeMetric(t, report, RealtimeToolMetricSchemaValidRate, 2, 3, 2.0/3.0)
	assertRealtimeMetric(t, report, RealtimeToolMetricSuccessRate, 1, 3, 1.0/3.0)
	assertRealtimeMetric(t, report, RealtimeToolMetricTimeoutRate, 1, 3, 1.0/3.0)
	assertRealtimeMetric(t, report, RealtimeToolMetricResultUsedRate, 1, 1, 1)

	errorRate, ok := report.Result(RealtimeToolMetricErrorRate)
	if !ok {
		t.Fatalf("missing %s", RealtimeToolMetricErrorRate)
	}
	if errorRate.Numerator != 2 || errorRate.Denominator != 3 {
		t.Fatalf("error rate count = %d/%d, want 2/3", errorRate.Numerator, errorRate.Denominator)
	}
	if report.ErrorCountByType["timeout"] != 1 || report.ErrorCountByType["permission_denied"] != 1 {
		t.Fatalf("error counts = %+v", report.ErrorCountByType)
	}
	if report.LatencyP95 != 900*time.Millisecond {
		t.Fatalf("latency p95 = %s, want 900ms", report.LatencyP95)
	}
	if errorRate.Definition.ID != RealtimeToolMetricErrorRate {
		t.Fatalf("error rate definition id = %q, want %q", errorRate.Definition.ID, RealtimeToolMetricErrorRate)
	}
}

func TestScoreRealtimeToolMetricsMarksMissingDenominatorNotApplicable(t *testing.T) {
	report := ScoreRealtimeToolMetrics(nil)

	result, ok := report.Result(RealtimeToolMetricSchemaValidRate)
	if !ok {
		t.Fatalf("missing %s", RealtimeToolMetricSchemaValidRate)
	}
	if result.Status != RealtimeToolMetricStatusNotApplicable {
		t.Fatalf("status = %q, want %q", result.Status, RealtimeToolMetricStatusNotApplicable)
	}
}

func assertRealtimeMetric(t *testing.T, report RealtimeToolMetricReport, metricID RealtimeToolMetricID, numerator int, denominator int, score float64) {
	t.Helper()
	result, ok := report.Result(metricID)
	if !ok {
		t.Fatalf("missing %s", metricID)
	}
	if result.Status != RealtimeToolMetricStatusScored {
		t.Fatalf("%s status = %q, want scored", metricID, result.Status)
	}
	if result.Numerator != numerator || result.Denominator != denominator {
		t.Fatalf("%s count = %d/%d, want %d/%d", metricID, result.Numerator, result.Denominator, numerator, denominator)
	}
	if math.Abs(result.Score-score) > 0.0001 {
		t.Fatalf("%s score = %.4f, want %.4f", metricID, result.Score, score)
	}
}
