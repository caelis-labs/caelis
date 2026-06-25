package controladapter

import (
	"context"
	"testing"
)

func TestRuntimeStackPluginDepsPreferGroupedField(t *testing.T) {
	t.Parallel()

	calledLegacy := false
	stack := &RuntimeStack{
		ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
			calledLegacy = true
			return []PluginSnapshot{{ID: "legacy"}}, nil
		},
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
				return []PluginSnapshot{{ID: "grouped"}}, nil
			},
		},
	}

	plugins, err := stack.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID != "grouped" {
		t.Fatalf("ListPlugins() = %#v, want grouped plugin", plugins)
	}
	if calledLegacy {
		t.Fatal("ListPlugins() used legacy flat dependency despite grouped dependency")
	}
}

func TestRuntimeStackPluginDepsFallbackToLegacyField(t *testing.T) {
	t.Parallel()

	stack := &RuntimeStack{
		ListPluginsFn: func(context.Context) ([]PluginSnapshot, error) {
			return []PluginSnapshot{{ID: "legacy"}}, nil
		},
	}

	plugins, err := stack.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins() error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].ID != "legacy" {
		t.Fatalf("ListPlugins() = %#v, want legacy plugin", plugins)
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
				return stack.SandboxStatus(), nil
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
				return stack.SetSandboxBackend(ctx, "windows")
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
				return stack.PrepareSandbox(ctx)
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
				return stack.RepairSandbox(ctx)
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
				return stack.PreflightSandbox(ctx, true)
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
				return stack.ResetSandbox(ctx)
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
