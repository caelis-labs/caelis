package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp"
)

func TestDangerouslySkipPermissionsForcesProcessHostMode(t *testing.T) {
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		StoreDir:                   storeDir,
		WorkspaceKey:               "yolo-workspace",
		WorkspaceCWD:               workspace,
		PolicyProfile:              presets.ModeWorkspaceWrite,
		DangerouslySkipPermissions: true,
		Sandbox:                    SandboxConfig{RequestedType: "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	if !stack.runtime.DangerouslySkipPermissions || stack.runtime.PolicyProfile != presets.ModeWorkspaceWrite {
		t.Fatalf("runtime config = %#v, want user policy preserved beside process YOLO flag", stack.runtime)
	}
	if stack.sandbox.RequestedType != "host" {
		t.Fatalf("sandbox requested type = %q, want host", stack.sandbox.RequestedType)
	}
	status := stack.SandboxStatus()
	if !status.FullAccessMode || status.Route != "host" || status.ResolvedBackend != "host" || !strings.Contains(status.SecuritySummary, "YOLO") {
		t.Fatalf("SandboxStatus() = %#v, want visible YOLO Host status", status)
	}

	report, err := stack.Doctor(context.Background(), DoctorRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.FullAccessMode || !report.HostExecution || report.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("Doctor() = %#v, want visible YOLO policy and Host execution", report)
	}
	if warnings := strings.Join(report.Warnings, "\n"); !strings.Contains(warnings, "without sandbox isolation, human approval, or Guardian review") ||
		!strings.Contains(warnings, "not a security boundary") {
		t.Fatalf("Doctor warnings = %q, want explicit YOLO risk", warnings)
	}
	active, err := startGatewayAppTestSession(context.Background(), stack, "yolo-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.updateSessionState(context.Background(), active.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[kernel.StateCurrentApprovalMode] = "manual"
		next[kernel.StateCurrentPolicyProfile] = presets.ModeWorkspaceWrite
		return next, nil
	}); err != nil {
		t.Fatal(err)
	}
	runtimeState, err := stack.SessionRuntimeState(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.SessionMode != dangerouslySkipPermissionsModeLabel || runtimeState.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("SessionRuntimeState() = %#v, want process-owned YOLO display", runtimeState)
	}
	report, err = stack.Doctor(context.Background(), DoctorRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionMode != dangerouslySkipPermissionsModeLabel || report.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("Doctor(active Session) = %#v, want process-owned YOLO display", report)
	}
	for name, change := range map[string]func() error{
		"set": func() error {
			_, err := stack.SetSessionMode(context.Background(), active.SessionRef, "manual")
			return err
		},
		"cycle": func() error {
			_, err := stack.CycleSessionMode(context.Background(), active.SessionRef)
			return err
		},
	} {
		if err := change(); err == nil || !strings.Contains(err.Error(), "cannot be changed while YOLO mode is active") {
			t.Fatalf("%s session mode error = %v, want process posture lock", name, err)
		}
	}
	runtimeState, err = stack.SessionRuntimeState(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.SessionMode != dangerouslySkipPermissionsModeLabel || runtimeState.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("SessionRuntimeState() after rejected mode changes = %#v, want YOLO", runtimeState)
	}

	fallbackModes := &recordingModeProvider{}
	surface := stack.ACPSurface(fallbackModes, true, nil)
	modes, err := surface.SessionModes(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	if modes == nil || modes.CurrentModeID != dangerouslySkipPermissionsModeLabel || len(modes.AvailableModes) != 0 {
		t.Fatalf("ACP SessionModes() = %#v, want process-owned YOLO mode without mutable choices", modes)
	}
	if _, err := surface.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionID: active.SessionID,
		ModeID:    "manual",
	}); err == nil || !strings.Contains(err.Error(), "cannot be changed while YOLO mode is active") {
		t.Fatalf("ACP SetSessionMode(manual) error = %v, want process posture lock", err)
	}
	if fallbackModes.setCalls != 0 {
		t.Fatalf("fallback SetSessionMode calls = %d, want zero while YOLO mode is active", fallbackModes.setCalls)
	}

	if _, err := stack.SetSandboxBackend(context.Background(), "auto"); err == nil || !strings.Contains(err.Error(), "fixes the sandbox backend to Host") {
		t.Fatalf("SetSandboxBackend(auto) error = %v, want process Host lock", err)
	}
	if status, err := stack.SetSandboxBackend(context.Background(), "host"); err != nil || !status.FullAccessMode {
		t.Fatalf("SetSandboxBackend(host) = %#v, %v; want safe no-op", status, err)
	}
}

type recordingModeProvider struct {
	setCalls int
}

func (p *recordingModeProvider) SessionModes(context.Context, session.Session) (*acp.SessionModeState, error) {
	return &acp.SessionModeState{
		CurrentModeID: "manual",
		AvailableModes: []acp.SessionMode{
			{ID: "manual", Name: "Manual"},
		},
	}, nil
}

func (p *recordingModeProvider) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	p.setCalls++
	return acp.SetSessionModeResponse{}, nil
}

func TestDangerouslySkipPermissionsIsNotPersisted(t *testing.T) {
	storeDir := t.TempDir()
	workspace := t.TempDir()
	first, err := newGatewayAppTestStack(t, Config{
		StoreDir:                   storeDir,
		WorkspaceKey:               "yolo-persistence",
		WorkspaceCWD:               workspace,
		DangerouslySkipPermissions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newGatewayAppTestStack(t, Config{
		StoreDir:     storeDir,
		WorkspaceKey: "yolo-persistence",
		WorkspaceCWD: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	if reloaded.runtime.DangerouslySkipPermissions || reloaded.runtime.PolicyProfile != presets.ModeWorkspaceWrite || reloaded.SandboxStatus().FullAccessMode {
		t.Fatalf("reloaded runtime retained process-only YOLO mode: runtime=%#v status=%#v", reloaded.runtime, reloaded.SandboxStatus())
	}
}
