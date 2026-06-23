package providers

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

func TestAnthropicBuildRequestIncludesStructuredOutputFormat(t *testing.T) {
	llm := newAnthropic(Config{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "anthropic-token",
		},
	}, "anthropic-token")
	typed, ok := llm.(*anthropicSDKLLM)
	if !ok {
		t.Fatalf("newAnthropic() = %T, want *anthropicSDKLLM", llm)
	}

	params, err := typed.buildRequest(&model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "return json")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome": map[string]any{"type": "string"},
				},
			},
			MaxOutputTokens: 64,
		},
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}

	payload := marshalAnthropicParamsForTest(t, params)
	if got := payload["max_tokens"]; got != float64(64) {
		t.Fatalf("max_tokens = %#v, want 64", got)
	}
	format := nestedMapForTest(t, payload, "output_config", "format")
	if got := format["type"]; got != "json_schema" {
		t.Fatalf("output_config.format.type = %#v, want json_schema", got)
	}
	schema := nestedMapForTest(t, payload, "output_config", "format", "schema")
	if got := schema["type"]; got != "object" {
		t.Fatalf("output_config.format.schema.type = %#v, want object", got)
	}
}

func TestAnthropicBuildRequestUsesObjectSchemaForJSONMode(t *testing.T) {
	llm := newAnthropic(Config{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "anthropic-token",
		},
	}, "anthropic-token").(*anthropicSDKLLM)

	params, err := llm.buildRequest(&model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "return json")},
		Output:   &model.OutputSpec{Mode: model.OutputModeJSON},
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}

	payload := marshalAnthropicParamsForTest(t, params)
	schema := nestedMapForTest(t, payload, "output_config", "format", "schema")
	if got := schema["type"]; got != "object" {
		t.Fatalf("output_config.format.schema.type = %#v, want default object schema", got)
	}
}

func TestDeepSeekAnthropicBuildRequestCarriesStructuredOutputFormat(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")
	typed, ok := llm.(*anthropicSDKLLM)
	if !ok {
		t.Fatalf("newDeepSeek() = %T, want *anthropicSDKLLM", llm)
	}

	params, err := typed.buildRequest(&model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "return json")},
		Output: &model.OutputSpec{
			Mode:       model.OutputModeJSON,
			JSONSchema: map[string]any{"type": "object"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}

	payload := marshalAnthropicParamsForTest(t, params)
	format := nestedMapForTest(t, payload, "output_config", "format")
	if got := format["type"]; got != "json_schema" {
		t.Fatalf("output_config.format.type = %#v, want json_schema", got)
	}
}

func TestDeepSeekAnthropicBuildRequestRaisesMaxTokensAboveThinkingBudget(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token").(*anthropicSDKLLM)

	params, err := llm.buildRequest(&model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "search")},
		Output:   &model.OutputSpec{MaxOutputTokens: 512},
		Reasoning: model.ReasoningConfig{
			Effort: "high",
		},
	})
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}

	payload := marshalAnthropicParamsForTest(t, params)
	thinking := nestedMapForTest(t, payload, "thinking")
	if got := thinking["budget_tokens"]; got != float64(8192) {
		t.Fatalf("thinking.budget_tokens = %#v, want high budget", got)
	}
	if got := payload["max_tokens"]; got != float64(8193) {
		t.Fatalf("max_tokens = %#v, want budget+1", got)
	}
}

func marshalAnthropicParamsForTest(t *testing.T, params any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal params: %v\n%s", err, raw)
	}
	return payload
}

func nestedMapForTest(t *testing.T, values map[string]any, path ...string) map[string]any {
	t.Helper()
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("path %v parent = %#v, want map", path, current)
		}
		current = mapped[key]
	}
	mapped, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("path %v = %#v, want map", path, current)
	}
	return mapped
}
