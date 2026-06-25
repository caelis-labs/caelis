package controladapter

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestRuntimeStackPluginDepsUseGroupedField(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
				return []PluginSnapshot{{ID: "grouped"}}, nil
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
			DefaultAliasFn: func() string {
				return "grouped"
			},
		},
	}

	if got := stack.Model.DefaultAliasFn(); got != "grouped" {
		t.Fatalf("Model.DefaultAliasFn() = %q, want grouped", got)
	}
}

func TestRuntimeStackModelDepsMissingFieldUsesEmptyDefault(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{}
	got := ""
	if stack.Model.DefaultAliasFn != nil {
		got = stack.Model.DefaultAliasFn()
	}
	if got != "" {
		t.Fatalf("Model.DefaultAliasFn() = %q, want empty default", got)
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

	statusFor := func(name string) SandboxStatus {
		return SandboxStatus{ResolvedBackend: name}
	}
	tests := []struct {
		name  string
		stack func(*testing.T, *int) *RuntimeStack
		call  func(context.Context, *RuntimeStack) (SandboxStatus, error)
	}{
		{
			name: "status",
			stack: func(t *testing.T, called *int) *RuntimeStack {
				t.Helper()
				return &RuntimeStack{Sandbox: SandboxRuntimeDeps{
					StatusFn: func() SandboxStatus {
						(*called)++
						return statusFor("status")
					},
				}}
			},
			call: func(_ context.Context, stack *RuntimeStack) (SandboxStatus, error) {
				return stack.Sandbox.StatusFn(), nil
			},
		},
		{
			name: "set-backend",
			stack: func(t *testing.T, called *int) *RuntimeStack {
				t.Helper()
				return &RuntimeStack{Sandbox: SandboxRuntimeDeps{
					SetBackendFn: func(_ context.Context, backend string) (SandboxStatus, error) {
						if backend != "windows" {
							t.Fatalf("SetSandboxBackend backend = %q, want windows", backend)
						}
						(*called)++
						return statusFor("set-backend"), nil
					},
				}}
			},
			call: func(ctx context.Context, stack *RuntimeStack) (SandboxStatus, error) {
				return stack.Sandbox.SetBackendFn(ctx, "windows")
			},
		},
		{
			name: "prepare",
			stack: func(t *testing.T, called *int) *RuntimeStack {
				t.Helper()
				return &RuntimeStack{Sandbox: SandboxRuntimeDeps{
					PrepareFn: func(context.Context) (SandboxStatus, error) {
						(*called)++
						return statusFor("prepare"), nil
					},
				}}
			},
			call: func(ctx context.Context, stack *RuntimeStack) (SandboxStatus, error) {
				return stack.Sandbox.PrepareFn(ctx)
			},
		},
		{
			name: "repair",
			stack: func(t *testing.T, called *int) *RuntimeStack {
				t.Helper()
				return &RuntimeStack{Sandbox: SandboxRuntimeDeps{
					RepairFn: func(context.Context) (SandboxStatus, error) {
						(*called)++
						return statusFor("repair"), nil
					},
				}}
			},
			call: func(ctx context.Context, stack *RuntimeStack) (SandboxStatus, error) {
				return stack.Sandbox.RepairFn(ctx)
			},
		},
		{
			name: "preflight",
			stack: func(t *testing.T, called *int) *RuntimeStack {
				t.Helper()
				return &RuntimeStack{Sandbox: SandboxRuntimeDeps{
					PreflightFn: func(_ context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
						if !allowNonElevatedRepair {
							t.Fatal("PreflightSandbox allowNonElevatedRepair = false, want true")
						}
						(*called)++
						return statusFor("preflight"), nil
					},
				}}
			},
			call: func(ctx context.Context, stack *RuntimeStack) (SandboxStatus, error) {
				return stack.Sandbox.PreflightFn(ctx, true)
			},
		},
		{
			name: "reset",
			stack: func(t *testing.T, called *int) *RuntimeStack {
				t.Helper()
				return &RuntimeStack{Sandbox: SandboxRuntimeDeps{
					ResetFn: func(context.Context) (SandboxStatus, error) {
						(*called)++
						return statusFor("reset"), nil
					},
				}}
			},
			call: func(ctx context.Context, stack *RuntimeStack) (SandboxStatus, error) {
				return stack.Sandbox.ResetFn(ctx)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := 0
			status, err := tt.call(context.Background(), tt.stack(t, &called))
			if err != nil {
				t.Fatalf("%s sandbox call error = %v", tt.name, err)
			}
			if status.ResolvedBackend != tt.name {
				t.Fatalf("%s sandbox call = %#v, want backend %q", tt.name, status, tt.name)
			}
			if called != 1 {
				t.Fatalf("%s sandbox call invoked dependency %d times, want 1", tt.name, called)
			}
		})
	}
}
