package controladapter

import (
	"context"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestPluginAssemblerDoesNotRetainRuntimeDeps(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[pluginAssembler]()
	if typ.NumField() != 1 {
		t.Fatalf("pluginAssembler fields = %d, want one focused dependency", typ.NumField())
	}
	field := typ.Field(0)
	if field.Name != "deps" || field.Type != reflect.TypeFor[PluginRuntimeDeps]() {
		t.Fatalf("pluginAssembler dependency = %s %v, want deps PluginRuntimeDeps", field.Name, field.Type)
	}
}

func TestPluginAssemblerUsesOnlyFocusedDependencies(t *testing.T) {
	t.Parallel()

	driver := NewPluginAssemblerForHost(PluginAssemblyDeps{
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(context.Context) ([]controlprompt.PluginSnapshot, error) {
				return []controlprompt.PluginSnapshot{{ID: "grouped"}}, nil
			},
		},
	})

	plugins, err := driver.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID != "grouped" {
		t.Fatalf("ListPlugins() = %#v, want grouped plugin", plugins)
	}
}

func TestRuntimeDepsPluginDepsMissingFieldErrors(t *testing.T) {
	t.Parallel()

	if err := missingRuntimeDependency("list plugins"); err == nil {
		t.Fatal("missingRuntimeDependency() error = nil")
	}
}

func TestRuntimeDepsModelDepsUseGroupedField(t *testing.T) {
	t.Parallel()

	deps := &runtimeDeps{
		Model: ModelRuntimeDeps{
			EffectiveAliasFn: func() string {
				return "grouped"
			},
		},
	}

	if got := deps.Model.EffectiveAliasFn(); got != "grouped" {
		t.Fatalf("Model.EffectiveAliasFn() = %q, want grouped", got)
	}
}

func TestHasReusableConnectAuthUsesCredentialValidationHook(t *testing.T) {
	t.Parallel()

	called := false
	driver := &assembler{deps: &runtimeDeps{
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

func TestRuntimeDepsModelDepsMissingFieldUsesEmptyDefault(t *testing.T) {
	t.Parallel()

	deps := &runtimeDeps{}
	got := ""
	if deps.Model.EffectiveAliasFn != nil {
		got = deps.Model.EffectiveAliasFn()
	}
	if got != "" {
		t.Fatalf("Model.EffectiveAliasFn() = %q, want empty effective selection", got)
	}
}

func TestRuntimeDepsModelChoicesFallbackUsesGroupedAliases(t *testing.T) {
	t.Parallel()

	deps := &runtimeDeps{
		Model: ModelRuntimeDeps{
			ListAliasesFn: func(context.Context, session.SessionRef) ([]string, error) {
				return []string{"alpha", "beta"}, nil
			},
		},
	}

	choices, err := listModelChoices(context.Background(), deps.Model, session.SessionRef{})
	if err != nil {
		t.Fatalf("listModelChoices() error = %v", err)
	}
	if len(choices) != 2 || choices[0].Alias != "alpha" || choices[1].Alias != "beta" {
		t.Fatalf("listModelChoices() = %#v, want alias-derived choices", choices)
	}
}

func TestRuntimeDepsSandboxDepsUseGroupedFields(t *testing.T) {
	t.Parallel()

	called := 0
	deps := &runtimeDeps{Sandbox: SandboxRuntimeDeps{StatusFn: func() SandboxStatusProjection {
		called++
		return SandboxStatusProjection{ResolvedBackend: "status"}
	}}}
	status := deps.Sandbox.StatusFn()
	if status.ResolvedBackend != "status" || called != 1 {
		t.Fatalf("sandbox status = %#v, calls=%d", status, called)
	}
}
