package controladapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestRuntimeStackPluginDepsUseGroupedField(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(context.Context) ([]controlprompt.PluginSnapshot, error) {
				return []controlprompt.PluginSnapshot{{ID: "grouped"}}, nil
			},
		},
	}

	plugins, err := stack.Plugin.ListPluginsFn(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID != "grouped" {
		t.Fatalf("ListPlugins() = %#v, want grouped plugin", plugins)
	}
}

func TestRuntimeStackPluginDepsMissingFieldErrors(t *testing.T) {
	t.Parallel()

	if err := missingRuntimeDependency("list plugins"); err == nil {
		t.Fatal("missingRuntimeDependency() error = nil")
	}
}

func TestRuntimeStackModelDepsUseGroupedField(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{
		Model: ModelRuntimeDeps{
			EffectiveAliasFn: func() string {
				return "grouped"
			},
		},
	}

	if got := stack.Model.EffectiveAliasFn(); got != "grouped" {
		t.Fatalf("Model.EffectiveAliasFn() = %q, want grouped", got)
	}
}

func TestHasReusableConnectAuthUsesCredentialValidationHook(t *testing.T) {
	t.Parallel()

	called := false
	driver := &assembler{stack: &RuntimeStack{
		Model: ModelRuntimeDeps{
			HasReusableAuthFn: func(_ context.Context, provider string, baseURL string) bool {
				called = true
				if provider != "deepseek" || baseURL != "https://api.deepseek.com/anthropic" {
					t.Fatalf("HasReusableAuthFn(%q, %q)", provider, baseURL)
				}
				return false
			},
			ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) {
				return []ModelChoice{{
					Provider: "deepseek",
					BaseURL:  "https://api.deepseek.com/anthropic",
				}}, nil
			},
		},
	}}

	if driver.hasReusableConnectAuth(context.Background(), "deepseek", "https://api.deepseek.com/anthropic") {
		t.Fatal("hasReusableConnectAuth() = true, want invalid credential hook result")
	}
	if !called {
		t.Fatal("HasReusableAuthFn was not called")
	}
}

func TestRuntimeStackModelDepsMissingFieldUsesEmptyDefault(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{}
	got := ""
	if stack.Model.EffectiveAliasFn != nil {
		got = stack.Model.EffectiveAliasFn()
	}
	if got != "" {
		t.Fatalf("Model.EffectiveAliasFn() = %q, want empty effective selection", got)
	}
}

func TestRuntimeStackModelChoicesFallbackUsesGroupedAliases(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{
		Model: ModelRuntimeDeps{
			ListAliasesFn: func(context.Context, session.SessionRef) ([]string, error) {
				return []string{"alpha", "beta"}, nil
			},
		},
	}

	choices, err := listModelChoices(context.Background(), stack.Model, session.SessionRef{})
	if err != nil {
		t.Fatalf("listModelChoices() error = %v", err)
	}
	if len(choices) != 2 || choices[0].Alias != "alpha" || choices[1].Alias != "beta" {
		t.Fatalf("listModelChoices() = %#v, want alias-derived choices", choices)
	}
}

func TestRuntimeStackSandboxDepsUseGroupedFields(t *testing.T) {
	t.Parallel()

	called := 0
	stack := &RuntimeStack{Sandbox: SandboxRuntimeDeps{StatusFn: func() SandboxStatus {
		called++
		return SandboxStatus{ResolvedBackend: "status"}
	}}}
	status := stack.Sandbox.StatusFn()
	if status.ResolvedBackend != "status" || called != 1 {
		t.Fatalf("sandbox status = %#v, calls=%d", status, called)
	}
}
