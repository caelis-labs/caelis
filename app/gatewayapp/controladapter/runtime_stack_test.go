package controladapter

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
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

func TestSetSessionModeRejectsProcessOwnedRuntimeMode(t *testing.T) {
	t.Parallel()

	setCalls := 0
	stack := &RuntimeStack{
		Status: StatusRuntimeDeps{
			RuntimeStateFn: func(context.Context, session.SessionRef) (SessionRuntimeState, error) {
				return SessionRuntimeState{SessionMode: "manual"}, nil
			},
			SetSessionModeFn: func(context.Context, session.SessionRef, string) (string, error) {
				setCalls++
				return "manual", nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus {
				return SandboxStatus{FullAccessMode: true, Route: "host", ResolvedBackend: "host"}
			},
		},
	}
	driver := newAssemblerForStack(stack, "surface", "")
	driver.session = session.Session{SessionRef: session.SessionRef{SessionID: "session-yolo"}}
	driver.hasSession = true

	_, err := driver.SetSessionMode(context.Background(), "manual")
	if err == nil || !strings.Contains(err.Error(), "process-owned") {
		t.Fatalf("SetSessionMode(manual) error = %v, want process-owned mode rejection", err)
	}
	if setCalls != 0 {
		t.Fatalf("SetSessionModeFn calls = %d, want zero", setCalls)
	}
}

func TestYOLONeverProjectsRemoteACPModeOrMutatesIt(t *testing.T) {
	t.Parallel()

	controllerModeSetCalls := 0
	stack := &RuntimeStack{
		Status: StatusRuntimeDeps{
			RuntimeStateFn: func(context.Context, session.SessionRef) (SessionRuntimeState, error) {
				return SessionRuntimeState{SessionMode: "yolo"}, nil
			},
			SetSessionModeFn: func(context.Context, session.SessionRef, string) (string, error) {
				t.Fatal("SetSessionModeFn must not be called in YOLO mode")
				return "", nil
			},
			CycleModeFn: func(context.Context, session.SessionRef) (string, error) {
				t.Fatal("CycleModeFn must not be called in YOLO mode")
				return "", nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus {
				return SandboxStatus{FullAccessMode: true, Route: "host", ResolvedBackend: "host"}
			},
		},
		Agent: AgentRuntimeDeps{
			ControllerStatusFn: func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error) {
				return controller.ControllerStatus{
					Mode: "manual",
					ModeOptions: []controller.ControllerMode{
						{ID: "manual", Name: "Manual"},
						{ID: "auto-review", Name: "Auto Review"},
					},
				}, true, nil
			},
			SetControllerModeFn: func(context.Context, session.SessionRef, string) (controller.ControllerStatus, error) {
				controllerModeSetCalls++
				return controller.ControllerStatus{Mode: "manual"}, nil
			},
		},
	}
	driver := newAssemblerForStack(stack, "surface", "")
	driver.session = session.Session{
		SessionRef: session.SessionRef{SessionID: "session-yolo-acp"},
		Controller: session.ControllerBinding{Kind: session.ControllerKindACP},
	}
	driver.hasSession = true

	for name, read := range map[string]func(context.Context) (controlstatus.StatusSnapshot, error){
		"full":        driver.Status,
		"lightweight": driver.LightweightStatus,
	} {
		status, err := read(context.Background())
		if err != nil {
			t.Fatalf("%s Status() error = %v", name, err)
		}
		if status.Session.SessionMode != "yolo" || status.Session.ModeLabel != "yolo" {
			t.Fatalf("%s Status() session = %#v, want process-owned YOLO mode", name, status.Session)
		}
		if !status.SandboxStatus.FullAccessMode || status.SandboxStatus.Route != "host" || status.SandboxStatus.ResolvedBackend != "host" {
			t.Fatalf("%s Status() sandbox = %#v, want visible full-access Host posture", name, status.SandboxStatus)
		}
	}

	for name, mutate := range map[string]func(context.Context) (controlstatus.StatusSnapshot, error){
		"set": func(ctx context.Context) (controlstatus.StatusSnapshot, error) {
			return driver.SetSessionMode(ctx, "manual")
		},
		"cycle": driver.CycleSessionMode,
	} {
		if _, err := mutate(context.Background()); err == nil || !strings.Contains(err.Error(), "process-owned") {
			t.Fatalf("%s session mode error = %v, want process-owned mode rejection", name, err)
		}
	}
	if controllerModeSetCalls != 0 {
		t.Fatalf("SetControllerModeFn calls = %d, want zero", controllerModeSetCalls)
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
