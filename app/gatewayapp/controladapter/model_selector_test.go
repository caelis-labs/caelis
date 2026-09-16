package controladapter

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func modelSelectorDriver() *assembler {
	var choices []ModelChoice
	for _, cfg := range []modelconfig.Config{
		{Provider: "deepseek", Model: "deepseek-v4-flash"},
		{Provider: "xiaomi", EndpointID: "api-cn", Model: "mimo-v2.5-pro"},
		{Provider: "xiaomi", EndpointID: "token-plan-cn", Model: "mimo-v2.5-pro"},
	} {
		choices = append(choices, modelconfig.ChoiceFromConfig(modelconfig.NormalizeConfig(cfg)))
	}
	choices = append(choices, ModelChoice{
		ID: "acp:codex:default", Alias: "Codex — Sol", Backend: "acp",
		Provider: "codex", Model: "default",
	})
	return newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{
		ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) {
			return choices, nil
		},
	}}, "", "")
}

func TestCompleteSlashArgModelPublishesPublicSelectors(t *testing.T) {
	driver := modelSelectorDriver()
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"deepseek/deepseek-v4-flash", "xiaomi@api-cn/mimo-v2.5-pro", "xiaomi@token-plan-cn/mimo-v2.5-pro", "acp:codex:default"}},
		{"deep", []string{"deepseek/deepseek-v4-flash"}},
		{"xiaomi@api", []string{"xiaomi@api-cn/mimo-v2.5-pro"}},
		{"xiaomi@token", []string{"xiaomi@token-plan-cn/mimo-v2.5-pro"}},
		{"acp:codex", []string{"acp:codex:default"}},
		{"xiaomi/mimo", []string{"xiaomi@api-cn/mimo-v2.5-pro", "xiaomi@token-plan-cn/mimo-v2.5-pro"}},
		{"nope", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			candidates, err := driver.CompleteSlashArg(context.Background(), "model", tt.query, 20)
			if err != nil {
				t.Fatal(err)
			}
			if got := slashCandidateValues(candidates); !slices.Equal(got, tt.want) {
				t.Fatalf("candidates = %v, want %v", got, tt.want)
			}
			for _, candidate := range candidates {
				switch candidate.Value {
				case "xiaomi@api-cn/mimo-v2.5-pro", "xiaomi@token-plan-cn/mimo-v2.5-pro":
					if candidate.Display != "xiaomi/mimo-v2.5-pro" {
						t.Fatalf("provider display = %q", candidate.Display)
					}
				case "acp:codex:default":
					if candidate.Display != "Codex — Sol" {
						t.Fatalf("ACP display = %q", candidate.Display)
					}
				}
			}
		})
	}
}

func TestCompleteSlashArgModelPublishesDurableModelConfigIDs(t *testing.T) {
	driver := modelSelectorDriver()
	candidates, err := driver.CompleteSlashArg(context.Background(), "model", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	// Value stays the user-facing public selector; ModelConfigID carries the
	// durable config identity for provider endpoints (default and non-default).
	// ACP choices omit it because their ID is a ModelProfile ID, not a
	// modelconfig identity.
	want := map[string]string{
		"deepseek/deepseek-v4-flash":         "deepseek@default/deepseek/deepseek-v4-flash",
		"xiaomi@api-cn/mimo-v2.5-pro":        "xiaomi@api-cn/xiaomi/mimo-v2.5-pro",
		"xiaomi@token-plan-cn/mimo-v2.5-pro": "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro",
		"acp:codex:default":                  "",
	}
	if len(candidates) != len(want) {
		t.Fatalf("candidate count = %d, want %d", len(candidates), len(want))
	}
	for _, candidate := range candidates {
		expected, ok := want[candidate.Value]
		if !ok {
			t.Fatalf("unexpected candidate %q", candidate.Value)
		}
		if candidate.ModelConfigID != expected {
			t.Fatalf("candidate %q model config ID = %q, want %q", candidate.Value, candidate.ModelConfigID, expected)
		}
	}
}

func TestResolveStoredModelAliasUsesSharedSelectors(t *testing.T) {
	driver := modelSelectorDriver()
	tests := []struct{ input, want string }{
		{"deepseek/deepseek-v4-flash", "deepseek@default/deepseek/deepseek-v4-flash"},
		{"deepseek@default/deepseek/deepseek-v4-flash", "deepseek@default/deepseek/deepseek-v4-flash"},
		{"xiaomi@api-cn/mimo-v2.5-pro", "xiaomi@api-cn/xiaomi/mimo-v2.5-pro"},
		{"xiaomi@token-plan-cn/mimo-v2.5-pro", "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro"},
		{"xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro", "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro"},
		{"xiaomi@api-cn/mimo", "xiaomi@api-cn/xiaomi/mimo-v2.5-pro"},
		{"acp:codex:default", "acp:codex:default"},
	}
	for _, tt := range tests {
		got, err := driver.resolveStoredModelAlias(context.Background(), tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("resolve(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
	for _, input := range []string{"xiaomi/mimo-v2.5-pro", "xiaomi"} {
		if _, err := driver.resolveStoredModelAlias(context.Background(), input); !errors.Is(err, modelconfig.ErrAmbiguousSelector) {
			t.Fatalf("ambiguous selector %q error = %v", input, err)
		}
	}
}
