package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
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

	if !stack.composition.activeRuntime.DangerouslySkipPermissions || stack.composition.activeRuntime.PolicyProfile != presets.ModeWorkspaceWrite {
		t.Fatalf("runtime config = %#v, want user policy preserved beside process YOLO flag", stack.composition.activeRuntime)
	}
	if stack.composition.sandbox.RequestedType != "host" {
		t.Fatalf("sandbox requested type = %q, want host", stack.composition.sandbox.RequestedType)
	}
	status := stack.ControlStatus().Sandbox()
	if !status.FullAccessMode || status.Route != "host" || status.ResolvedBackend != "host" || !strings.Contains(status.SecuritySummary, "YOLO") {
		t.Fatalf("SandboxStatus() = %#v, want visible YOLO Host status", status)
	}

	report, err := stack.ControlStatus().Doctor(context.Background(), DoctorRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.FullAccessMode || !report.HostExecution || report.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("Doctor() = %#v, want visible YOLO policy and Host execution", report)
	}
	if warnings := strings.Join(report.Warnings, "\n"); !strings.Contains(warnings, "YOLO host escape is active") {
		t.Fatalf("Doctor warnings = %q, want a single YOLO reminder", warnings)
	}
	active, err := startGatewayAppTestSession(context.Background(), stack, "yolo-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.composition.updateSessionStateAtRevision(context.Background(), active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
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
	runtimeState, err := stack.ControlStatus().SessionRuntimeState(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.SessionMode != dangerouslySkipPermissionsModeLabel || runtimeState.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("SessionRuntimeState() = %#v, want process-owned YOLO display", runtimeState)
	}
	report, err = stack.ControlStatus().Doctor(context.Background(), DoctorRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionMode != dangerouslySkipPermissionsModeLabel || report.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("Doctor(active Session) = %#v, want process-owned YOLO display", report)
	}
	current := mustCurrentSession(t, stack, active.SessionID)
	revision := current.Revision
	base := appserver.WriteBase{
		SessionID:               current.SessionID,
		ExpectedRevision:        &revision,
		ExpectedControllerEpoch: current.Controller.EpochID,
	}
	for name, change := range map[string]func() (appserver.CommandResult, error){
		"approval": func() (appserver.CommandResult, error) {
			request := base
			request.OperationID = "yolo-approval-mode"
			return stack.ConfigurationCommands().ConfigureSessionMode(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, appserver.SessionModeRequest{WriteBase: request, Mode: "manual"})
		},
		"presentation": func() (appserver.CommandResult, error) {
			request := base
			request.OperationID = "yolo-presentation-mode"
			return stack.ConfigurationCommands().ConfigureSessionPresentationMode(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, appserver.SessionPresentationModeRequest{WriteBase: request, Mode: "manual"})
		},
	} {
		result, err := change()
		if err == nil || result.Outcome != appserver.OutcomeRejected {
			t.Fatalf("%s session mode = %#v, %v; want process posture rejection", name, result, err)
		}
	}
	runtimeState, err = stack.ControlStatus().SessionRuntimeState(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.SessionMode != dangerouslySkipPermissionsModeLabel || runtimeState.PolicyProfile != presets.ModeDangerFullAccess {
		t.Fatalf("SessionRuntimeState() after rejected mode changes = %#v, want YOLO", runtimeState)
	}

	fallbackModes := &recordingModeProvider{}
	source := stack.PresentationSource(fallbackModes, true, nil)
	modes, err := source.SessionModes(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	if modes == nil || modes.CurrentModeID != dangerouslySkipPermissionsModeLabel || len(modes.AvailableModes) != 0 {
		t.Fatalf("ACP SessionModes() = %#v, want process-owned YOLO mode without mutable choices", modes)
	}
	if fallbackModes.setCalls != 0 {
		t.Fatalf("fallback SetSessionMode calls = %d, want read-only projection", fallbackModes.setCalls)
	}
	self, ok := agentConfigForToolTest(stack.composition.activeRuntime.Assembly.Agents, "self")
	if !ok {
		t.Fatalf("YOLO runtime self agent missing from assembly: %#v", stack.composition.activeRuntime.Assembly.Agents)
	}
	if _, ok := self.SessionOptions.ConfigValues[acpConfigModeID]; ok {
		t.Fatalf("YOLO child session options = %#v, must not request unavailable manual mode", self.SessionOptions)
	}

	if _, _, err := stack.commandBackend.setSandboxBackendAtRevision(context.Background(), "auto", nil); err == nil || !strings.Contains(err.Error(), "fixes the sandbox backend to Host") {
		t.Fatalf("SetSandboxBackend(auto) error = %v, want process Host lock", err)
	}
	if status, _, err := stack.commandBackend.setSandboxBackendAtRevision(context.Background(), "host", nil); err != nil || !status.FullAccessMode {
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
	if reloaded.composition.activeRuntime.DangerouslySkipPermissions || reloaded.composition.activeRuntime.PolicyProfile != presets.ModeWorkspaceWrite || reloaded.ControlStatus().Sandbox().FullAccessMode {
		t.Fatalf("reloaded runtime retained process-only YOLO mode: runtime=%#v status=%#v", reloaded.composition.activeRuntime, reloaded.ControlStatus().Sandbox())
	}
}
