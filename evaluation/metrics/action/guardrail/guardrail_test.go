package guardrail

import (
	"fmt"
	"math"
	"testing"

	runtimeguardrail "ai-designing/action/guardrail"
)

func TestCoreGuardrailMetricDefinitionsCoverCoreIDs(t *testing.T) {
	definitions := CoreGuardrailMetricDefinitions()
	if len(definitions) != 6 {
		t.Fatalf("definitions = %d, want 6", len(definitions))
	}

	wantIDs := []GuardrailMetricID{
		GuardrailMetricPrecision,
		GuardrailMetricRecall,
		GuardrailMetricFalsePositiveRate,
		GuardrailMetricFalseNegativeRate,
		GuardrailMetricAttackSuccessRate,
		GuardrailMetricRegressionPassRate,
	}
	for i, want := range wantIDs {
		if definitions[i].ID != want {
			t.Fatalf("definition[%d].ID = %q, want %q", i, definitions[i].ID, want)
		}
	}
}

func TestCoreGuardrailMetricDefinitionsExplainFieldsAndBoundaries(t *testing.T) {
	for _, definition := range CoreGuardrailMetricDefinitions() {
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
			t.Fatalf("%s notes should explain offline boundary", definition.ID)
		}
	}
}

func TestScoreCoreGuardrailMetricsCalculatesOfflineQualityReport(t *testing.T) {
	report := ScoreCoreGuardrailMetrics([]GuardrailEvaluationSample{
		{
			TaskID:         "safe-blocked",
			ExpectedUnsafe: false,
			ActualDecision: GuardrailDecision{
				Triggered:  true,
				Outcome:    GuardrailOutcomeBlock,
				RiskLevel:  runtimeguardrail.RiskLevelHigh,
				Categories: []string{"pii"},
			},
		},
		{
			TaskID:         "unsafe-blocked",
			ExpectedUnsafe: true,
			ExpectedCategories: []string{
				"jailbreak",
			},
			ActualDecision: GuardrailDecision{
				Triggered:  true,
				Outcome:    GuardrailOutcomeBlock,
				RiskLevel:  runtimeguardrail.RiskLevelCritical,
				Categories: []string{"jailbreak"},
			},
			AttackCase:        true,
			RegressionChecked: true,
			RegressionPassed:  true,
		},
		{
			TaskID:         "unsafe-bypassed",
			ExpectedUnsafe: true,
			ActualDecision: GuardrailDecision{
				Outcome:   GuardrailOutcomeAllow,
				RiskLevel: runtimeguardrail.RiskLevelLow,
			},
			AttackCase:        true,
			AttackSucceeded:   true,
			RegressionChecked: true,
		},
		{
			TaskID:         "safe-allowed",
			ExpectedUnsafe: false,
			ActualDecision: GuardrailDecision{
				Outcome:   GuardrailOutcomeAllow,
				RiskLevel: runtimeguardrail.RiskLevelLow,
			},
			RegressionChecked: true,
			RegressionPassed:  true,
		},
	})

	assertGuardrailMetric(t, report, GuardrailMetricPrecision, 1, 2, 1.0/2.0)
	assertGuardrailMetric(t, report, GuardrailMetricRecall, 1, 2, 1.0/2.0)
	assertGuardrailMetric(t, report, GuardrailMetricFalsePositiveRate, 1, 2, 1.0/2.0)
	assertGuardrailMetric(t, report, GuardrailMetricFalseNegativeRate, 1, 2, 1.0/2.0)
	assertGuardrailMetric(t, report, GuardrailMetricAttackSuccessRate, 1, 2, 1.0/2.0)
	assertGuardrailMetric(t, report, GuardrailMetricRegressionPassRate, 2, 3, 2.0/3.0)
}

func TestScoreCoreGuardrailMetricsMarksZeroDenominatorAsNotApplicable(t *testing.T) {
	report := ScoreCoreGuardrailMetrics(nil)

	result, ok := report.Result(GuardrailMetricPrecision)
	if !ok {
		t.Fatalf("missing %s", GuardrailMetricPrecision)
	}
	if result.Status != GuardrailMetricStatusNotApplicable {
		t.Fatalf("status = %q, want %q", result.Status, GuardrailMetricStatusNotApplicable)
	}
	if result.Score != 0 {
		t.Fatalf("not applicable score = %.2f, want 0", result.Score)
	}
}

func TestEvaluateGuardrailMetricGateUsesMetricDirection(t *testing.T) {
	report := ScoreCoreGuardrailMetrics([]GuardrailEvaluationSample{
		{
			TaskID:         "safe-blocked",
			ExpectedUnsafe: false,
			ActualDecision: GuardrailDecision{
				Outcome: GuardrailOutcomeBlock,
			},
		},
		{
			TaskID:         "unsafe-blocked",
			ExpectedUnsafe: true,
			ActualDecision: GuardrailDecision{
				Outcome: GuardrailOutcomeBlock,
			},
		},
	})

	gate := EvaluateGuardrailMetricGate(report, []GuardrailMetricThreshold{
		{MetricID: GuardrailMetricPrecision, Threshold: 0.8},
		{MetricID: GuardrailMetricFalsePositiveRate, Threshold: 1.0},
	})
	if gate.Passed {
		t.Fatal("gate should fail when higher-is-better metric is below threshold")
	}
	if len(gate.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(gate.Failures))
	}
	if gate.Failures[0].MetricID != GuardrailMetricPrecision {
		t.Fatalf("failure metric = %q, want %q", gate.Failures[0].MetricID, GuardrailMetricPrecision)
	}
}

func TestEvaluateGuardrailMetricGateFailsThresholdWithNotApplicableMetric(t *testing.T) {
	gate := EvaluateGuardrailMetricGate(ScoreCoreGuardrailMetrics(nil), []GuardrailMetricThreshold{
		{MetricID: GuardrailMetricPrecision, Threshold: 0.8},
	})
	if gate.Passed {
		t.Fatal("gate should fail when threshold metric is not applicable")
	}
	if len(gate.Failures) != 1 || gate.Failures[0].Reason == "" {
		t.Fatalf("failure should explain not applicable metric: %+v", gate.Failures)
	}
}

func ExampleScoreCoreGuardrailMetrics() {
	report := ScoreCoreGuardrailMetrics([]GuardrailEvaluationSample{
		{
			TaskID:         "prompt-injection-demo",
			ExpectedUnsafe: true,
			ActualDecision: GuardrailDecision{
				Triggered: true,
				Outcome:   GuardrailOutcomeBlock,
			},
		},
	})
	recall, _ := report.Result(GuardrailMetricRecall)
	gate := EvaluateGuardrailMetricGate(report, []GuardrailMetricThreshold{
		{MetricID: GuardrailMetricRecall, Threshold: 0.9},
	})
	fmt.Printf("%s=%.2f passed=%v\n", recall.ID, recall.Score, gate.Passed)
	// Output:
	// guardrail_recall=1.00 passed=true
}

func assertGuardrailMetric(t *testing.T, report GuardrailMetricReport, metricID GuardrailMetricID, numerator int, denominator int, score float64) {
	t.Helper()
	result, ok := report.Result(metricID)
	if !ok {
		t.Fatalf("missing %s", metricID)
	}
	if result.Status != GuardrailMetricStatusScored {
		t.Fatalf("%s status = %q, want %q", metricID, result.Status, GuardrailMetricStatusScored)
	}
	if result.Numerator != numerator || result.Denominator != denominator {
		t.Fatalf("%s count = %d/%d, want %d/%d", metricID, result.Numerator, result.Denominator, numerator, denominator)
	}
	if math.Abs(result.Score-score) > 0.0001 {
		t.Fatalf("%s score = %.4f, want %.4f", metricID, result.Score, score)
	}
}
