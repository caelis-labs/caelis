package kernel

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestAssemblyResolverAppliesAssemblyStateAndModelDefaults(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) {
			return map[string]any{
				assembly.StateCurrentModeID: "plan",
				assembly.StateCurrentConfigValues: map[string]any{
					"sandbox": "workspace_write",
				},
			}, nil
		}),
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{
				{
					ID: "default",
					Runtime: assembly.RuntimeOverrides{
						SystemPrompt: "default prompt",
					},
				},
				{
					ID: "plan",
					Runtime: assembly.RuntimeOverrides{
						SystemPrompt: "plan prompt",
						Reasoning: model.ReasoningConfig{
							Effort:       "high",
							BudgetTokens: 64,
						},
					},
				},
			},
			Configs: []assembly.ConfigOption{
				{
					ID:           "sandbox",
					DefaultValue: "read_only",
					Options: []assembly.ConfigSelectOption{
						{Value: "read_only"},
						{Value: "workspace_write", Runtime: assembly.RuntimeOverrides{
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
		SessionRef: session.SessionRef{SessionID: "s1"},
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
	if got := meta[policyapi.MetadataPolicyProfile]; got != policyapi.ProfileWorkspaceWrite {
		t.Fatalf("policy_profile = %#v", got)
	}
}

func TestAssemblyResolverSessionReasoningOverridesModeAndModelDefault(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) {
			return map[string]any{
				StateCurrentReasoningEffort: "none",
				assembly.StateCurrentModeID: "plan",
			}, nil
		}),
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{{
				ID: "plan",
				Runtime: assembly.RuntimeOverrides{
					Reasoning: model.ReasoningConfig{Effort: "high"},
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
		SessionRef: session.SessionRef{SessionID: "s1"},
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	if got := turn.RunRequest.AgentSpec.Metadata["reasoning_effort"]; got != "none" {
		t.Fatalf("reasoning_effort = %#v, want session override none", got)
	}
}

func TestAssemblyResolverRejectsLegacySessionState(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) {
			return map[string]any{"gateway.current_session_mode": "manual"}, nil
		}),
		ModelLookup: modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
			t.Fatal("ResolveModel should not be called for legacy session state")
			return ModelResolution{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}

	_, err = resolver.ResolveTurn(context.Background(), TurnIntent{SessionRef: session.SessionRef{SessionID: "s1"}})
	if !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("ResolveTurn() error = %v, want ErrUnsupportedLegacyFormat", err)
	}
	if err == nil || !strings.Contains(err.Error(), "gateway.current_session_mode") {
		t.Fatalf("ResolveTurn() error = %v, want legacy key detail", err)
	}

	_, err = resolver.ResolveControllerTurn(context.Background(), TurnIntent{SessionRef: session.SessionRef{SessionID: "s1"}})
	if !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("ResolveControllerTurn() error = %v, want ErrUnsupportedLegacyFormat", err)
	}
}

func TestAssemblyResolverDoesNotInventPolicyProfile(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) {
			return map[string]any{}, nil
		}),
		DefaultModelAlias: "mini",
		ModelLookup: modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
			return ModelResolution{Model: fakeLLM{name: "mini"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}
	turn, err := resolver.ResolveTurn(context.Background(), TurnIntent{
		SessionRef: session.SessionRef{SessionID: "s1"},
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	if _, ok := turn.RunRequest.AgentSpec.Metadata[policyapi.MetadataPolicyProfile]; ok {
		t.Fatalf("policy_profile = %#v, want runtime default policy to remain authoritative", turn.RunRequest.AgentSpec.Metadata[policyapi.MetadataPolicyProfile])
	}
}

func TestAssemblyResolverControllerTurnPreservesPolicyMetadataWithoutModelLookup(t *testing.T) {
	t.Parallel()

	modelCalls := 0
	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) {
			return map[string]any{
				StateCurrentApprovalMode:    "manual",
				assembly.StateCurrentModeID: "plan",
			}, nil
		}),
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{{
				ID: "plan",
				Runtime: assembly.RuntimeOverrides{
					PolicyMode:     "auto-review",
					SystemPrompt:   "controller prompt",
					ExtraReadRoots: []string{"/tmp/read"},
				},
			}},
		},
		DefaultModelAlias: "mini",
		ModelLookup: modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
			modelCalls++
			return ModelResolution{Model: fakeLLM{name: "mini"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}

	turn, err := resolver.ResolveControllerTurn(context.Background(), TurnIntent{
		SessionRef: session.SessionRef{SessionID: "s1"},
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("ResolveControllerTurn() error = %v", err)
	}
	if modelCalls != 0 {
		t.Fatalf("model lookup calls = %d, want 0", modelCalls)
	}
	meta := turn.RunRequest.AgentSpec.Metadata
	if _, ok := meta[policyapi.MetadataPolicyProfile]; ok {
		t.Fatalf("policy_profile = %#v, want legacy approval mode omitted from policy profile", meta[policyapi.MetadataPolicyProfile])
	}
	if got := meta["system_prompt"]; got != "controller prompt" {
		t.Fatalf("system_prompt = %#v, want assembly metadata", got)
	}
	if roots, _ := meta[policyapi.MetadataExtraReadRoots].([]string); len(roots) != 1 || roots[0] != "/tmp/read" {
		t.Fatalf("policy_extra_read_roots = %#v, want assembly roots", meta[policyapi.MetadataExtraReadRoots])
	}
	if turn.RunRequest.AgentSpec.Model != nil {
		t.Fatalf("controller AgentSpec.Model = %#v, want nil without model resolution", turn.RunRequest.AgentSpec.Model)
	}
}

func TestAssemblyResolverIntentModeOverridesState(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions: snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) {
			return map[string]any{assembly.StateCurrentModeID: "plan"}, nil
		}),
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{
				{ID: "default", Runtime: assembly.RuntimeOverrides{SystemPrompt: "default prompt"}},
				{ID: "plan", Runtime: assembly.RuntimeOverrides{SystemPrompt: "plan prompt"}},
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
		SessionRef: session.SessionRef{SessionID: "s1"},
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
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{{ID: "default"}},
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

func TestCurrentSessionModeUsesSessionModeKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  string
	}{
		{name: "empty defaults to auto-review", state: map[string]any{}, want: "auto-review"},
		{name: "approval mode key wins", state: map[string]any{StateCurrentApprovalMode: "auto-review"}, want: "auto-review"},
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

func TestCurrentApprovalModeIgnoresLegacySandboxState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  ApprovalMode
	}{
		{name: "empty defaults to auto-review", state: map[string]any{}, want: ApprovalModeAutoReview},
		{name: "unknown approval mode defaults to auto-review", state: map[string]any{StateCurrentApprovalMode: "unknown"}, want: ApprovalModeAutoReview},
		{name: "approval mode key wins", state: map[string]any{StateCurrentApprovalMode: "manual"}, want: ApprovalModeManual},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := CurrentApprovalMode(tt.state); got != tt.want {
				t.Fatalf("CurrentApprovalMode(%#v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestCurrentApprovalModeOrDefaultUsesFallbackOnlyWithoutOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  ApprovalMode
	}{
		{name: "empty uses manual fallback", state: map[string]any{}, want: ApprovalModeManual},
		{name: "explicit approval mode overrides fallback", state: map[string]any{StateCurrentApprovalMode: "auto-review"}, want: ApprovalModeAutoReview},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := CurrentApprovalModeOrDefault(tt.state, ApprovalModeManual); got != tt.want {
				t.Fatalf("CurrentApprovalModeOrDefault(%#v, manual) = %q, want %q", tt.state, got, tt.want)
			}
			if got := CurrentSessionModeOrDefault(tt.state, string(ApprovalModeManual)); got != string(tt.want) {
				t.Fatalf("CurrentSessionModeOrDefault(%#v, manual) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestAssemblyResolverConcurrentSetModelLookupAndResolveTurn(t *testing.T) {
	t.Parallel()

	resolver, err := NewAssemblyResolver(AssemblyResolverConfig{
		Sessions:          snapshotStateFunc(func(context.Context, session.SessionRef) (map[string]any, error) { return map[string]any{}, nil }),
		DefaultModelAlias: "mini",
		ContextWindow:     1024,
		ModelLookup:       namedModelLookup("mini"),
	})
	if err != nil {
		t.Fatalf("NewAssemblyResolver() error = %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				resolver.SetModelLookup(namedModelLookup("mini"), "mini")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := resolver.ResolveTurn(context.Background(), TurnIntent{SessionRef: session.SessionRef{SessionID: "s1"}}); err != nil {
					t.Errorf("ResolveTurn() error = %v", err)
					return
				}
				if _, err := resolver.ListModelAliases(context.Background(), session.SessionRef{SessionID: "s1"}); err != nil {
					t.Errorf("ListModelAliases() error = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func namedModelLookup(name string) ModelLookup {
	return modelLookupFunc(func(context.Context, string, int) (ModelResolution, error) {
		return ModelResolution{Model: fakeLLM{name: name}}, nil
	})
}

type modelLookupFunc func(context.Context, string, int) (ModelResolution, error)

func (f modelLookupFunc) ResolveModel(ctx context.Context, alias string, contextWindow int) (ModelResolution, error) {
	return f(ctx, alias, contextWindow)
}

type snapshotStateFunc func(context.Context, session.SessionRef) (map[string]any, error)

func (f snapshotStateFunc) SnapshotState(ctx context.Context, ref session.SessionRef) (map[string]any, error) {
	return f(ctx, ref)
}

type fakeLLM struct {
	name string
}

func (f fakeLLM) Name() string { return f.name }
func (f fakeLLM) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
