package core

import (
	"context"
	"iter"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestAssemblyResolverAppliesAssemblyStateAndModelDefaults(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, sdksession.SessionRef) (map[string]any, error) {
			return map[string]any{
				sdkplugin.StateCurrentModeID: "plan",
				sdkplugin.StateCurrentConfigValues: map[string]any{
					"sandbox": "workspace_write",
				},
			}, nil
		}),
		Assembly: sdkplugin.ResolvedAssembly{
			Modes: []sdkplugin.ModeConfig{
				{
					ID: "default",
					Runtime: sdkplugin.RuntimeOverrides{
						SystemPrompt: "default prompt",
					},
				},
				{
					ID: "plan",
					Runtime: sdkplugin.RuntimeOverrides{
						SystemPrompt: "plan prompt",
						Reasoning: sdkmodel.ReasoningConfig{
							Effort:       "high",
							BudgetTokens: 64,
						},
					},
				},
			},
			Configs: []sdkplugin.ConfigOption{
				{
					ID:           "sandbox",
					DefaultValue: "read_only",
					Options: []sdkplugin.ConfigSelectOption{
						{Value: "read_only"},
						{Value: "workspace_write", Runtime: sdkplugin.RuntimeOverrides{
							PolicyMode:      "workspace_write",
							ExtraWriteRoots: []string{"/tmp/ws"},
						}},
					},
				},
			},
		},
		DefaultModelAlias: "mini",
		ContextWindow:     16384,
		ModelLookup: modelLookupFunc(func(_ context.Context, alias string, contextWindow int) (ModelResolution, error) {
			if alias != "mini" || contextWindow != 16384 {
				t.Fatalf("ResolveModel(alias=%q, contextWindow=%d)", alias, contextWindow)
			}
			return ModelResolution{
				Model:                  fakeLLM{name: "mini"},
				DefaultReasoningEffort: "medium",
			}, nil
		}),
		BaseMetadata: map[string]any{
			"system_prompt": "base prompt",
		},
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}

	turn, err := resolver.ResolveTurn(context.Background(), TurnIntent{
		SessionRef: sdksession.SessionRef{SessionID: "s1"},
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	meta := turn.RunRequest.AgentSpec.Metadata
	if got := meta["system_prompt"]; got != "plan prompt" {
		t.Fatalf("system_prompt = %#v", got)
	}
	if got := meta["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %#v", got)
	}
	if got := meta["reasoning_budget_tokens"]; got != 64 {
		t.Fatalf("reasoning_budget_tokens = %#v", got)
	}
	if got := meta["policy_mode"]; got != "workspace_write" {
		t.Fatalf("policy_mode = %#v", got)
	}
}

func TestAssemblyResolverSessionReasoningOverridesModeAndModelDefault(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, sdksession.SessionRef) (map[string]any, error) {
			return map[string]any{
				StateCurrentReasoningEffort:  "none",
				sdkplugin.StateCurrentModeID: "plan",
			}, nil
		}),
		Assembly: sdkplugin.ResolvedAssembly{
			Modes: []sdkplugin.ModeConfig{{
				ID: "plan",
				Runtime: sdkplugin.RuntimeOverrides{
					Reasoning: sdkmodel.ReasoningConfig{Effort: "high"},
				},
			}},
		},
		DefaultModelAlias: "mini",
		ModelLookup: modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
			return ModelResolution{
				Model:                  fakeLLM{name: "mini"},
				DefaultReasoningEffort: "medium",
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}
	turn, err := resolver.ResolveTurn(context.Background(), TurnIntent{
		SessionRef: sdksession.SessionRef{SessionID: "s1"},
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	if got := turn.RunRequest.AgentSpec.Metadata["reasoning_effort"]; got != "none" {
		t.Fatalf("reasoning_effort = %#v, want session override none", got)
	}
}

func TestAssemblyResolverIntentModeOverridesState(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, sdksession.SessionRef) (map[string]any, error) {
			return map[string]any{sdkplugin.StateCurrentModeID: "plan"}, nil
		}),
		Assembly: sdkplugin.ResolvedAssembly{
			Modes: []sdkplugin.ModeConfig{
				{ID: "default", Runtime: sdkplugin.RuntimeOverrides{SystemPrompt: "default prompt"}},
				{ID: "plan", Runtime: sdkplugin.RuntimeOverrides{SystemPrompt: "plan prompt"}},
			},
		},
		DefaultModelAlias: "mini",
		ModelLookup: modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
			return ModelResolution{Model: fakeLLM{name: "mini"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}

	turn, err := resolver.ResolveTurn(context.Background(), TurnIntent{
		SessionRef: sdksession.SessionRef{SessionID: "s1"},
		ModeName:   "default",
	})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	if got := turn.RunRequest.AgentSpec.Metadata["system_prompt"]; got != "default prompt" {
		t.Fatalf("system_prompt = %#v", got)
	}
}

func TestAssemblyResolverRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Assembly: sdkplugin.ResolvedAssembly{
			Modes: []sdkplugin.ModeConfig{{ID: "default"}},
		},
		DefaultModelAlias: "mini",
		ModelLookup: modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
			return ModelResolution{Model: fakeLLM{name: "mini"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}

	_, err = resolver.ResolveTurn(context.Background(), TurnIntent{ModeName: "missing"})
	if err == nil {
		t.Fatal("ResolveTurn() error = nil, want invalid request")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeModeNotFound {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
}

func TestCurrentSessionModeMigratesLegacySandboxState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  string
	}{
		{name: "empty defaults to default", state: map[string]any{}, want: "default"},
		{name: "legacy auto becomes default", state: map[string]any{StateCurrentSandboxMode: "auto"}, want: "default"},
		{name: "legacy full control becomes full_access", state: map[string]any{StateCurrentSandboxMode: "full_control"}, want: "full_access"},
		{name: "new key wins", state: map[string]any{StateCurrentSandboxMode: "full_control", StateCurrentSessionMode: "plan"}, want: "plan"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := CurrentSessionMode(tt.state); got != tt.want {
				t.Fatalf("CurrentSessionMode(%#v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

type modelLookupFunc func(context.Context, string, int) (ModelResolution, error)

func (f modelLookupFunc) ResolveModel(ctx context.Context, alias string, contextWindow int) (ModelResolution, error) {
	return f(ctx, alias, contextWindow)
}

type snapshotStateFunc func(context.Context, sdksession.SessionRef) (map[string]any, error)

func (f snapshotStateFunc) SnapshotState(ctx context.Context, ref sdksession.SessionRef) (map[string]any, error) {
	return f(ctx, ref)
}

type fakeLLM struct {
	name string
}

func (f fakeLLM) Name() string { return f.name }
func (f fakeLLM) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(func(*sdkmodel.StreamEvent, error) bool) {}
}
