package tool

import (
	"fmt"
	"math"
	"testing"
)

func TestCoreToolMetricDefinitionsCoverCoreIDs(t *testing.T) {
	definitions := CoreToolMetricDefinitions()
	if len(definitions) != 8 {
		t.Fatalf("definitions = %d, want 8", len(definitions))
	}

	wantIDs := []ToolMetricID{
		ToolMetricCallPrecision,
		ToolMetricCallRecall,
		ToolMetricWrongToolRate,
		ToolMetricSchemaValidRate,
		ToolMetricEntityBindingAccuracy,
		ToolMetricSuccessRate,
		ToolMetricResultRelevance,
		ToolMetricResultUsedRate,
	}
	for i, want := range wantIDs {
		if definitions[i].ID != want {
			t.Fatalf("definition[%d].ID = %q, want %q", i, definitions[i].ID, want)
		}
	}
}

func TestCoreToolMetricDefinitionsExplainFieldsAndBoundaries(t *testing.T) {
	for _, definition := range CoreToolMetricDefinitions() {
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
			t.Fatalf("%s notes should explain common confusion", definition.ID)
		}
	}
}

func TestScoreCoreToolMetricsCalculatesClosedLoopReport(t *testing.T) {
	report := ScoreCoreToolMetrics([]ToolEvaluationSample{
		{
			TaskID: "task-1",
			ExpectedCalls: []ExpectedToolCall{
				{ID: "lookup_customer", ToolName: "lookup_customer"},
				{ID: "create_ticket", ToolName: "create_ticket"},
			},
			ActualCalls: []ActualToolCall{
				{
					CallID:                "call-1",
					ToolName:              "lookup_customer",
					Necessary:             true,
					CoveredExpectedCallID: "lookup_customer",
					SchemaChecked:         true,
					SchemaValid:           true,
					EntityBindingRequired: true,
					EntityBindingCorrect:  true,
					Executed:              true,
					ExecutionSuccessful:   true,
					ResultHasContent:      true,
					ResultRelevant:        true,
				},
				{
					CallID:              "call-2",
					ToolName:            "read_random_faq",
					SchemaChecked:       true,
					SchemaValid:         true,
					Executed:            true,
					ExecutionSuccessful: true,
					ResultHasContent:    true,
				},
			},
			HasAvailableToolResult:    true,
			FinalAnswerUsedToolResult: true,
		},
		{
			TaskID: "task-2",
			ExpectedCalls: []ExpectedToolCall{
				{ID: "lookup_order", ToolName: "lookup_order"},
			},
			ActualCalls: []ActualToolCall{
				{
					CallID:                "call-3",
					ToolName:              "lookup_customer",
					Necessary:             true,
					WrongTool:             true,
					SchemaChecked:         true,
					EntityBindingRequired: true,
					Executed:              true,
				},
			},
			HasAvailableToolResult: true,
		},
	})

	assertMetricResult(t, report, ToolMetricCallPrecision, 2, 3, 2.0/3.0)
	assertMetricResult(t, report, ToolMetricCallRecall, 1, 3, 1.0/3.0)
	assertMetricResult(t, report, ToolMetricWrongToolRate, 1, 3, 1.0/3.0)
	assertMetricResult(t, report, ToolMetricSchemaValidRate, 2, 3, 2.0/3.0)
	assertMetricResult(t, report, ToolMetricEntityBindingAccuracy, 1, 2, 1.0/2.0)
	assertMetricResult(t, report, ToolMetricSuccessRate, 2, 3, 2.0/3.0)
	assertMetricResult(t, report, ToolMetricResultRelevance, 1, 2, 1.0/2.0)
	assertMetricResult(t, report, ToolMetricResultUsedRate, 1, 2, 1.0/2.0)
}

func TestScoreCoreToolMetricsMarksZeroDenominatorAsNotApplicable(t *testing.T) {
	report := ScoreCoreToolMetrics(nil)

	result, ok := report.Result(ToolMetricCallPrecision)
	if !ok {
		t.Fatalf("missing %s", ToolMetricCallPrecision)
	}
	if result.Status != ToolMetricStatusNotApplicable {
		t.Fatalf("status = %q, want %q", result.Status, ToolMetricStatusNotApplicable)
	}
	if result.Score != 0 {
		t.Fatalf("not applicable score = %.2f, want 0", result.Score)
	}
}

func TestEvaluateToolMetricGateUsesMetricDirection(t *testing.T) {
	report := ScoreCoreToolMetrics([]ToolEvaluationSample{
		{
			TaskID: "task-1",
			ExpectedCalls: []ExpectedToolCall{
				{ID: "lookup_customer", ToolName: "lookup_customer"},
			},
			ActualCalls: []ActualToolCall{
				{
					CallID:                "call-1",
					ToolName:              "lookup_customer",
					Necessary:             true,
					CoveredExpectedCallID: "lookup_customer",
					Executed:              true,
					ExecutionSuccessful:   true,
				},
				{
					CallID:    "call-2",
					ToolName:  "wrong_tool",
					WrongTool: true,
					Executed:  true,
				},
			},
		},
	})

	gate := EvaluateToolMetricGate(report, []ToolMetricThreshold{
		{MetricID: ToolMetricCallPrecision, Threshold: 0.8},
		{MetricID: ToolMetricWrongToolRate, Threshold: 0.6},
	})
	if gate.Passed {
		t.Fatal("gate should fail when higher-is-better metric is below threshold")
	}
	if len(gate.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(gate.Failures))
	}
	if gate.Failures[0].MetricID != ToolMetricCallPrecision {
		t.Fatalf("failure metric = %q, want %q", gate.Failures[0].MetricID, ToolMetricCallPrecision)
	}
}

func TestEvaluateToolMetricGateFailsThresholdWithNotApplicableMetric(t *testing.T) {
	gate := EvaluateToolMetricGate(ScoreCoreToolMetrics(nil), []ToolMetricThreshold{
		{MetricID: ToolMetricCallPrecision, Threshold: 0.8},
	})
	if gate.Passed {
		t.Fatal("gate should fail when threshold metric is not applicable")
	}
	if len(gate.Failures) != 1 || gate.Failures[0].Reason == "" {
		t.Fatalf("failure should explain not applicable metric: %+v", gate.Failures)
	}
}

func TestScoreCoreToolMetricsDerivesWrongToolFromExpectedAcceptableTools(t *testing.T) {
	report := ScoreCoreToolMetrics([]ToolEvaluationSample{
		{
			TaskID: "task-1",
			ExpectedCalls: []ExpectedToolCall{
				{
					ID:              "lookup_order",
					ToolName:        "lookup_order",
					AcceptableTools: []string{"lookup_order", "get_order"},
				},
			},
			ActualCalls: []ActualToolCall{
				{
					CallID:                "call-1",
					ToolName:              "lookup_customer",
					Necessary:             true,
					CoveredExpectedCallID: "lookup_order",
				},
			},
		},
	})

	assertMetricResult(t, report, ToolMetricWrongToolRate, 1, 1, 1)
}

func ExampleScoreCoreToolMetrics() {
	report := ScoreCoreToolMetrics([]ToolEvaluationSample{
		{
			TaskID: "customer-support-demo",
			ExpectedCalls: []ExpectedToolCall{
				{ID: "lookup_customer", ToolName: "lookup_customer"},
			},
			ActualCalls: []ActualToolCall{
				{
					CallID:                "call-1",
					ToolName:              "lookup_customer",
					Necessary:             true,
					CoveredExpectedCallID: "lookup_customer",
					SchemaChecked:         true,
					SchemaValid:           true,
					EntityBindingRequired: true,
					EntityBindingCorrect:  true,
					Executed:              true,
					ExecutionSuccessful:   true,
					ResultHasContent:      true,
					ResultRelevant:        true,
				},
			},
			HasAvailableToolResult:    true,
			FinalAnswerUsedToolResult: true,
		},
	})
	precision, _ := report.Result(ToolMetricCallPrecision)
	gate := EvaluateToolMetricGate(report, []ToolMetricThreshold{
		{MetricID: ToolMetricCallPrecision, Threshold: 0.9},
	})
	fmt.Printf("%s=%.2f passed=%v\n", precision.ID, precision.Score, gate.Passed)
	// Output:
	// tool_call_precision=1.00 passed=true
}

func assertMetricResult(t *testing.T, report ToolMetricReport, metricID ToolMetricID, numerator int, denominator int, score float64) {
	t.Helper()
	result, ok := report.Result(metricID)
	if !ok {
		t.Fatalf("missing %s", metricID)
	}
	if result.Status != ToolMetricStatusScored {
		t.Fatalf("%s status = %q, want %q", metricID, result.Status, ToolMetricStatusScored)
	}
	if result.Numerator != numerator || result.Denominator != denominator {
		t.Fatalf("%s count = %d/%d, want %d/%d", metricID, result.Numerator, result.Denominator, numerator, denominator)
	}
	if math.Abs(result.Score-score) > 0.0001 {
		t.Fatalf("%s score = %.4f, want %.4f", metricID, result.Score, score)
	}
}
