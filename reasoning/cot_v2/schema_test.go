package cot_v2

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStepDraftListJSONSchemaExposesProductionCOTContract 验证模型输出看到的是 StepDraft 契约，而不是散文式 CoT。
func TestStepDraftListJSONSchemaExposesProductionCOTContract(t *testing.T) {
	schema := StepDraftListJSONSchema()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal schema error = %v", err)
	}
	text := string(raw)

	assertSchemaContains(t, text,
		`"steps"`,
		`"kind"`,
		`"claim_text"`,
		`"suggested_subject"`,
		`"suggested_predicate"`,
		`"suggested_evidence_query"`,
		`"additionalProperties":false`,
		`"observe"`,
		`"decompose"`,
		`"derive"`,
		`"verify"`,
		`"decide"`,
	)
}

func assertSchemaContains(t *testing.T, schema string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema missing %s\nschema=%s", want, schema)
		}
	}
}
