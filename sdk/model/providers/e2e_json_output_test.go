//go:build e2e

package providers_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	"github.com/OnslaughtSnail/caelis/sdk/model/providers/e2etest"
)

func TestProviderJSONOutputE2E_DeepSeek(t *testing.T) {
	runProviderJSONOutputE2E(t, "deepseek", "deepseek-v4-flash")
}

func TestProviderJSONOutputE2E_MiMo(t *testing.T) {
	runProviderJSONOutputE2E(t, "xiaomi", "mimo-v2-flash")
}

func TestProviderJSONOutputE2E_MiMoTokenPlanCN(t *testing.T) {
	runProviderJSONOutputE2E(t, "xiaomi-token-plan-cn", "mimo-v2.5-pro")
}

func TestProviderJSONOutputE2E_Ollama(t *testing.T) {
	runProviderJSONOutputE2E(t, "ollama", "qwen3.5:4b")
}

func runProviderJSONOutputE2E(t *testing.T, provider string, defaultModel string) {
	t.Helper()
	t.Setenv("SDK_E2E_PROVIDER", provider)
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: provider,
		DefaultModel:    defaultModel,
		Timeout:         90 * time.Second,
		MaxTokens:       128,
	})
	acceptsXiaomiProvider := provider == "xiaomi-token-plan-cn"
	if !strings.EqualFold(spec.Provider, provider) && !(acceptsXiaomiProvider && strings.EqualFold(spec.Provider, "xiaomi")) {
		t.Fatalf("provider = %q, want %q", spec.Provider, provider)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"status": map[string]any{
				"type": "string",
				"enum": []any{"ok"},
			},
			"provider": map[string]any{"type": "string"},
		},
		"required": []any{"status", "provider"},
	}
	var final *sdkmodel.Response
	for event, err := range spec.LLM.Generate(ctx, &sdkmodel.Request{
		Instructions: []sdkmodel.Part{
			sdkmodel.NewTextPart("Return only valid JSON. Do not include markdown or prose."),
		},
		Messages: []sdkmodel.Message{
			sdkmodel.NewTextMessage(sdkmodel.RoleUser, `Return exactly this JSON object shape with status set to "ok" and provider set to the provider name you are serving: {"status":"ok","provider":"..."}`),
		},
		Output: &sdkmodel.OutputSpec{
			Mode:            sdkmodel.OutputModeSchema,
			JSONSchema:      schema,
			MaxOutputTokens: 128,
		},
		Reasoning: sdkmodel.ReasoningConfig{Effort: "none"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	raw := strings.TrimSpace(final.Message.TextContent())
	var parsed struct {
		Status   string `json:"status"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("assistant content is not valid JSON: %v\ncontent: %s", err, raw)
	}
	if parsed.Status != "ok" {
		t.Fatalf("status = %q, want ok; content: %s", parsed.Status, raw)
	}
}
