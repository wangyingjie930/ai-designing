package cot_v2

import "github.com/eino-contrib/jsonschema"

// StepDraftListJSONSchema 生成给模型使用的 JSON Schema，避免 prompt 和 Go 结构体长期漂移。
func StepDraftListJSONSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             true,
	}
	schema := reflector.Reflect(&StepDraftList{})
	patchOpenAIStepDraftSchema(schema)
	return schema
}

func patchOpenAIStepDraftSchema(schema *jsonschema.Schema) {
	if schema == nil || schema.Properties == nil {
		return
	}
	steps, ok := schema.Properties.Get("steps")
	if !ok || steps == nil || steps.Items == nil || steps.Items.Properties == nil {
		return
	}
	stepSchema := steps.Items
	stepSchema.Required = appendMissingRequired(stepSchema.Required, []string{
		"kind",
		"claim_text",
		"suggested_subject",
		"suggested_predicate",
		"suggested_object",
		"suggested_evidence_query",
	})
	patchNullableStringProperty(stepSchema, "suggested_subject")
	patchNullableStringProperty(stepSchema, "suggested_predicate")
	patchNullableStringProperty(stepSchema, "suggested_evidence_query")
	patchSuggestedObjectProperty(stepSchema)
}

func patchNullableStringProperty(stepSchema *jsonschema.Schema, name string) {
	property, ok := stepSchema.Properties.Get(name)
	if !ok || property == nil {
		return
	}
	property.Type = ""
	property.TypeEnhanced = []string{"string", "null"}
}

func patchSuggestedObjectProperty(stepSchema *jsonschema.Schema) {
	suggestedObject, ok := stepSchema.Properties.Get("suggested_object")
	if !ok || suggestedObject == nil {
		return
	}

	// suggested_object 在 Go 里是 any，反射库会生成没有 type 的空 schema；
	// OpenAI strict response_format 不允许这里暴露自由 object/array，复杂结构交给证据层返回。
	suggestedObject.Type = ""
	suggestedObject.TypeEnhanced = []string{"string", "number", "integer", "boolean", "null"}
}

func appendMissingRequired(existing []string, required []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(required))
	for _, name := range existing {
		seen[name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := seen[name]; ok {
			continue
		}
		existing = append(existing, name)
		seen[name] = struct{}{}
	}
	return existing
}
