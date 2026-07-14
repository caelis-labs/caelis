package controladapter

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/testenv"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/controlprompt/connectwizard"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

func encryptCodeFreeAPIKeyForRuntimeTest(t *testing.T, apiKey string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte("Xtpa6sS&+D.NAo%CP8LA:7pk"))
	if err != nil {
		t.Fatalf("init aes cipher: %v", err)
	}
	blockSize := block.BlockSize()
	pad := blockSize - (len(apiKey) % blockSize)
	plain := append([]byte(apiKey), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte("%1KJIrl3!XUxr04V")).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func TestMain(m *testing.M) {
	readGitWorkspaceStatusForDisplay = func(context.Context, string) (gitWorkspaceStatus, bool) {
		return gitWorkspaceStatus{}, false
	}
	os.Exit(m.Run())
}

func ptrRuntimeMessage(message model.Message) *model.Message {
	return &message
}

func gatewayRuntimeDepsForTest(gw GatewayService) GatewayRuntimeDeps {
	return gatewayRuntimeDepsProviderForTest(func() GatewayService {
		return gw
	})
}

func gatewayRuntimeDepsProviderForTest(provider func() GatewayService) GatewayRuntimeDeps {
	return GatewayRuntimeDeps{
		TurnServiceFn: func() GatewayTurnService {
			return provider()
		},
		SessionServiceFn: func() GatewaySessionService {
			return provider()
		},
		ControlPlaneServiceFn: func() GatewayControlPlaneService {
			return provider()
		},
		StreamProviderFn: func() GatewayStreamProvider {
			return provider()
		},
	}
}

func modelUsageMetaForRuntimeTest(prompt int, cached int, completion int, total int, reasoning ...int) map[string]any {
	reasoningTokens := 0
	if len(reasoning) > 0 {
		reasoningTokens = reasoning[0]
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"sdk": map[string]any{
				"usage": map[string]any{
					"prompt_tokens":       prompt,
					"cached_input_tokens": cached,
					"completion_tokens":   completion,
					"reasoning_tokens":    reasoningTokens,
					"total_tokens":        total,
				},
			},
		},
	}
}

func closeAdapterTestTurn(t *testing.T, turn Turn) {
	t.Helper()
	if turn == nil {
		return
	}
	turn.Cancel()
	events := turn.Events()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				if err := turn.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
				return
			}
		case <-timer.C:
			_ = turn.Close()
			t.Fatal("turn did not close after cancel")
		}
	}
}

func TestStreamRequestFromProjectedSemanticSpawnRunningEvent(t *testing.T) {
	t.Parallel()

	event := session.CanonicalizeEvent(&session.Event{
		SessionID:  "root-session",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "spawn-1",
			Name:   "SPAWN",
			Status: "running",
			Input:  map[string]any{"agent": "explorer", "prompt": "inspect files"},
			Output: map[string]any{"task_id": "reya", "state": "running"},
			Content: []session.EventToolContent{{
				Type:       "terminal",
				TerminalID: "subagent-task-1",
			}},
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{"name": "SPAWN"},
					"task": map[string]any{
						"task_id":       "reya",
						"terminal_id":   "subagent-task-1",
						"output_cursor": int64(0),
						"running":       true,
						"state":         "running",
					},
				},
			},
		},
	})
	base := eventstream.Envelope{
		SessionID: "root-session",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "root-session",
		Meta:      event.Meta,
	}
	events := acpprojector.ProjectSessionEventEnvelope(base, event)
	for _, acpEnv := range events {
		req, ok := streamRequestFromACPEvent(acpEnv)
		if !ok {
			continue
		}
		if req.CallID != "spawn-1" || req.ToolName != "SPAWN" {
			t.Fatalf("stream request identity = %#v, want spawn-1/SPAWN", req)
		}
		if req.Ref.SessionID != "root-session" || req.Ref.TaskID != "reya" || req.Ref.TerminalID != "subagent-task-1" {
			t.Fatalf("stream request ref = %#v, want root-session/reya/subagent-task-1", req.Ref)
		}
		return
	}
	t.Fatalf("projected ACP envelopes did not produce stream request: %#v", events)
}

func TestAdapterUsesCurrentTurnServiceAfterSandboxRebuild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "driver-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "driver-workspace",
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Sandbox: gatewayapp.SandboxConfig{
			HelperPath: filepath.Join(t.TempDir(), "missing-landlock-helper"),
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "rebuild-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	beforeTurns := stack.KernelTurns()
	if got, err := driver.gatewayTurns(); err != nil || got != beforeTurns {
		t.Fatalf("driver.gatewayTurns() before rebuild = %p, %v; want %p", got, err, beforeTurns)
	}
	// This test only needs to force a gateway rebuild; the missing helper keeps
	// auto landlock fallback from recursively executing this test binary in CI.
	if _, err := stack.SetSandboxBackend(ctx, "auto"); err != nil {
		t.Fatalf("SetSandboxBackend(auto) error = %v", err)
	}
	afterTurns := stack.KernelTurns()
	if afterTurns == nil || afterTurns == beforeTurns {
		t.Fatalf("KernelTurns() after rebuild = %p, before %p; want replacement", afterTurns, beforeTurns)
	}
	if got, err := driver.gatewayTurns(); err != nil || got != afterTurns {
		t.Fatalf("driver.gatewayTurns() after rebuild = %p, %v; want current %p", got, err, afterTurns)
	}
}

func TestAllocateSideAgentHandleUsesSharedNamePool(t *testing.T) {
	t.Parallel()

	used := map[string]struct{}{}

	first := allocateSideAgentHandle(used, "claude")
	if !agenthandle.ContainsPoolName(first) {
		t.Fatalf("allocateSideAgentHandle() = %q, want shared human-name pool handle", first)
	}
	used[first] = struct{}{}
	second := allocateSideAgentHandle(used, "claude")
	if !agenthandle.ContainsPoolName(second) || second == first {
		t.Fatalf("allocateSideAgentHandle() = %q after %q, want unique shared pool handle", second, first)
	}
	used[second] = struct{}{}
	third := allocateSideAgentHandle(used, "claude")
	if !agenthandle.ContainsPoolName(third) || third == first || third == second {
		t.Fatalf("allocateSideAgentHandle() = %q after %q/%q, want unique shared pool handle", third, first, second)
	}
	if got := allocateSideAgentHandle(used, "anthropic/Claude Agent"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSideAgentHandle() = %q, want shared human-name pool handle", got)
	}
	if got := allocateSideAgentHandle(used, "!!!"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSideAgentHandle() = %q, want shared human-name pool handle", got)
	}
}

func TestAdapterDefersBlankSessionUntilFirstSubmission(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "lazy-session-test",
		StoreDir:     storeDir,
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.ID != "" {
		t.Fatalf("Status().SessionID = %q, want empty before first submission", status.Session.ID)
	}
	before, err := stack.KernelSessions().ListSessions(ctx, gateway.ListSessionsRequest{
		AppName:      stack.AppName,
		UserID:       stack.UserID,
		WorkspaceKey: stack.Workspace.Key,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("ListSessions(before) error = %v", err)
	}
	if len(before.Sessions) != 0 {
		t.Fatalf("ListSessions(before) = %d sessions, want none", len(before.Sessions))
	}

	turn, err := driver.Submit(ctx, Submission{Text: "hello"})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	closeAdapterTestTurn(t, turn)
	after, err := stack.KernelSessions().ListSessions(ctx, gateway.ListSessionsRequest{
		AppName:      stack.AppName,
		UserID:       stack.UserID,
		WorkspaceKey: stack.Workspace.Key,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("ListSessions(after) error = %v", err)
	}
	if len(after.Sessions) != 1 {
		t.Fatalf("ListSessions(after) = %d sessions, want one after first submission", len(after.Sessions))
	}
}

func TestAdapterSubmitRoutesActiveSessionInputToActiveTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	gw := &activeSubmitGatewayService{
		active: []gateway.ActiveTurnState{{
			SessionRef: activeSession.SessionRef,
			Kind:       gateway.ActiveTurnKindKernel,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}},
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	turn, err := driver.Submit(ctx, Submission{Text: "  steer next step  ", DisplayText: "$cmpctl steer next step", Mode: SubmissionModeActiveTurn})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if turn != nil {
		t.Fatalf("Submit() turn = %#v, want nil for active-turn guidance", turn)
	}
	if gw.beginCalls != 0 {
		t.Fatalf("BeginTurn calls = %d, want 0", gw.beginCalls)
	}
	if got, want := len(gw.activeSubmits), 1; got != want {
		t.Fatalf("active submits = %d, want %d", got, want)
	}
	if got := gw.activeSubmits[0].Text; got != "steer next step" {
		t.Fatalf("active submit text = %q, want trimmed guidance", got)
	}
	if got := gw.activeSubmits[0].DisplayText; got != "$cmpctl steer next step" {
		t.Fatalf("active submit display text = %q, want original display text", got)
	}
	if _, ok := gw.activeSubmits[0].Metadata["display_text"]; ok {
		t.Fatalf("active submit metadata contains legacy display_text: %#v", gw.activeSubmits[0].Metadata)
	}
}

func TestAdapterResumeCommitsAtomicReconnectAndSteersActiveTurn(t *testing.T) {
	ctx := context.Background()
	oldSession := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "old-session", WorkspaceKey: "ws",
	}, CWD: t.TempDir()}
	target := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "target-session", WorkspaceKey: "ws",
	}, CWD: oldSession.CWD}
	gw := &activeSubmitGatewayService{
		resume: target,
		active: []gateway.ActiveTurnState{{
			SessionRef: target.SessionRef, Kind: gateway.ActiveTurnKindKernel,
			HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
		}},
	}
	reconnect := &fixedReconnectReader{result: controlclientport.ReconnectResult{
		State: controlclientport.SessionState{
			SessionID: target.SessionID, ResumeMode: controlclientport.ResumeModeExact,
			Run: controlclientport.RunState{Active: true, HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
			Approval: controlclientport.ApprovalState{Active: &controlclientport.ActiveApproval{
				RequestID: "approval-1", Permission: &session.ProtocolApproval{},
			}},
		},
		Subscription: newProtocolFeedSubscription(nil),
	}}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw), ControlReconnect: reconnect,
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws", CWD: oldSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) { return oldSession, nil },
		},
		Sandbox: SandboxRuntimeDeps{StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} }},
	}, oldSession.SessionID, "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := driver.ResumeSession(ctx, target.SessionID)
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if gw.resumeReq.BindingKey != "" || len(gw.bindReqs) != 1 || gw.bindReqs[0].SessionRef.SessionID != target.SessionID {
		t.Fatalf("resume/bind requests = %#v / %#v, want read-only resolve then one target bind", gw.resumeReq, gw.bindReqs)
	}
	if snapshot.Reconnect == nil || !snapshot.Reconnect.State().Run.Active {
		t.Fatalf("snapshot reconnect = %#v", snapshot.Reconnect)
	}
	defer snapshot.Reconnect.Close()
	bootstrap := snapshot.Reconnect.BootstrapEvents()
	if len(bootstrap) != 1 || bootstrap[0].ApprovalRequestID != "approval-1" {
		t.Fatalf("approval bootstrap = %#v, want original request ID", bootstrap)
	}
	if _, err := driver.Submit(ctx, Submission{Text: "steer after resume", Mode: SubmissionModeActiveTurn}); err != nil {
		t.Fatalf("Submit(active) error = %v", err)
	}
	if gw.beginCalls != 0 || len(gw.activeSubmits) != 1 || gw.activeSubmits[0].Kind != gateway.SubmissionKindConversation {
		t.Fatalf("turn routing begin=%d active=%#v", gw.beginCalls, gw.activeSubmits)
	}
	if err := snapshot.Reconnect.SubmitApproval(ctx, ApprovalDecision{RequestID: "approval-1", OptionID: "allow_once", Approved: true}); err != nil {
		t.Fatalf("SubmitApproval() error = %v", err)
	}
	if len(gw.activeSubmits) != 2 || gw.activeSubmits[1].Approval == nil || gw.activeSubmits[1].Approval.RequestID != "approval-1" {
		t.Fatalf("approval submit = %#v, want original request ID", gw.activeSubmits)
	}
}

func TestAdapterResumeBootstrapFailurePreservesCurrentSessionAndBinding(t *testing.T) {
	ctx := context.Background()
	oldSession := session.Session{SessionRef: session.SessionRef{SessionID: "old-session"}}
	target := session.Session{SessionRef: session.SessionRef{SessionID: "target-session"}}
	gw := &activeSubmitGatewayService{resume: target}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway:          gatewayRuntimeDepsForTest(gw),
		ControlReconnect: &fixedReconnectReader{err: errors.New("bootstrap failed")},
		Session:          SessionRuntimeDeps{StartFn: func(context.Context, string, string) (session.Session, error) { return oldSession, nil }},
	}, oldSession.SessionID, "surface", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ResumeSession(ctx, target.SessionID); err == nil {
		t.Fatal("ResumeSession() error = nil, want bootstrap failure")
	}
	current, ok := driver.currentSession()
	if !ok || current.SessionID != oldSession.SessionID || len(gw.bindReqs) != 0 {
		t.Fatalf("current/binds after failure = %#v %v / %#v", current, ok, gw.bindReqs)
	}
}

func TestAdapterSubmitDefaultModeStartsNewTurnDespiteActiveKernelTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	gw := &activeSubmitGatewayService{
		active: []gateway.ActiveTurnState{{
			SessionRef: activeSession.SessionRef,
			Kind:       gateway.ActiveTurnKindKernel,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}},
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	_, err = driver.Submit(ctx, Submission{Text: "  fresh prompt after resume  "})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if got := len(gw.activeSubmits); got != 0 {
		t.Fatalf("active submits = %d, want 0 for default submission", got)
	}
	if gw.beginCalls != 1 {
		t.Fatalf("BeginTurn calls = %d, want 1 default main turn", gw.beginCalls)
	}
	if got := gw.beginReqs[0].Input; got != "fresh prompt after resume" {
		t.Fatalf("BeginTurn Input = %q, want trimmed prompt", got)
	}
}

func TestAdapterSubmitActiveTurnRequiresActiveKernelTurn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		controllerKind session.ControllerKind
		active         []gateway.ActiveTurnState
	}{
		{name: "no active turn"},
		{name: "acp controller session", controllerKind: session.ControllerKindACP, active: []gateway.ActiveTurnState{{
			SessionRef: session.SessionRef{AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws"},
			Kind:       gateway.ActiveTurnKindKernel,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			activeSession := session.Session{
				SessionRef: session.SessionRef{
					AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
				},
				CWD:        t.TempDir(),
				Controller: session.ControllerBinding{Kind: tc.controllerKind},
			}
			gw := &activeSubmitGatewayService{active: tc.active}
			driver, err := NewAdapter(ctx, &RuntimeStack{
				Gateway: gatewayRuntimeDepsForTest(gw),
				Session: SessionRuntimeDeps{
					Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
					StartFn: func(context.Context, string, string) (session.Session, error) {
						return activeSession, nil
					},
				},
				Sandbox: SandboxRuntimeDeps{
					StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
				},
			}, activeSession.SessionID, "surface", "")
			if err != nil {
				t.Fatalf("NewAdapter() error = %v", err)
			}

			turn, err := driver.Submit(ctx, Submission{Text: "steer running turn", Mode: SubmissionModeActiveTurn})
			assertNoActiveRunError(t, err)
			if turn != nil {
				t.Fatalf("Submit() turn = %#v, want nil", turn)
			}
			if got := len(gw.activeSubmits); got != 0 {
				t.Fatalf("active submits = %d, want 0", got)
			}
			if gw.beginCalls != 0 {
				t.Fatalf("BeginTurn calls = %d, want 0", gw.beginCalls)
			}
		})
	}
}

func TestAdapterSubmitActiveTurnNoActiveRunErrorDoesNotBeginTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	gw := &activeSubmitGatewayService{
		active: []gateway.ActiveTurnState{{
			SessionRef: activeSession.SessionRef,
			Kind:       gateway.ActiveTurnKindKernel,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}},
		activeErr: &gateway.Error{
			Kind:        gateway.KindConflict,
			Code:        gateway.CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: no active run is available for this session",
		},
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	turn, err := driver.Submit(ctx, Submission{Text: "steer running turn", Mode: SubmissionModeActiveTurn})
	assertNoActiveRunError(t, err)
	if turn != nil {
		t.Fatalf("Submit() turn = %#v, want nil", turn)
	}
	if got := len(gw.activeSubmits); got != 1 {
		t.Fatalf("active submits = %d, want 1 attempted active submit", got)
	}
	if gw.beginCalls != 0 {
		t.Fatalf("BeginTurn calls = %d, want 0 after no_active_run", gw.beginCalls)
	}
}

func assertNoActiveRunError(t *testing.T, err error) {
	t.Helper()
	var gwErr *gateway.Error
	if !errors.As(err, &gwErr) {
		t.Fatalf("Submit() error = %v, want gateway error", err)
	}
	if gwErr.Code != gateway.CodeNoActiveRun {
		t.Fatalf("gateway error code = %q, want %q", gwErr.Code, gateway.CodeNoActiveRun)
	}
}

func TestAdapterSubmitForwardsReferencesToGateway(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "dict.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "reference-session", WorkspaceKey: "ws",
		},
		CWD: workspace,
	}
	gw := &activeSubmitGatewayService{}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: workspace},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	raw := "$CMPCTL inspect #dict.go"
	turn, err := driver.Submit(ctx, Submission{Text: raw, DisplayText: raw})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if turn != nil {
		t.Fatalf("Submit() turn = %#v, want nil for fake BeginTurn", turn)
	}
	if got, want := len(gw.beginReqs), 1; got != want {
		t.Fatalf("BeginTurn calls = %d, want %d", got, want)
	}
	req := gw.beginReqs[0]
	if req.Input != raw {
		t.Fatalf("BeginTurn Input = %q, want raw input forwarded to gateway", req.Input)
	}
	if req.DisplayInput != "" {
		t.Fatalf("DisplayInput = %q, want empty when display matches raw input", req.DisplayInput)
	}
	if _, ok := req.Metadata["display_text"]; ok {
		t.Fatalf("metadata contains legacy display_text: %#v", req.Metadata)
	}
}

func TestAdapterStartupDoesNotQuerySandboxStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statusCalls := 0
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "startup-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus {
				statusCalls++
				return SandboxStatus{RequestedBackend: "windows", ResolvedBackend: "windows"}
			},
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	if driver.sandboxType != "auto" {
		t.Fatalf("startup sandbox type = %q, want lightweight default", driver.sandboxType)
	}
	if statusCalls != 0 {
		t.Fatalf("SandboxStatus() calls during startup = %d, want 0", statusCalls)
	}
}

func TestAdapterLightweightStatusSkipsSandboxDiagnostics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sandboxCalls := 0
	doctorCalls := 0
	usageCalls := 0
	store := &countingEventsSessionService{
		Service: inmemory.NewService(inmemory.NewStore(inmemory.Config{})),
	}
	stack := &RuntimeStack{
		Session: SessionRuntimeDeps{
			Store:     store,
			Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
		},
		Model: ModelRuntimeDeps{
			DefaultAliasFn: func() string {
				return "gpt-light"
			},
			SessionUsageSnapshotFn: func(context.Context, session.SessionRef, string) (compact.UsageSnapshot, error) {
				usageCalls++
				return compact.UsageSnapshot{}, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus {
				sandboxCalls++
				return SandboxStatus{RequestedBackend: "windows", ResolvedBackend: "windows"}
			},
		},
		Status: StatusRuntimeDeps{
			DoctorFn: func(context.Context, DoctorRequest) (DoctorReport, error) {
				doctorCalls++
				return DoctorReport{ActiveModelAlias: "doctor-model"}, nil
			},
		},
	}
	driver := newAdapterForStack(stack, "surface", "")
	driver.session = session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "user-1", SessionID: "session-1", WorkspaceKey: "ws"},
		CWD:        stack.Session.Workspace.CWD,
	}
	driver.hasSession = true
	status, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatalf("LightweightStatus() error = %v", err)
	}
	if status.ModelStatus.Display != "gpt-light" {
		t.Fatalf("LightweightStatus().Model = %q, want default alias", status.ModelStatus.Display)
	}
	if sandboxCalls != 0 {
		t.Fatalf("SandboxStatus() calls = %d, want 0", sandboxCalls)
	}
	if doctorCalls != 0 {
		t.Fatalf("Doctor() calls = %d, want 0", doctorCalls)
	}
	if usageCalls != 0 || store.eventsCalls != 0 {
		t.Fatalf("LightweightStatus() usage calls = %d, event scans = %d, want 0/0", usageCalls, store.eventsCalls)
	}
	if _, err := driver.Status(ctx); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if usageCalls != 1 || store.eventsCalls != 1 {
		t.Fatalf("Status() usage calls = %d, event scans = %d, want 1/1", usageCalls, store.eventsCalls)
	}
}

func TestAdapterLightweightStatusPropagatesCallerCancellation(t *testing.T) {
	t.Parallel()

	driver := newAdapterForStack(&RuntimeStack{
		Status: StatusRuntimeDeps{
			RuntimeStateFn: func(ctx context.Context, _ session.SessionRef) (SessionRuntimeState, error) {
				<-ctx.Done()
				return SessionRuntimeState{}, ctx.Err()
			},
		},
	}, "surface", "")
	driver.session = session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}
	driver.hasSession = true

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := driver.LightweightStatus(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LightweightStatus() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("LightweightStatus() ignored caller cancellation for %s", elapsed)
	}
}

func TestAdapterLightweightStatusDoesNotWaitForBlockedEventReader(t *testing.T) {
	t.Parallel()

	store := &blockingEventsSessionService{
		Service: inmemory.NewService(inmemory.NewStore(inmemory.Config{})),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	driver := newAdapterForStack(&RuntimeStack{
		Session: SessionRuntimeDeps{Store: store},
	}, "surface", "")
	driver.session = session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}
	driver.hasSession = true

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := driver.LightweightStatus(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LightweightStatus() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(store.release)
		t.Fatal("LightweightStatus() waited for the blocked event reader")
	}
	select {
	case <-store.entered:
		close(store.release)
		t.Fatal("LightweightStatus() called the event reader")
	default:
		close(store.release)
	}
}

type countingEventsSessionService struct {
	session.Service
	eventsCalls int
}

func (s *countingEventsSessionService) Events(ctx context.Context, req session.EventsRequest) ([]*session.Event, error) {
	s.eventsCalls++
	return s.Service.Events(ctx, req)
}

type blockingEventsSessionService struct {
	session.Service
	entered chan struct{}
	release chan struct{}
}

func (s *blockingEventsSessionService) Events(ctx context.Context, req session.EventsRequest) ([]*session.Event, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.Service.Events(ctx, req)
	}
}

func TestAdapterSubmitDoesNotRouteParticipantActiveTurnInputToActiveTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	gw := &activeSubmitGatewayService{
		active: []gateway.ActiveTurnState{{
			SessionRef: activeSession.SessionRef,
			Kind:       gateway.ActiveTurnKindParticipant,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}},
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	_, err = driver.Submit(ctx, Submission{Text: "  main prompt after side run  "})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if got := len(gw.activeSubmits); got != 0 {
		t.Fatalf("active submits = %d, want 0 for participant active turn", got)
	}
	if gw.beginCalls != 1 {
		t.Fatalf("BeginTurn calls = %d, want 1 fallback main turn attempt", gw.beginCalls)
	}
}

func TestAdapterListSessionsSkipsUntitledSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "resume-filter-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.KernelSessions().StartSession(ctx, gateway.StartSessionRequest{
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,
	}); err != nil {
		t.Fatalf("StartSession(blank) error = %v", err)
	}
	titled, err := stack.KernelSessions().StartSession(ctx, gateway.StartSessionRequest{
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,
		Title:     "visible prompt",
	})
	if err != nil {
		t.Fatalf("StartSession(titled) error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	candidates, err := driver.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("ListSessions() = %#v, want one titled candidate", candidates)
	}
	if candidates[0].SessionID != titled.SessionID || candidates[0].Prompt != "visible prompt" {
		t.Fatalf("ListSessions()[0] = %#v, want titled session", candidates[0])
	}
}

func TestAdapterCompleteSlashArgConnectFlowUsesLegacyCommands(t *testing.T) {
	ctx := context.Background()
	credsPath := filepath.Join(t.TempDir(), "codefree.json")
	rawCreds, err := json.Marshal(map[string]any{
		"encryptedApiKey": encryptCodeFreeAPIKeyForRuntimeTest(t, "cached-api-key"),
		"userId":          "272182",
		"sessionId":       "cached-session",
		"baseUrlSnapshot": "https://www.srdcloud.cn",
	})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.WriteFile(credsPath, rawCreds, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	t.Setenv("CODEFREE_OAUTH_CREDS_PATH", credsPath)

	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "connect-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "connect-flow-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	providers, err := driver.CompleteSlashArg(ctx, "connect", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect) error = %v", err)
	}
	if len(providers) == 0 || providers[0].Value == "" {
		t.Fatalf("provider candidates = %#v, want non-empty", providers)
	}
	xiaomiEndpoints, err := driver.CompleteSlashArg(ctx, "connect-baseurl:xiaomi", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-baseurl:xiaomi) error = %v", err)
	}
	if !slashCandidatesHaveValue(xiaomiEndpoints, connectXiaomiAPIBaseURL) {
		t.Fatalf("xiaomi endpoint candidates = %#v, missing api cn", xiaomiEndpoints)
	}
	var foundTokenPlan bool
	for _, item := range xiaomiEndpoints {
		if strings.EqualFold(strings.TrimSpace(item.Value), connectXiaomiTokenPlanCNBaseURL) &&
			strings.Contains(item.Detail, "MIMO_TOKEN_PLAN_API_KEY") {
			foundTokenPlan = true
		}
	}
	if !foundTokenPlan {
		t.Fatalf("xiaomi endpoint candidates = %#v, missing token-plan CN OpenAI detail", xiaomiEndpoints)
	}

	models, err := driver.CompleteSlashArg(ctx, connectModelCompletionCommand(connectwizard.ConnectWizardState{
		Provider:       "minimax",
		BaseURL:        "https://api.minimaxi.com/anthropic",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model) error = %v", err)
	}
	found := false
	for _, item := range models {
		if item.Value == "MiniMax-M2.7-highspeed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("connect model candidates = %#v, want built-in MiniMax-M2.7-highspeed", models)
	}

	deepseekModels, err := driver.CompleteSlashArg(ctx, connectModelCompletionCommand(connectwizard.ConnectWizardState{
		Provider:       "deepseek",
		BaseURL:        "https://api.deepseek.com/v1",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model deepseek) error = %v", err)
	}
	if len(deepseekModels) != 2 {
		t.Fatalf("deepseek connect model candidates = %#v, want exactly 2 built-ins", deepseekModels)
	}
	if deepseekModels[0].Value != "deepseek-v4-flash" || deepseekModels[1].Value != "deepseek-v4-pro" {
		t.Fatalf("deepseek connect model candidates = %#v, want deepseek-v4-flash and deepseek-v4-pro", deepseekModels)
	}
	for _, item := range deepseekModels {
		if !strings.Contains(item.Detail, "catalog preset") {
			t.Fatalf("deepseek connect model candidate = %#v, want catalog preset detail", item)
		}
	}
	openAICompatModels, err := driver.CompleteSlashArg(ctx, connectModelCompletionCommand(connectwizard.ConnectWizardState{
		Provider:       "openai-compatible",
		BaseURL:        "https://api.openai.com/v1",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}), "gpt-5.5", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model openai-compatible) error = %v", err)
	}
	foundOpenAICompatDirectoryModel := false
	for _, item := range openAICompatModels {
		if item.Value == "gpt-5.5" && strings.Contains(item.Detail, "model directory") {
			foundOpenAICompatDirectoryModel = true
			break
		}
	}
	if !foundOpenAICompatDirectoryModel {
		t.Fatalf("openai-compatible connect model candidates = %#v, want gpt-5.5 from model directory", openAICompatModels)
	}
	openAIModels, err := driver.CompleteSlashArg(ctx, connectModelCompletionCommand(connectwizard.ConnectWizardState{
		Provider:       "openai",
		BaseURL:        "https://api.openai.com/v1",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}), "gpt-5.1-codex", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model openai) error = %v", err)
	}
	if len(openAIModels) != 0 {
		t.Fatalf("openai connect model candidates = %#v, did not want models.dev-only model for explicit provider", openAIModels)
	}

	codefreeModels, err := driver.CompleteSlashArg(ctx, connectModelCompletionCommand(connectwizard.ConnectWizardState{
		Provider:       "codefree",
		BaseURL:        "https://www.srdcloud.cn",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model codefree) error = %v", err)
	}
	foundCodeFree := false
	for _, item := range codefreeModels {
		if item.Value == "GLM-4.7" {
			foundCodeFree = true
			break
		}
	}
	if !foundCodeFree {
		t.Fatalf("codefree connect model candidates = %#v, want built-in GLM-4.7 without auth side effects", codefreeModels)
	}
}

func TestAdapterCompleteSlashArgUsesRealModelAliases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "slash-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "slash-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "alt-model",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	useCandidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(useCandidates) < 2 {
		t.Fatalf("model use candidates = %#v, want at least default and session aliases", useCandidates)
	}
	if got := useCandidates[0].Display; got != "ollama/alt-model" {
		t.Fatalf("first model use display = %q, want ollama/alt-model", got)
	}

	delCandidates, err := driver.CompleteSlashArg(ctx, "model del", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model del) error = %v", err)
	}
	if len(delCandidates) < 2 {
		t.Fatalf("model del candidates = %#v, want at least default and session aliases", delCandidates)
	}
	if got := delCandidates[0].Display; got != "ollama/alt-model" {
		t.Fatalf("first model del display = %q, want ollama/alt-model", got)
	}
}

func TestAdapterCompleteSlashArgACPModelUseOnly(t *testing.T) {
	t.Parallel()

	driver := &Adapter{}
	status := gatewayapp.ACPControllerStatus{
		ModelOptions: []gatewayapp.ACPControllerConfigChoice{{
			Value:       "claude-sonnet",
			Name:        "Claude Sonnet",
			Description: "remote model",
		}},
		EffortOptions: []gatewayapp.ACPControllerConfigChoice{{
			Value: "high",
			Name:  "High",
		}},
	}
	actions, handled := driver.completeACPControllerSlashArg(status, "model", "", 10)
	if !handled || len(actions) != 1 || actions[0].Value != "use" {
		t.Fatalf("ACP model actions = %#v handled=%v, want only use", actions, handled)
	}
	models, handled := driver.completeACPControllerSlashArg(status, "model use", "claude", 10)
	if !handled || len(models) != 1 || models[0].Value != "claude-sonnet" {
		t.Fatalf("ACP model candidates = %#v handled=%v, want remote model", models, handled)
	}
	efforts, handled := driver.completeACPControllerSlashArg(status, "model use claude-sonnet", "", 10)
	if !handled || len(efforts) != 1 || efforts[0].Value != "high" {
		t.Fatalf("ACP effort candidates = %#v handled=%v, want remote effort", efforts, handled)
	}
	deletes, handled := driver.completeACPControllerSlashArg(status, "model del", "", 10)
	if !handled || len(deletes) != 0 {
		t.Fatalf("ACP delete candidates = %#v handled=%v, want handled empty", deletes, handled)
	}
}

func TestAdapterCompleteSlashArgACPModelUsesConfigEfforts(t *testing.T) {
	t.Parallel()

	driver := &Adapter{}
	status := gatewayapp.ACPControllerStatus{
		ModelOptions: []gatewayapp.ACPControllerConfigChoice{
			{Value: "gpt-5.5", Name: "GPT-5.5"},
			{Value: "gpt-5.4", Name: "gpt-5.4"},
		},
		EffortOptions: []gatewayapp.ACPControllerConfigChoice{
			{Value: "low", Name: "Low"},
			{Value: "high", Name: "High"},
		},
	}
	efforts, handled := driver.completeACPControllerSlashArg(status, "model use gpt-5.5", "", 10)
	if !handled || len(efforts) != 2 || efforts[0].Value != "low" || efforts[1].Value != "high" {
		t.Fatalf("ACP gpt-5.5 efforts = %#v handled=%v, want config low/high", efforts, handled)
	}
	efforts, handled = driver.completeACPControllerSlashArg(status, "model use gpt-5.4", "", 10)
	if !handled || len(efforts) != 2 || efforts[0].Value != "low" || efforts[1].Value != "high" {
		t.Fatalf("ACP gpt-5.4 efforts = %#v handled=%v, want config low/high", efforts, handled)
	}
}

func TestAdapterCompleteSlashArgACPModelUsesModelSpecificEfforts(t *testing.T) {
	t.Parallel()

	driver := &Adapter{}
	status := gatewayapp.ACPControllerStatus{
		ModelOptions: []gatewayapp.ACPControllerConfigChoice{
			{Value: "gpt-5.5", Name: "GPT-5.5"},
			{Value: "gpt-5.4", Name: "gpt-5.4"},
		},
		EffortOptionsByModel: map[string][]gatewayapp.ACPControllerConfigChoice{
			"gpt-5.4": {
				{Value: "low", Name: "Low"},
				{Value: "xhigh", Name: "Xhigh"},
			},
		},
	}
	efforts, handled := driver.completeACPControllerSlashArg(status, "model use gpt-5.4", "", 10)
	if !handled || len(efforts) != 2 || efforts[0].Value != "low" || efforts[1].Value != "xhigh" {
		t.Fatalf("ACP gpt-5.4 efforts = %#v handled=%v, want model-specific low/xhigh", efforts, handled)
	}
	efforts, handled = driver.completeACPControllerSlashArg(status, "model use gpt-5.5", "", 10)
	if !handled || len(efforts) != 0 {
		t.Fatalf("ACP gpt-5.5 efforts = %#v handled=%v, want no model-specific efforts", efforts, handled)
	}
}

func TestAdapterCompletesAndPersistsModelReasoningLevel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "model-reasoning-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "deepseek",
			API:      providers.APIDeepSeek,
			Model:    "deepseek-v4-pro",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "model-reasoning-session", "surface", "deepseek/deepseek-v4-pro")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	levels, err := driver.CompleteSlashArg(ctx, "model use deepseek/deepseek-v4-pro", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use alias) error = %v", err)
	}
	if got := candidateValues(levels); !equalStrings(got, []string{"none", "high", "max"}) {
		t.Fatalf("reasoning candidates = %#v, want none/high/max", levels)
	}
	if _, err := driver.UseModel(ctx, "deepseek/deepseek-v4-pro", "high"); err != nil {
		t.Fatalf("UseModel(reasoning) error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.ModelStatus.Display); got != "deepseek/deepseek-v4-pro [high]" {
		t.Fatalf("status model = %q, want deepseek/deepseek-v4-pro [high]", got)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("driver has no current session")
	}
	state, err := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := strings.TrimSpace(state[gateway.StateCurrentReasoningEffort].(string)); got != "high" {
		t.Fatalf("reasoning state = %q, want high", got)
	}
	cfg, ok := stack.ModelConfig("deepseek/deepseek-v4-pro")
	if !ok {
		t.Fatal("expected deepseek model config")
	}
	if got := strings.TrimSpace(cfg.ReasoningEffort); got != "high" {
		t.Fatalf("config reasoning effort = %q, want high", got)
	}
}

func TestAdapterConnectPersistsDeepSeekModelDefaults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "connect-defaults-test",
		StoreDir:     root,
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "connect-defaults-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "secret",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := status.Usage.ContextWindowTokens; got != 1048576 {
		t.Fatalf("status.Usage.ContextWindowTokens = %d, want 1048576", got)
	}
	if got := strings.TrimSpace(status.ModelStatus.ReasoningEffort); got != "high" {
		t.Fatalf("status.ModelStatus.ReasoningEffort = %q, want high", got)
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg gatewayapp.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "deepseek/deepseek-v4-flash") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want deepseek/deepseek-v4-flash", doc.Models.Configs)
	}
	if cfg.ID != "deepseek@default/deepseek/deepseek-v4-flash" {
		t.Fatalf("persisted model id = %q, want readable profile/model alias id", cfg.ID)
	}
	if cfg.ProfileID != "deepseek@default" {
		t.Fatalf("persisted profile id = %q, want deepseek@default", cfg.ProfileID)
	}
	if cfg.Provider != "" || cfg.BaseURL != "" || cfg.Token != "" || cfg.TokenEnv != "" {
		t.Fatalf("persisted model leaked profile fields: %#v", cfg)
	}
	var conn gatewayapp.ModelProfileConfig
	for _, item := range doc.Models.Profiles {
		if strings.EqualFold(item.ID, cfg.ProfileID) {
			conn = item
			break
		}
	}
	if conn.ID == "" {
		t.Fatalf("persisted profiles = %#v, missing %q", doc.Models.Profiles, cfg.ProfileID)
	}
	if conn.Provider != "deepseek" {
		t.Fatalf("persisted profile provider = %q, want deepseek", conn.Provider)
	}
	if conn.Token != "secret" || !conn.PersistToken {
		t.Fatalf("persisted profile token/persist = %q/%v, want pasted API key persisted", conn.Token, conn.PersistToken)
	}
	if conn.TokenEnv != "" {
		t.Fatalf("persisted profile token_env = %q, want empty for pasted API key", conn.TokenEnv)
	}
	if cfg.ContextWindowTokens != 1048576 {
		t.Fatalf("persisted context window = %d, want 1048576", cfg.ContextWindowTokens)
	}
	if cfg.MaxOutputTok != 32768 {
		t.Fatalf("persisted max output = %d, want 32768", cfg.MaxOutputTok)
	}
	if cfg.ReasoningEffort != "high" || cfg.DefaultReasoningEffort != "high" {
		t.Fatalf("persisted reasoning effort/default = %q/%q, want high/high", cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
	}
	if !equalStrings(cfg.ReasoningLevels, []string{"none", "high", "max"}) {
		t.Fatalf("persisted reasoning levels = %#v, want none/high/max", cfg.ReasoningLevels)
	}
	rawConfig, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	raw := string(rawConfig)
	for _, forbidden := range []string{
		`"API"`,
		`"AuthType"`,
		`"HeaderKey"`,
		`"TokenEnv"`,
		`"DefaultReasoningEffort"`,
		`"ReasoningMode"`,
		`"Timeout"`,
		`"PersistToken"`,
		`"api":`,
		`"auth_type":`,
		`"header_key":`,
		`"token_env":`,
		`"default_reasoning_effort":`,
		`"reasoning_mode":`,
		`"timeout":`,
		`"persist_token":`,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("config contains redundant key %s", forbidden)
		}
	}
	for _, required := range []string{
		`"profiles": [`,
		`"id": "deepseek@default"`,
		`"id": "deepseek@default/deepseek/deepseek-v4-flash"`,
		`"alias": "deepseek/deepseek-v4-flash"`,
		`"profile_id": "deepseek@default"`,
		`"provider": "deepseek"`,
		`"model": "deepseek-v4-flash"`,
		`"base_url": "https://api.deepseek.com/anthropic"`,
		`"token": "secret"`,
		`"context_window_tokens": 1048576`,
		`"reasoning_effort": "high"`,
		`"max_output_tokens": 32768`,
	} {
		if !strings.Contains(raw, required) {
			t.Fatalf("config missing compact key %s", required)
		}
	}
}

func TestAdapterConnectWithTokenEnvDoesNotPersistTokenValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "connect-token-env-test",
		StoreDir:     root,
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "connect-token-env-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "env:DEEPSEEK_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg gatewayapp.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "deepseek/deepseek-v4-flash") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want deepseek/deepseek-v4-flash", doc.Models.Configs)
	}
	var conn gatewayapp.ModelProfileConfig
	for _, item := range doc.Models.Profiles {
		if strings.EqualFold(item.ID, cfg.ProfileID) {
			conn = item
			break
		}
	}
	if conn.ID == "" {
		t.Fatalf("persisted profiles = %#v, missing %q", doc.Models.Profiles, cfg.ProfileID)
	}
	if conn.Token != "" || conn.PersistToken {
		t.Fatalf("persisted profile token/persist = %q/%v, want no plaintext token for env auth", conn.Token, conn.PersistToken)
	}
	if conn.TokenEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("persisted profile token_env = %q, want DEEPSEEK_API_KEY", conn.TokenEnv)
	}
}

func TestAdapterCodeFreeModelHasNoReasoningLevels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "codefree-no-reasoning-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "codefree",
			API:      providers.APICodeFree,
			Model:    "GLM-5.1",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "codefree-no-reasoning-session", "surface", "codefree/glm-5.1")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	levels, err := driver.CompleteSlashArg(ctx, "model use codefree/glm-5.1", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use codefree alias) error = %v", err)
	}
	if len(levels) != 0 {
		t.Fatalf("codefree reasoning candidates = %#v, want empty", levels)
	}
}

func TestAdapterConnectCodeFreeUsesExistingOAuthCache(t *testing.T) {
	ctx := context.Background()
	credsPath := filepath.Join(t.TempDir(), "codefree.json")
	raw, err := json.Marshal(map[string]any{
		"encryptedApiKey": encryptCodeFreeAPIKeyForRuntimeTest(t, "cached-api-key"),
		"userId":          "272182",
		"sessionId":       "cached-session",
		"baseUrlSnapshot": "https://www.srdcloud.cn",
	})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.WriteFile(credsPath, raw, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	t.Setenv("CODEFREE_OAUTH_CREDS_PATH", credsPath)

	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "codefree-connect-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "codefree-connect-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider: "codefree",
		Model:    "GLM-4.7",
	})
	if err != nil {
		t.Fatalf("Connect(codefree) error = %v", err)
	}
	if status.ModelStatus.Provider != "codefree" {
		t.Fatalf("provider = %q, want codefree", status.ModelStatus.Provider)
	}
	if status.ModelStatus.Name != "GLM-4.7" {
		t.Fatalf("model name = %q, want GLM-4.7", status.ModelStatus.Name)
	}
}

func TestAdapterStatusIncludesContextUsageSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "status-usage-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider:            "ollama",
			API:                 providers.APIOllama,
			Model:               "llama3",
			ContextWindowTokens: 88000,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "status-usage-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleAssistant, "world")),
			Text:    "world",
			Invocation: &session.EventInvocation{
				Provider: "ollama",
				Model:    "llama3",
			},
			Meta: map[string]any{
				"provider":            "ollama",
				"model":               "llama3",
				"prompt_tokens":       12600,
				"cached_input_tokens": 9000,
				"completion_tokens":   200,
				"reasoning_tokens":    50,
				"total_tokens":        12800,
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Usage.TotalTokens <= 12600 {
		t.Fatalf("status.Usage.TotalTokens = %d, want provider baseline plus estimated delta", status.Usage.TotalTokens)
	}
	if status.Usage.ContextWindowTokens != 88000 {
		t.Fatalf("status.Usage.ContextWindowTokens = %d, want 88000", status.Usage.ContextWindowTokens)
	}
	if status.Usage.SessionInputTokens != 12600 || status.Usage.SessionCachedInputTokens != 9000 || status.Usage.SessionOutputTokens != 200 || status.Usage.SessionReasoningTokens != 50 || status.Usage.SessionTotalTokens != 12800 {
		t.Fatalf("session token usage = input %d cached %d output %d reasoning %d total %d", status.Usage.SessionInputTokens, status.Usage.SessionCachedInputTokens, status.Usage.SessionOutputTokens, status.Usage.SessionReasoningTokens, status.Usage.SessionTotalTokens)
	}
	if status.Usage.SessionUsageMain.PromptTokens != 12600 || status.Usage.SessionUsageMain.ReasoningTokens != 50 {
		t.Fatalf("main usage = %+v, want assistant usage", status.Usage.SessionUsageMain)
	}
	if len(status.Usage.SessionUsageByModel) != 1 {
		t.Fatalf("SessionUsageByModel = %#v, want one model row", status.Usage.SessionUsageByModel)
	}
	row := status.Usage.SessionUsageByModel[0]
	if row.Provider != "ollama" || row.Model != "llama3" || row.Usage.PromptTokens != 12600 || row.Usage.TotalTokens != 12800 {
		t.Fatalf("model usage row = %+v, want ollama/llama3 usage", row)
	}
}

func TestAdapterSessionTokenUsageDeduplicatesConsecutiveToolCallUsage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "status-usage-dedupe-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "status-usage-dedupe-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	for _, id := range []string{"call-1", "call-2"} {
		if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event: &session.Event{
				Type: session.EventTypeToolCall,
				Tool: &session.EventTool{
					ID:     id,
					Name:   "RUN_COMMAND",
					Kind:   "execute",
					Title:  "RUN_COMMAND",
					Status: "pending",
					Input:  map[string]any{"cmd": "pwd"},
				},
				Meta: modelUsageMetaForRuntimeTest(10, 3, 2, 12),
			},
		}); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", id, err)
		}
	}

	usage, err := driver.sessionTokenUsage(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("sessionTokenUsage() error = %v", err)
	}
	if usage.PromptTokens != 10 || usage.CachedInputTokens != 3 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want one model response counted once", usage)
	}
}

func TestSessionTokenUsageBreakdownNormalizesAnthropicCachedInput(t *testing.T) {
	t.Parallel()

	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	events := []*session.Event{{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &assistant,
		Invocation: &session.EventInvocation{
			Provider: "deepseek",
			Model:    "deepseek-v4-flash",
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"sdk": map[string]any{
					"usage": map[string]any{
						"provider":            "deepseek-anthropic",
						"prompt_tokens":       10,
						"cached_input_tokens": 100,
						"completion_tokens":   5,
						"total_tokens":        15,
					},
				},
			},
		},
	}}

	usage := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if usage.Main.PromptTokens != 110 || usage.Main.CachedInputTokens != 100 || usage.Main.CompletionTokens != 5 || usage.Main.TotalTokens != 115 {
		t.Fatalf("main usage = %+v, want Anthropic-style cached input counted in input/total", usage.Main)
	}
	row := usage.ByModel["deepseek\x00deepseek-v4-flash"]
	if row.Usage.PromptTokens != 110 || row.Usage.TotalTokens != 115 {
		t.Fatalf("by-model usage = %+v, want normalized cached input", row)
	}
}

func TestAdapterSessionTokenUsageBreakdownIncludesSubagentsAndAutoReview(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "status-usage-breakdown-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "status-usage-breakdown-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:    session.EventTypeAssistant,
			Text:    "main answer",
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleAssistant, "main answer")),
			Meta:    modelUsageMetaForRuntimeTest(10, 3, 2, 12, 1),
		},
	}); err != nil {
		t.Fatalf("AppendEvent(main) error = %v", err)
	}
	if _, err := stack.Sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: session.SessionRef{SessionID: activeSession.SessionID}, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[gateway.StateUsageAccounting] = map[string]any{
			"auto_review": map[string]any{
				"provider":            "deepseek-anthropic",
				"prompt_tokens":       7,
				"cached_input_tokens": 1,
				"completion_tokens":   2,
				"reasoning_tokens":    2,
				"total_tokens":        9,
			},
			"by_model": []any{map[string]any{
				"provider": "deepseek",
				"model":    "deepseek-v4-pro",
				"usage": map[string]any{
					"provider":            "deepseek-anthropic",
					"prompt_tokens":       7,
					"cached_input_tokens": 1,
					"completion_tokens":   2,
					"reasoning_tokens":    2,
					"total_tokens":        9,
				},
			}},
		}
		return next, nil
	}}); err != nil {
		t.Fatalf("UpdateState(auto-review usage) error = %v", err)
	}
	child, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            activeSession.AppName,
		UserID:             activeSession.UserID,
		Workspace:          session.WorkspaceRef{Key: activeSession.WorkspaceKey, CWD: activeSession.CWD},
		PreferredSessionID: "child-self-usage",
	})
	if err != nil {
		t.Fatalf("StartSession(child) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "self-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			AgentName:    "self",
			SessionID:    child.SessionID,
			DelegationID: "task-1",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(self) error = %v", err)
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: child.SessionRef,
		Event: &session.Event{
			Type:    session.EventTypeAssistant,
			Text:    "child answer",
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleAssistant, "child answer")),
			Meta:    modelUsageMetaForRuntimeTest(20, 4, 6, 26, 5),
		},
	}); err != nil {
		t.Fatalf("AppendEvent(child) error = %v", err)
	}
	explorer, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            activeSession.AppName,
		UserID:             activeSession.UserID,
		Workspace:          session.WorkspaceRef{Key: filepath.Join(activeSession.CWD, "explorer"), CWD: activeSession.CWD},
		PreferredSessionID: "child-explorer-usage",
	})
	if err != nil {
		t.Fatalf("StartSession(explorer) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "explorer-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			AgentName:    "explorer",
			SessionID:    explorer.SessionID,
			DelegationID: "task-2",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(explorer) error = %v", err)
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: explorer.SessionRef,
		Event: &session.Event{
			Type:    session.EventTypeAssistant,
			Text:    "explorer answer",
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleAssistant, "explorer answer")),
			Meta:    modelUsageMetaForRuntimeTest(30, 5, 4, 34, 2),
		},
	}); err != nil {
		t.Fatalf("AppendEvent(explorer) error = %v", err)
	}

	usage, err := driver.sessionTokenUsageBreakdown(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("sessionTokenUsageBreakdown() error = %v", err)
	}
	if usage.Main.PromptTokens != 10 || usage.Main.ReasoningTokens != 1 || usage.Main.TotalTokens != 12 {
		t.Fatalf("main usage = %+v, want parent model usage", usage.Main)
	}
	if usage.Subagents.PromptTokens != 50 || usage.Subagents.CachedInputTokens != 9 || usage.Subagents.ReasoningTokens != 7 || usage.Subagents.TotalTokens != 60 {
		t.Fatalf("subagent usage = %+v, want all child subagent usage", usage.Subagents)
	}
	if usage.AutoReview.PromptTokens != 8 || usage.AutoReview.ReasoningTokens != 2 || usage.AutoReview.TotalTokens != 10 {
		t.Fatalf("auto-review usage = %+v, want review usage", usage.AutoReview)
	}
	if usage.Total.PromptTokens != 68 || usage.Total.CachedInputTokens != 13 || usage.Total.CompletionTokens != 14 || usage.Total.ReasoningTokens != 10 || usage.Total.TotalTokens != 82 {
		t.Fatalf("total usage = %+v, want all buckets", usage.Total)
	}
	if len(usage.ByModel) != 1 {
		t.Fatalf("by-model usage = %#v, want one auto-review model row", usage.ByModel)
	}
	modelRow := usage.ByModel["deepseek\x00deepseek-v4-pro"]
	if modelRow.Provider != "deepseek" || modelRow.Model != "deepseek-v4-pro" || modelRow.Usage.PromptTokens != 8 || modelRow.Usage.TotalTokens != 10 {
		t.Fatalf("by-model row = %+v, want deepseek/deepseek-v4-pro usage", modelRow)
	}
}

func TestSessionTokenUsageBreakdownFromStateNormalizesAutoReviewAggregateProvider(t *testing.T) {
	state := map[string]any{
		gateway.StateUsageAccounting: map[string]any{
			"auto_review_provider": "deepseek",
			"auto_review_model":    "deepseek-v4-pro",
			"auto_review": map[string]any{
				"provider":            "deepseek-anthropic",
				"prompt_tokens":       7,
				"cached_input_tokens": 1,
				"completion_tokens":   2,
				"total_tokens":        10,
			},
		},
	}

	usage := sessionTokenUsageBreakdownFromState(state)
	if usage.AutoReview.PromptTokens != 8 || usage.AutoReview.CachedInputTokens != 1 || usage.AutoReview.TotalTokens != 10 {
		t.Fatalf("auto-review usage = %+v, want DeepSeek cached input folded into display input/total", usage.AutoReview)
	}
	row := usage.ByModel["deepseek\x00deepseek-v4-pro"]
	if row.Provider != "deepseek" || row.Model != "deepseek-v4-pro" || row.Usage.PromptTokens != 8 || row.Usage.TotalTokens != 10 {
		t.Fatalf("by-model row = %+v, want aggregate attribution", row)
	}
}

func TestSessionTokenUsageBreakdownFromEventsNormalizesProviderWithoutInvocation(t *testing.T) {
	events := []*session.Event{{
		Type: session.EventTypeAssistant,
		Meta: map[string]any{
			"caelis": map[string]any{
				"sdk": map[string]any{
					"provider": "deepseek",
					"usage": map[string]any{
						"provider":            "deepseek-anthropic",
						"prompt_tokens":       7,
						"cached_input_tokens": 1,
						"completion_tokens":   2,
						"total_tokens":        10,
					},
				},
			},
		},
	}}

	usage := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if usage.Main.PromptTokens != 8 || usage.Main.CachedInputTokens != 1 || usage.Main.TotalTokens != 10 {
		t.Fatalf("main usage = %+v, want provider-normalized DeepSeek usage without invocation", usage.Main)
	}
	row := usage.ByModel["deepseek\x00"]
	if row.Provider != "deepseek" || row.Usage.PromptTokens != 8 || row.Usage.TotalTokens != 10 {
		t.Fatalf("by-model usage = %#v, want provider-only DeepSeek attribution", usage.ByModel)
	}
}

func TestSessionTokenUsageBreakdownFromStatePrefersByModelAutoReviewRows(t *testing.T) {
	state := map[string]any{
		gateway.StateUsageAccounting: map[string]any{
			"auto_review_provider": "deepseek",
			"auto_review_model":    "deepseek-v4-pro",
			"auto_review": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
			"by_model": []any{map[string]any{
				"provider": "deepseek",
				"model":    "deepseek-v4-pro",
				"usage": map[string]any{
					"provider":            "deepseek-anthropic",
					"prompt_tokens":       7,
					"cached_input_tokens": 1,
					"completion_tokens":   2,
					"total_tokens":        9,
				},
			}, map[string]any{
				"category": "main",
				"provider": "deepseek",
				"model":    "deepseek-v4-pro",
				"usage": map[string]any{
					"prompt_tokens":     100,
					"completion_tokens": 100,
					"total_tokens":      200,
				},
			}},
		},
	}

	usage := sessionTokenUsageBreakdownFromState(state)
	if usage.AutoReview.PromptTokens != 8 || usage.AutoReview.TotalTokens != 10 {
		t.Fatalf("auto-review usage = %+v, want authoritative by_model row", usage.AutoReview)
	}
}

func TestAdapterDeleteModelRemovesConfiguredAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "slash-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "delete-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "alt-model",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := driver.DeleteModel(ctx, "ollama/alt-model"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "model del", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model del) error = %v", err)
	}
	for _, item := range candidates {
		if item.Value == "ollama/alt-model" {
			t.Fatalf("deleted alias still present in %#v", candidates)
		}
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.ModelStatus.Display == "ollama/alt-model" {
		t.Fatalf("status model = %q, want deleted alias removed", status.ModelStatus.Display)
	}
}

func TestAdapterDeleteOnlyModelClearsAliasCandidatesAndStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "delete-only-model-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "delete-only-model-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "llama3",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := driver.DeleteModel(ctx, "ollama/llama3"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("model use candidates = %#v, want empty after deleting only model", candidates)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.TrimSpace(status.ModelStatus.Display) != "" {
		t.Fatalf("status model = %q, want empty after deleting only model", status.ModelStatus.Display)
	}
}

func TestAdapterUseModelResolvesCaseInsensitiveAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "use-model-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "use-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		APIKey:   "secret",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	status, err := driver.UseModel(ctx, "minimax/minimax-m2.7-highspeed")
	if err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.ModelStatus.Display)); got != "minimax/minimax-m2.7-highspeed" {
		t.Fatalf("status model = %q, want minimax/minimax-m2.7-highspeed", status.ModelStatus.Display)
	}
}

func TestAdapterAgentRegistryAndControllerUse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	helperCommand := adapterACPHelperCommandForTest(t)
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "agent-driver-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "default",
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "copilot",
				Description: "ACP sidecar agent.",
				Command:     helperCommand,
				Args:        []string{"-test.run=^TestAdapterACPHelperProcess$", "--"},
				WorkDir:     workdir,
				Env: map[string]string{
					"CAELIS_ADAPTER_ACP_HELPER":         "1",
					"CAELIS_ADAPTER_ACP_REMOTE_SESSION": "driver-remote-session",
				},
			}},
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "agent-driver-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	agents, err := driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if !agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents() = %#v, want assembly-registered copilot", agents)
	}
	addCandidates, err := driver.CompleteSlashArg(ctx, "agent add", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent add) error = %v", err)
	}
	for _, want := range []string{"claude", "codex", "opencode", "codefree-o", "copilot", "grok"} {
		if !slashCandidatesHaveValue(addCandidates, want) {
			t.Fatalf("agent add candidates = %#v, want %q", addCandidates, want)
		}
	}
	if slashCandidatesHaveValue(addCandidates, "gemini") {
		t.Fatalf("agent add candidates = %#v, want gemini unsupported", addCandidates)
	}
	if slashCandidatesHaveValue(addCandidates, "--install claude") || slashCandidatesHaveValue(addCandidates, "--install codex") {
		t.Fatalf("agent add candidates = %#v, want no install variants", addCandidates)
	}
	installCandidates, err := driver.CompleteSlashArg(ctx, "agent install", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent install) error = %v", err)
	}
	for _, want := range []string{"claude", "codex"} {
		if !slashCandidatesHaveValue(installCandidates, want) {
			t.Fatalf("agent install candidates = %#v, want %q", installCandidates, want)
		}
	}
	for _, notInstallable := range []string{"opencode", "codefree-o", "copilot", "grok", "gemini"} {
		if slashCandidatesHaveValue(installCandidates, notInstallable) {
			t.Fatalf("agent install candidates = %#v, want no %q", installCandidates, notInstallable)
		}
	}

	status, err := driver.AddAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("AddAgent() error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("AddAgent() status = %#v, want no session participants", status)
	}
	agents, err = driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents(after add) error = %v", err)
	}
	if !agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents(after add) = %#v, want attached copilot", agents)
	}
	useCandidates, err := driver.CompleteSlashArg(ctx, "agent use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent use) error = %v", err)
	}
	if !slashCandidatesHaveValue(useCandidates, "local") || !slashCandidatesHaveValue(useCandidates, "copilot") {
		t.Fatalf("agent use candidates = %#v, want local and copilot", useCandidates)
	}

	status, err = driver.HandoffAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("HandoffAgent(copilot) error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.ControllerKind)); got != "acp" {
		t.Fatalf("controller kind after ACP handoff = %q, want acp", status.ControllerKind)
	}

	if _, err := driver.RemoveAgent(ctx, "copilot"); err == nil {
		t.Fatal("RemoveAgent(active copilot) error = nil, want use local first")
	}
	status, err = driver.HandoffAgent(ctx, "local")
	if err != nil {
		t.Fatalf("HandoffAgent(local) error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.ControllerKind)); got != "kernel" {
		t.Fatalf("controller kind after local handoff = %q, want kernel", status.ControllerKind)
	}

	removeCandidates, err := driver.CompleteSlashArg(ctx, "agent remove", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent remove) error = %v", err)
	}
	if len(removeCandidates) != 1 || removeCandidates[0].Value != "copilot" {
		t.Fatalf("agent remove candidates = %#v, want registered copilot", removeCandidates)
	}

	status, err = driver.RemoveAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("RemoveAgent(copilot) error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("RemoveAgent() status = %#v, want zero participants", status)
	}
	agents, err = driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents(after remove) error = %v", err)
	}
	if agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents(after remove) = %#v, want copilot removed", agents)
	}
}

func TestAdapterStartAgentSubagentRollsBackAttachmentOnPromptConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "agent-conflict-rollback-test",
			SessionID:    "agent-conflict-session",
			WorkspaceKey: "ws",
		},
		CWD:        t.TempDir(),
		Controller: session.ControllerBinding{Kind: session.ControllerKindKernel},
		Participants: []session.ParticipantBinding{{
			ID:        "side-existing",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			AgentName: "copilot",
			Label:     "@ari",
			SessionID: "remote-existing",
		}},
	}
	gw := &sideAgentRollbackGatewayService{
		session: activeSession,
		promptErr: &gateway.Error{
			Kind:    gateway.KindConflict,
			Code:    gateway.CodeActiveRunConflict,
			Message: "active participant run already in progress",
		},
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{
				Key: "ws",
				CWD: activeSession.CWD,
			},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return session.CloneSession(gw.session), nil
			},
		},
		Agent: AgentRuntimeDeps{
			ListFn: func() []ACPAgentInfo {
				return []ACPAgentInfo{{Name: "copilot", Description: "ACP sidecar agent."}}
			},
		},
	}, activeSession.SessionID, "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	imageRaw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeSession.CWD, "side.png"), imageRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = driver.StartAgentSubagent(ctx, "copilot", "  second prompt  ", []Attachment{{Name: "side.png", Offset: len([]rune("second "))}})
	if err == nil {
		t.Fatal("StartAgentSubagent() error = nil, want active run conflict")
	}
	var gwErr *gateway.Error
	if !gateway.As(err, &gwErr) || gwErr.Code != gateway.CodeActiveRunConflict {
		t.Fatalf("StartAgentSubagent() error = %v, want active run conflict", err)
	}
	if len(gw.attachReqs) != 1 {
		t.Fatalf("AttachParticipant calls = %d, want 1", len(gw.attachReqs))
	}
	if len(gw.promptReqs) != 1 || gw.promptReqs[0].Input != "second prompt" {
		t.Fatalf("PromptParticipant requests = %#v, want trimmed prompt", gw.promptReqs)
	}
	if got := gw.promptReqs[0].DisplayInput; got != "second [image #1] prompt" {
		t.Fatalf("PromptParticipant DisplayInput = %q, want image marker", got)
	}
	if parts := gw.promptReqs[0].ContentParts; len(parts) != 3 ||
		parts[0].Type != model.ContentPartText || parts[0].Text != "second " ||
		parts[1].Type != model.ContentPartImage || parts[1].FileName != "side.png" ||
		parts[2].Type != model.ContentPartText || parts[2].Text != "prompt" {
		t.Fatalf("PromptParticipant content parts = %#v, want text/image/text", parts)
	}
	if len(gw.detachReqs) != 1 || gw.detachReqs[0].ParticipantID != "side-new" {
		t.Fatalf("DetachParticipant requests = %#v, want rollback of new sidecar", gw.detachReqs)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 1 {
		t.Fatalf("AgentStatus().Participants = %#v, want only first sidecar after rollback", status.Participants)
	}
	if status.Participants[0].ID != "side-existing" || status.Participants[0].AgentName != "copilot" || !agenthandle.ContainsPoolName(strings.TrimPrefix(status.Participants[0].Label, "@")) {
		t.Fatalf("remaining participant = %#v, want original copilot sidecar with shared pool label", status.Participants[0])
	}
}

func TestAdapterStartAgentSubagentKeepsDynamicSidecarAttachedForFollowUp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "agent-follow-up-test",
			SessionID:    "agent-follow-up-session",
			WorkspaceKey: "ws",
		},
		CWD:        t.TempDir(),
		Controller: session.ControllerBinding{Kind: session.ControllerKindKernel},
	}
	gw := &sideAgentRollbackGatewayService{
		session: activeSession,
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{
				Key: "ws",
				CWD: activeSession.CWD,
			},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return session.CloneSession(gw.session), nil
			},
		},
		Agent: AgentRuntimeDeps{
			ListFn: func() []ACPAgentInfo {
				return []ACPAgentInfo{{Name: "copilot", Description: "ACP sidecar agent."}}
			},
		},
	}, activeSession.SessionID, "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	turn, err := driver.StartAgentSubagent(ctx, "copilot", "first prompt", nil)
	if err != nil {
		t.Fatalf("StartAgentSubagent() error = %v", err)
	}
	if turn != nil {
		t.Fatalf("StartAgentSubagent() turn = %#v, want nil fake turn", turn)
	}
	if len(gw.detachReqs) != 0 {
		t.Fatalf("DetachParticipant requests after first prompt = %#v, want persistent sidecar", gw.detachReqs)
	}
	if len(gw.session.Participants) != 1 || gw.session.Participants[0].ID != "side-new" || !agenthandle.ContainsPoolName(strings.TrimPrefix(gw.session.Participants[0].Label, "@")) {
		t.Fatalf("Participants after first prompt = %#v, want attached copilot sidecar with pool label", gw.session.Participants)
	}
	handle := gw.session.Participants[0].Label

	turn, err = driver.ContinueSubagent(ctx, handle, "follow up", nil)
	if err != nil {
		t.Fatalf("ContinueSubagent(%s) error = %v", handle, err)
	}
	if turn != nil {
		t.Fatalf("ContinueSubagent() turn = %#v, want nil fake turn", turn)
	}
	if len(gw.attachReqs) != 1 {
		t.Fatalf("AttachParticipant requests = %#v, want only initial attach", gw.attachReqs)
	}
	if got, want := len(gw.promptReqs), 2; got != want {
		t.Fatalf("PromptParticipant requests = %d, want %d", got, want)
	}
	if got := gw.promptReqs[1].ParticipantID; got != "side-new" {
		t.Fatalf("follow-up ParticipantID = %q, want side-new", got)
	}
	if got := gw.promptReqs[1].Input; got != "follow up" {
		t.Fatalf("follow-up input = %q, want trimmed prompt", got)
	}
}

func TestAdapterStatusUsesPersistedDefaultAliasOnStartup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "status-startup-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.Connect(gatewayapp.ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
		Token:    "secret",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	reloaded, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "status-startup-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, reloaded, "startup-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.ModelStatus.Display); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("status model = %q, want deepseek/deepseek-v4-pro", status.ModelStatus.Display)
	}
}

func TestAdapterStartupUsesRequestedSessionID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "lazy-session-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected startup driver to create an active session")
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.TrimSpace(status.Session.ID) == "" {
		t.Fatal("expected startup status to include active session id")
	}
	if status.Session.ID != activeSession.SessionID {
		t.Fatalf("status session = %q, want %q", status.Session.ID, activeSession.SessionID)
	}
	if status.Session.ID != "sticky-session" {
		t.Fatalf("session id = %q, want sticky-session from constructor hint", status.Session.ID)
	}
}

func TestAdapterStartupBindsRequestedSessionInsteadOfFreshOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "binding-reset-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	stale, err := stack.StartSession(ctx, "stale-session", "surface")
	if err != nil {
		t.Fatalf("StartSession(stale) error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.TrimSpace(status.Session.ID) == "" {
		t.Fatal("expected startup driver to bind the requested session")
	}
	if status.Session.ID != "sticky-session" {
		t.Fatalf("startup session = %q, want sticky-session", status.Session.ID)
	}
	if status.Session.ID == stale.SessionID {
		t.Fatalf("startup session = %q, want sticky-session instead of stale bound session", status.Session.ID)
	}
	binding, err := stack.KernelSessions().LookupBinding(gateway.BindingStateRequest{BindingKey: "surface"})
	if err != nil {
		t.Fatalf("LookupBinding(surface) error = %v", err)
	}
	current := binding.SessionRef
	if current.SessionID != status.Session.ID {
		t.Fatalf("current binding session = %q, want %q", current.SessionID, status.Session.ID)
	}
}

func TestAdapterStartupReusesExistingRequestedSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "startup-resume-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	existing, err := stack.StartSession(ctx, "sticky-session", "other-surface")
	if err != nil {
		t.Fatalf("StartSession(sticky-session) error = %v", err)
	}

	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.ID != existing.SessionID {
		t.Fatalf("status session = %q, want existing session %q", status.Session.ID, existing.SessionID)
	}
}

func TestNewAdapterForSessionBindsResolvedWorkspaceWithoutStarting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stackWorkspace := t.TempDir()
	clientWorkspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "adapter-session-bind-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: stackWorkspace,
		WorkspaceCWD: stackWorkspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	runtimeStack := gatewayAppStackForRuntimeTest(stack)
	runtimeStack.Session.StartFn = func(context.Context, string, string) (session.Session, error) {
		t.Fatal("StartFn should not be called for an already resolved session")
		return session.Session{}, nil
	}
	resolved := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "adapter-session-bind-test",
			SessionID:    "shared-session",
			WorkspaceKey: clientWorkspace,
		},
		CWD: clientWorkspace,
	}
	driver, err := NewAdapterForSession(ctx, runtimeStack, resolved, "acp", "")
	if err != nil {
		t.Fatalf("NewAdapterForSession() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("adapter has no active session")
	}
	if activeSession.SessionRef != resolved.SessionRef {
		t.Fatalf("active session ref = %#v, want %#v", activeSession.SessionRef, resolved.SessionRef)
	}
}

func TestAdapterCycleSessionModeUsesStartupSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "lazy-session-mode-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	startup, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected startup session")
	}
	status, err := driver.CycleSessionMode(ctx)
	if err != nil {
		t.Fatalf("CycleSessionMode() error = %v", err)
	}
	if strings.TrimSpace(status.Session.ID) == "" {
		t.Fatal("expected CycleSessionMode() to keep an active session")
	}
	if status.Session.ID != startup.SessionID {
		t.Fatalf("session id = %q, want startup session %q", status.Session.ID, startup.SessionID)
	}
	if status.Session.SessionMode != "manual" {
		t.Fatalf("session mode = %q, want manual", status.Session.SessionMode)
	}
}

func TestAdapterSetSessionModeUpdatesLocalApprovalModeUnderACPController(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acp-approval-mode-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "acp-approval-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "codex",
			Label:           "Codex ACP",
			RemoteSessionID: "remote-1",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	driver := &Adapter{
		stack:              gatewayAppStackForRuntimeTest(stack),
		session:            activeSession,
		hasSession:         true,
		bindingKey:         "surface",
		defaultSessionMode: "auto-review",
		sessionMode:        "auto-review",
		defaultSandboxType: "host",
		sandboxType:        "host",
	}

	status, err := driver.SetSessionMode(ctx, "manual")
	if err != nil {
		t.Fatalf("SetSessionMode(manual) error = %v", err)
	}
	if status.Session.SessionMode != "manual" {
		t.Fatalf("status.Session.SessionMode = %q, want manual", status.Session.SessionMode)
	}
	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("state.SessionMode = %q, want manual", state.SessionMode)
	}
	status, err = driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.SessionMode != "manual" {
		t.Fatalf("Status().SessionMode = %q, want manual", status.Session.SessionMode)
	}
}

func TestAdapterCycleSessionModeUpdatesRemoteACPControllerMode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws"}
	activeSession := session.Session{
		SessionRef: ref,
		CWD:        t.TempDir(),
		Controller: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			AgentName:       "codex",
			RemoteSessionID: "remote-1",
		},
	}
	remoteStatus := gatewayapp.ACPControllerStatus{
		SessionRef: activeSession.SessionRef,
		Agent:      "codex",
		Model:      "remote-model",
		Mode:       "ask",
		ModeOptions: []gatewayapp.ACPControllerMode{
			{ID: "ask", Name: "Ask"},
			{ID: "code", Name: "Code"},
		},
	}
	var localCycleCalled bool
	var setRemoteMode string
	driver := &Adapter{
		stack: &RuntimeStack{
			Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{CWD: activeSession.CWD}},
			Gateway: gatewayRuntimeDepsForTest(&activeSubmitGatewayService{}),
			Status: StatusRuntimeDeps{
				RuntimeStateFn: func(context.Context, session.SessionRef) (SessionRuntimeState, error) {
					return SessionRuntimeState{ModelAlias: "local/model", SessionMode: "auto-review"}, nil
				},
				CycleModeFn: func(context.Context, session.SessionRef) (string, error) {
					localCycleCalled = true
					return "manual", nil
				},
			},
			Agent: AgentRuntimeDeps{
				ControllerStatusFn: func(context.Context, session.SessionRef) (gatewayapp.ACPControllerStatus, bool, error) {
					return remoteStatus, true, nil
				},
				SetControllerModeFn: func(_ context.Context, ref session.SessionRef, mode string) (gatewayapp.ACPControllerStatus, error) {
					if ref.SessionID != activeSession.SessionID {
						t.Fatalf("SetACPControllerMode ref = %#v, want session %q", ref, activeSession.SessionID)
					}
					setRemoteMode = mode
					remoteStatus.Mode = mode
					return remoteStatus, nil
				},
			},
		},
		session:            activeSession,
		hasSession:         true,
		bindingKey:         "surface",
		defaultSessionMode: "auto-review",
		sessionMode:        "auto-review",
		defaultSandboxType: "host",
		sandboxType:        "host",
	}

	status, err := driver.CycleSessionMode(ctx)
	if err != nil {
		t.Fatalf("CycleSessionMode() error = %v", err)
	}
	if localCycleCalled {
		t.Fatal("CycleSessionMode() called local session mode cycle under an active ACP controller")
	}
	if setRemoteMode != "code" {
		t.Fatalf("remote mode set to %q, want code", setRemoteMode)
	}
	if status.Session.SessionMode != "code" || status.Session.ModeLabel != "Code" {
		t.Fatalf("status mode = %q/%q, want code/Code", status.Session.SessionMode, status.Session.ModeLabel)
	}
}

func TestNextACPControllerModeUsesDeclaredModeOrder(t *testing.T) {
	t.Parallel()

	status := gatewayapp.ACPControllerStatus{
		Mode: "default",
		ModeOptions: []gatewayapp.ACPControllerMode{
			{ID: "default", Name: "Default"},
			{ID: "review", Name: "Review"},
			{ID: "plan", Name: "Plan"},
		},
	}
	next, err := nextACPControllerMode(status)
	if err != nil {
		t.Fatalf("nextACPControllerMode() error = %v", err)
	}
	if next.ID != "review" {
		t.Fatalf("next mode = %#v, want review", next)
	}

	status.Mode = "Plan"
	next, err = nextACPControllerMode(status)
	if err != nil {
		t.Fatalf("nextACPControllerMode(name) error = %v", err)
	}
	if next.ID != "default" {
		t.Fatalf("next mode from name = %#v, want default", next)
	}
}

func TestACPControllerModeDisplayPrefersDeclaredName(t *testing.T) {
	t.Parallel()

	status := gatewayapp.ACPControllerStatus{
		Mode: "review",
		ModeOptions: []gatewayapp.ACPControllerMode{
			{ID: "review", Name: "Review"},
		},
	}
	if got := acpControllerModeDisplay(status); got != "Review" {
		t.Fatalf("acpControllerModeDisplay() = %q, want Review", got)
	}
	status.ModeOptions = nil
	if got := acpControllerModeDisplay(status); got != "review" {
		t.Fatalf("acpControllerModeDisplay() fallback = %q, want review", got)
	}
}

func TestAdapterACPStatusPrefersRemoteModeOverLocalSessionMode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws"}
	activeSession := session.Session{
		SessionRef: ref,
		CWD:        t.TempDir(),
		Controller: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			AgentName:       "opencode",
			RemoteSessionID: "remote-1",
		},
	}
	driver := &Adapter{
		stack: &RuntimeStack{
			Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{CWD: activeSession.CWD}},
			Gateway: gatewayRuntimeDepsForTest(&activeSubmitGatewayService{}),
			Model: ModelRuntimeDeps{
				DefaultAliasFn: func() string { return "local/model" },
			},
			Status: StatusRuntimeDeps{
				RuntimeStateFn: func(context.Context, session.SessionRef) (SessionRuntimeState, error) {
					return SessionRuntimeState{ModelAlias: "local/model", SessionMode: "local-default"}, nil
				},
			},
			Agent: AgentRuntimeDeps{
				ControllerStatusFn: func(context.Context, session.SessionRef) (gatewayapp.ACPControllerStatus, bool, error) {
					return gatewayapp.ACPControllerStatus{
						Model: "remote-model",
						Mode:  "code",
						ModeOptions: []gatewayapp.ACPControllerMode{
							{ID: "code", Name: "Code"},
						},
					}, true, nil
				},
			},
		},
		session:            activeSession,
		hasSession:         true,
		defaultSessionMode: "local-default",
		sessionMode:        "local-default",
		defaultSandboxType: "host",
		sandboxType:        "host",
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.SessionMode != "code" || status.Session.ModeLabel != "Code" {
		t.Fatalf("status mode/label = %q/%q, want remote code/Code", status.Session.SessionMode, status.Session.ModeLabel)
	}
	if status.ModelStatus.Provider != "acp" || status.ModelStatus.Display != "remote-model" {
		t.Fatalf("status provider/model = %q/%q, want acp/remote-model", status.ModelStatus.Provider, status.ModelStatus.Display)
	}
}

func TestAdapterACPStatusKeepsAgentFallbackWithoutRemoteModel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acp-model-fallback-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "minimax",
			Model:    "MiniMax-M2",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "acp-fallback-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "codex",
			Label:           "Codex ACP",
			RemoteSessionID: "remote-1",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	driver := &Adapter{
		stack:              gatewayAppStackForRuntimeTest(stack),
		session:            activeSession,
		hasSession:         true,
		bindingKey:         "surface",
		defaultSessionMode: "default",
		sessionMode:        "default",
		defaultSandboxType: "host",
		sandboxType:        "host",
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.ModelStatus.Provider != "acp" {
		t.Fatalf("provider = %q, want acp", status.ModelStatus.Provider)
	}
	if status.ModelStatus.Display != "Codex ACP" {
		t.Fatalf("model = %q, want ACP agent fallback instead of local model", status.ModelStatus.Display)
	}
}

func TestAdapterIgnoresStaleSessionAliasOutsideConfiguredModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "stale-session-alias-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "stale-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	activeSession, err := driver.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := stack.Sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: session.SessionRef{SessionID: activeSession.SessionID}, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next["gateway.current_model_alias"] = "minimax/minimax-m2.7-highspeed"
		return next, nil
	}}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.ModelStatus.Display); got != "" {
		t.Fatalf("status model = %q, want empty because alias is stale", status.ModelStatus.Display)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	for _, item := range candidates {
		if strings.EqualFold(strings.TrimSpace(item.Value), "minimax/minimax-m2.7-highspeed") {
			t.Fatalf("stale session alias leaked into candidates: %#v", candidates)
		}
	}
}

func TestAdapterCompleteSlashArgUsesPrefixMatching(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "prefix-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "prefix-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	modelActions, err := driver.CompleteSlashArg(ctx, "model", "de", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model, de) error = %v", err)
	}
	if len(modelActions) != 1 || modelActions[0].Value != "del" {
		t.Fatalf("model action candidates = %#v, want only del", modelActions)
	}

	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		TokenEnv: "DEEPSEEK_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	modelAliases, err := driver.CompleteSlashArg(ctx, "model use", "dee", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use, dee) error = %v", err)
	}
	if len(modelAliases) == 0 || modelAliases[0].Display != "deepseek/deepseek-v4-pro" {
		t.Fatalf("model alias candidates = %#v, want deepseek/deepseek-v4-pro first", modelAliases)
	}
	deepseekLevels, err := driver.CompleteSlashArg(ctx, "model use deepseek/deepseek-v4-pro", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use deepseek alias) error = %v", err)
	}
	if got := candidateValues(deepseekLevels); !equalStrings(got, []string{"none", "high", "max"}) {
		t.Fatalf("deepseek reasoning candidates = %#v, want none/high/max", deepseekLevels)
	}
}

func TestAdapterCompleteSlashArgAgentRootOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "agent-root-order-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "agent-root-order-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "agent", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent) error = %v", err)
	}
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.Value)
	}
	want := []string{"use", "add", "install", "list", "remove"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent root candidates = %#v, want %#v", got, want)
	}
}

func TestAdapterCompleteSlashArgPluginRootOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "plugin-root-order-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "plugin-root-order-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "plugin", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(plugin) error = %v", err)
	}
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.Value)
	}
	want := []string{"install", "marketplace", "manage", "rm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugin root candidates = %#v, want %#v", got, want)
	}
}

func TestAdapterCompleteSlashArgPluginMarketplace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Plugin: PluginRuntimeDeps{
			ListMarketplacesFn: func(context.Context) ([]MarketplaceSnapshot, error) {
				return []MarketplaceSnapshot{
					{Name: "demo-market", Description: "Demo marketplace", Source: "acme/plugins", PluginCount: 2},
					{Name: "internal", Description: "Internal plugins", Source: "/tmp/internal", PluginCount: 1},
				}, nil
			},
		},
	}, "", "", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	actions, err := driver.CompleteSlashArg(ctx, "plugin marketplace", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(plugin marketplace) error = %v", err)
	}
	if got := candidateValues(actions); !equalStrings(got, []string{"add", "list", "update", "rm"}) {
		t.Fatalf("plugin marketplace actions = %#v, want add/list/update/rm", actions)
	}

	filtered, err := driver.CompleteSlashArg(ctx, "plugin marketplace", "up", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(plugin marketplace, up) error = %v", err)
	}
	if got := candidateValues(filtered); !equalStrings(got, []string{"update"}) {
		t.Fatalf("plugin marketplace filtered actions = %#v, want update", filtered)
	}

	names, err := driver.CompleteSlashArg(ctx, "plugin marketplace update", "de", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(plugin marketplace update) error = %v", err)
	}
	if got := candidateValues(names); !equalStrings(got, []string{"demo-market"}) {
		t.Fatalf("plugin marketplace update candidates = %#v, want demo-market", names)
	}
	if len(names) != 1 || !strings.Contains(names[0].Detail, "2 plugins") {
		t.Fatalf("plugin marketplace update detail = %#v, want plugin count", names)
	}

	removeNames, err := driver.CompleteSlashArg(ctx, "plugin marketplace rm", "in", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(plugin marketplace rm) error = %v", err)
	}
	if got := candidateValues(removeNames); !equalStrings(got, []string{"internal"}) {
		t.Fatalf("plugin marketplace rm candidates = %#v, want internal", removeNames)
	}
}

func TestAdapterInterruptCancelsAgentInstall(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Agent: AgentRuntimeDeps{
			RegisterBuiltinWithOptionsFn: func(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
				if target != "claude" || !opts.Install {
					return errors.New("unexpected install request")
				}
				close(started)
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := driver.AddAgentWithOptions(ctx, "claude", AgentAddOptions{Install: true})
		done <- err
	}()

	select {
	case <-started:
	case err := <-done:
		t.Fatalf("AddAgentWithOptions returned before install started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("install did not start")
	}
	if err := driver.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("AddAgentWithOptions error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AddAgentWithOptions did not return after Interrupt")
	}
}

func TestAdapterConnectPersistsMultipleProviders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "multi-provider-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "multi-provider-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		APIKey:   "minimax-secret",
	}); err != nil {
		t.Fatalf("Connect(minimax) error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		APIKey:   "deepseek-secret",
	}); err != nil {
		t.Fatalf("Connect(deepseek) error = %v", err)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("model use candidates = %#v, want both providers", candidates)
	}
	if candidates[0].Display != "deepseek/deepseek-v4-pro" {
		t.Fatalf("first candidate display = %q, want deepseek/deepseek-v4-pro", candidates[0].Display)
	}
	foundMinimax := false
	for _, candidate := range candidates {
		if candidate.Display == "minimax/minimax-m2.7-highspeed" {
			foundMinimax = true
			break
		}
	}
	if !foundMinimax {
		t.Fatalf("model use candidates = %#v, missing minimax alias", candidates)
	}
}

func TestFindProviderTemplateSupportsOpenAICompatible(t *testing.T) {
	t.Parallel()

	tpl, ok := findProviderTemplate("openai-compatible")
	if !ok {
		t.Fatal("findProviderTemplate(openai-compatible) = false, want true")
	}
	if tpl.provider != "openai-compatible" {
		t.Fatalf("provider = %q, want openai-compatible", tpl.provider)
	}
	if tpl.defaultBaseURL == "" {
		t.Fatal("defaultBaseURL = empty, want non-empty")
	}
}

func TestFindProviderTemplateSupportsXiaomiTokenPlanCN(t *testing.T) {
	t.Parallel()

	tpl, ok := findProviderTemplate(connectXiaomiTokenPlanCNAlias)
	if !ok {
		t.Fatalf("findProviderTemplate(%q) = false, want true", connectXiaomiTokenPlanCNAlias)
	}
	if tpl.provider != "xiaomi" {
		t.Fatalf("provider = %q, want xiaomi", tpl.provider)
	}
	if tpl.api != providers.APIMimo {
		t.Fatalf("api = %q, want %q", tpl.api, providers.APIMimo)
	}
	if tpl.defaultBaseURL != connectXiaomiTokenPlanCNBaseURL {
		t.Fatalf("defaultBaseURL = %q, want %q", tpl.defaultBaseURL, connectXiaomiTokenPlanCNBaseURL)
	}
}

func TestFindProviderTemplateRejectsMimoProviderAliases(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"mimo", "mimo-token-plan-cn"} {
		if tpl, ok := findProviderTemplate(provider); ok {
			t.Fatalf("findProviderTemplate(%q) = %#v, want unsupported", provider, tpl)
		}
	}
}

func TestValidateConnectConfigXiaomiTokenPlanCNUsesTokenPlanEnvHint(t *testing.T) {
	t.Parallel()

	tpl, ok := findProviderTemplate("xiaomi")
	if !ok {
		t.Fatal("findProviderTemplate(xiaomi) = false, want true")
	}
	err := validateConnectConfig(tpl, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  connectXiaomiTokenPlanCNBaseURL,
	})
	if err == nil || !strings.Contains(err.Error(), "env:MIMO_TOKEN_PLAN_API_KEY") {
		t.Fatalf("validateConnectConfig() error = %v, want MIMO_TOKEN_PLAN_API_KEY hint", err)
	}
}

func TestAdapterConnectXiaomiTokenPlanCNStoresXiaomiProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "xiaomi-token-plan-connect-test",
		StoreDir:     root,
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "xiaomi-token-plan-connect-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  connectXiaomiTokenPlanCNBaseURL,
		APIKey:   "env:MIMO_TOKEN_PLAN_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg gatewayapp.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "xiaomi/mimo-v2.5-pro") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want xiaomi alias", doc.Models.Configs)
	}
	if cfg.ID != "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro" {
		t.Fatalf("persisted model id = %q, want readable profile/model alias id", cfg.ID)
	}
	if cfg.ProfileID != "xiaomi@token-plan-cn" {
		t.Fatalf("persisted profile id = %q, want xiaomi@token-plan-cn", cfg.ProfileID)
	}
	if cfg.Provider != "" || cfg.BaseURL != "" || cfg.Token != "" || cfg.TokenEnv != "" {
		t.Fatalf("persisted model leaked profile fields: %#v", cfg)
	}
	var profile gatewayapp.ModelProfileConfig
	for _, item := range doc.Models.Profiles {
		if strings.EqualFold(item.ID, cfg.ProfileID) {
			profile = item
			break
		}
	}
	if profile.ID == "" {
		t.Fatalf("persisted profiles = %#v, missing %q", doc.Models.Profiles, cfg.ProfileID)
	}
	if profile.Provider != "xiaomi" {
		t.Fatalf("profile provider = %q, want xiaomi", profile.Provider)
	}
	if profile.BaseURL != connectXiaomiTokenPlanCNBaseURL {
		t.Fatalf("profile base_url = %q, want %q", profile.BaseURL, connectXiaomiTokenPlanCNBaseURL)
	}
	if profile.TokenEnv != "MIMO_TOKEN_PLAN_API_KEY" {
		t.Fatalf("profile token_env = %q, want MIMO_TOKEN_PLAN_API_KEY", profile.TokenEnv)
	}
}

func TestAdapterConnectXiaomiEndpointsCoexistUnderVisibleAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "xiaomi-endpoint-coexist-test",
		StoreDir:     root,
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "xiaomi-endpoint-coexist-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	for _, cfg := range []ConnectConfig{
		{Provider: "xiaomi", Model: "mimo-v2.5-pro", BaseURL: connectXiaomiAPIBaseURL, APIKey: "env:XIAOMI_API_KEY"},
		{Provider: "xiaomi", Model: "mimo-v2.5-pro", BaseURL: connectXiaomiTokenPlanCNBaseURL, APIKey: "env:MIMO_TOKEN_PLAN_API_KEY"},
	} {
		if _, err := driver.Connect(ctx, cfg); err != nil {
			t.Fatalf("Connect(%s) error = %v", cfg.BaseURL, err)
		}
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var sameAlias int
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "xiaomi/mimo-v2.5-pro") {
			sameAlias++
		}
	}
	if sameAlias != 2 {
		t.Fatalf("persisted configs = %#v, want two xiaomi/mimo-v2.5-pro bindings", doc.Models.Configs)
	}
	if len(doc.Models.Profiles) != 2 {
		t.Fatalf("persisted profiles = %#v, want two endpoint profiles", doc.Models.Profiles)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "xiaomi/mimo-v2.5-pro", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	var apiCandidate, tokenPlanCandidate SlashArgCandidate
	for _, candidate := range candidates {
		if candidate.Display != "xiaomi/mimo-v2.5-pro" {
			continue
		}
		switch {
		case strings.Contains(candidate.Detail, "api-cn"):
			apiCandidate = candidate
		case strings.Contains(candidate.Detail, "token-plan-cn"):
			tokenPlanCandidate = candidate
		}
	}
	if apiCandidate.Value == "" || tokenPlanCandidate.Value == "" || apiCandidate.Value == tokenPlanCandidate.Value {
		t.Fatalf("model use candidates = %#v, want distinct hidden ids for both endpoints", candidates)
	}
	if apiCandidate.Value != "xiaomi@api-cn/xiaomi/mimo-v2.5-pro" {
		t.Fatalf("api candidate value = %q, want readable api profile/model id", apiCandidate.Value)
	}
	if tokenPlanCandidate.Value != "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro" {
		t.Fatalf("token-plan candidate value = %q, want readable token-plan profile/model id", tokenPlanCandidate.Value)
	}
	if _, err := driver.UseModel(ctx, "xiaomi/mimo-v2.5-pro"); err == nil || !strings.Contains(err.Error(), "ambiguous model alias") {
		t.Fatalf("UseModel(visible alias) error = %v, want ambiguity", err)
	}
	if _, err := driver.UseModel(ctx, tokenPlanCandidate.Value); err != nil {
		t.Fatalf("UseModel(token-plan hidden id) error = %v", err)
	}
}

func TestAdapterConnectReusesExistingEndpointAuth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "connect-reuse-auth-test",
		StoreDir:     root,
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "connect-reuse-auth-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  connectXiaomiAPIBaseURL,
		APIKey:   "env:XIAOMI_API_KEY",
	}); err != nil {
		t.Fatalf("Connect(first model) error = %v", err)
	}
	endpoints, err := driver.CompleteSlashArg(ctx, "connect-baseurl:xiaomi", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-baseurl:xiaomi) error = %v", err)
	}
	var foundReusable bool
	for _, endpoint := range endpoints {
		if endpoint.Value == connectXiaomiAPIBaseURL && endpoint.NoAuth && strings.Contains(endpoint.Detail, "configured auth") {
			foundReusable = true
			break
		}
	}
	if !foundReusable {
		t.Fatalf("endpoint candidates = %#v, want reusable auth marker for api cn", endpoints)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2-pro",
		BaseURL:  connectXiaomiAPIBaseURL,
	}); err != nil {
		t.Fatalf("Connect(second model without key) error = %v", err)
	}
	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Models.Profiles) != 1 {
		t.Fatalf("persisted profiles = %#v, want one shared profile", doc.Models.Profiles)
	}
	if got := doc.Models.Profiles[0].TokenEnv; got != "XIAOMI_API_KEY" {
		t.Fatalf("shared profile token_env = %q, want XIAOMI_API_KEY", got)
	}
}

func TestConnectDefaultsForConfigOpenAICompatibleCustomBaseURL(t *testing.T) {
	t.Parallel()

	defaults, err := connectDefaultsForConfig(context.Background(), ConnectConfig{
		Provider: "openai-compatible",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://proxy.example.test/v1",
	})
	if err != nil {
		t.Fatalf("connectDefaultsForConfig() error = %v", err)
	}
	if defaults.ContextWindow <= 0 {
		t.Fatalf("ContextWindow = %d, want > 0", defaults.ContextWindow)
	}
	if defaults.MaxOutput <= 0 {
		t.Fatalf("MaxOutput = %d, want > 0", defaults.MaxOutput)
	}
}

func TestAdapterCompleteFileUsesRelativePathsAndSkipsNoise(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "src", "pkg"), 0o700); err != nil {
		t.Fatalf("MkdirAll(src/pkg) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "node_modules", "leftpad"), 0o700); err != nil {
		t.Fatalf("MkdirAll(node_modules) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "objects"), 0o700); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(workspace, "src", "main.go"),
		filepath.Join(workspace, "src", "pkg", "helper.go"),
		filepath.Join(workspace, "node_modules", "leftpad", "index.js"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "file-complete-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "file-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	candidates, err := driver.CompleteFile(ctx, "src/ma", 10)
	if err != nil {
		t.Fatalf("CompleteFile() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("CompleteFile() returned no candidates, want src/main.go")
	}
	if got := candidates[0].Value; got != "src/main.go" {
		t.Fatalf("first candidate value = %q, want src/main.go", got)
	}

	all, err := driver.CompleteFile(ctx, "", 20)
	if err != nil {
		t.Fatalf("CompleteFile(all) error = %v", err)
	}
	for _, item := range all {
		if strings.Contains(item.Value, "node_modules") || strings.Contains(item.Value, ".git") {
			t.Fatalf("noise directory leaked into candidates: %#v", all)
		}
	}
}

func TestAdapterCompleteSkillDiscoversGlobalAndWorkspaceSkills(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	setHomeForAdapterTest(t, home)

	globalSkill := filepath.Join(home, ".agents", "skills", "echo")
	workspaceSkill := filepath.Join(workspace, ".agents", "skills", "lint")
	for _, dir := range []string{globalSkill, workspaceSkill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(globalSkill, "SKILL.md"), []byte("---\nname: echo\ndescription: Echo text.\n---\n# Echo\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(global SKILL.md) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceSkill, "SKILL.md"), []byte("---\nname: lint\ndescription: Run lint checks.\n---\n# Lint\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(workspace SKILL.md) error = %v", err)
	}

	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "skill-complete-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		SkillDirs:    gatewayapp.DefaultSkillDiscoveryDirs(workspace),
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "skill-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	candidates, err := driver.CompleteSkill(ctx, "", 10)
	if err != nil {
		t.Fatalf("CompleteSkill() error = %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("CompleteSkill() = %#v, want global and workspace skills", candidates)
	}
	foundEcho := false
	foundLint := false
	for _, item := range candidates {
		switch item.Value {
		case "echo":
			foundEcho = item.Kind == "Skill" && strings.Contains(item.Detail, "Echo text") && strings.TrimSpace(item.Path) != ""
		case "lint":
			foundLint = item.Kind == "Skill" && strings.Contains(item.Detail, "Run lint checks") && strings.TrimSpace(item.Path) != ""
		}
	}
	if !foundEcho || !foundLint {
		t.Fatalf("CompleteSkill() = %#v, want echo and lint metadata", candidates)
	}
}

func TestAdapterCompleteSkillUsesRuntimeSnapshot(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	setHomeForAdapterTest(t, home)

	initialSkill := filepath.Join(workspace, ".agents", "skills", "initial")
	if err := os.MkdirAll(initialSkill, 0o700); err != nil {
		t.Fatalf("MkdirAll(initial skill) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(initialSkill, "SKILL.md"), []byte("---\nname: initial\ndescription: Initial skill.\n---\n# Initial\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(initial SKILL.md) error = %v", err)
	}

	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "skill-snapshot-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		SkillDirs:    gatewayapp.DefaultSkillDiscoveryDirs(workspace),
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	lateSkill := filepath.Join(workspace, ".agents", "skills", "late")
	if err := os.MkdirAll(lateSkill, 0o700); err != nil {
		t.Fatalf("MkdirAll(late skill) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(lateSkill, "SKILL.md"), []byte("---\nname: late\ndescription: Late skill.\n---\n# Late\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(late SKILL.md) error = %v", err)
	}

	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "skill-snapshot-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	candidates, err := driver.CompleteSkill(ctx, "", 10)
	if err != nil {
		t.Fatalf("CompleteSkill() error = %v", err)
	}
	foundInitial := false
	for _, item := range candidates {
		switch item.Value {
		case "initial":
			foundInitial = true
		case "late":
			t.Fatalf("CompleteSkill() = %#v, should not include skill added after runtime snapshot", candidates)
		}
	}
	if !foundInitial {
		t.Fatalf("CompleteSkill() = %#v, want initial skill from runtime snapshot", candidates)
	}
}

func TestAdapterCompleteSkillRefreshesAfterRuntimeRebuild(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	setHomeForAdapterTest(t, home)

	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "skill-reconfigure-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		SkillDirs:    gatewayapp.DefaultSkillDiscoveryDirs(workspace),
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "skill-reconfigure-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	initialCandidates, err := driver.CompleteSkill(ctx, "", 10)
	if err != nil {
		t.Fatalf("CompleteSkill() before plugin add error = %v", err)
	}
	if completionCandidatesContainValue(initialCandidates, "skillplugin:runtime-skill") {
		t.Fatalf("CompleteSkill() before plugin add = %#v, should not include plugin skill", initialCandidates)
	}

	pluginDir := filepath.Join(t.TempDir(), "skillplugin")
	writeAdapterTestPluginSkill(t, pluginDir, "runtime-skill", "Runtime plugin skill.")
	if _, err := stack.Plugins().AddPath(ctx, pluginDir); err != nil {
		t.Fatalf("Plugins().AddPath() error = %v", err)
	}

	refreshedCandidates, err := driver.CompleteSkill(ctx, "", 10)
	if err != nil {
		t.Fatalf("CompleteSkill() after plugin add error = %v", err)
	}
	if !completionCandidatesContainValue(refreshedCandidates, "skillplugin:runtime-skill") {
		t.Fatalf("CompleteSkill() after runtime rebuild = %#v, want plugin skill", refreshedCandidates)
	}
}

func writeAdapterTestPluginSkill(t *testing.T, root string, name string, description string) {
	t.Helper()
	manifestDir := filepath.Join(root, ".caelis-plugin")
	skillDir := filepath.Join(root, "skills", name)
	for _, dir := range []string{manifestDir, skillDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(`{"name":"skill-plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(plugin.json) error = %v", err)
	}
	content := []byte("---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}
}

func completionCandidatesContainValue(candidates []CompletionCandidate, value string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Value) == value {
			return true
		}
	}
	return false
}

func TestSkillCompletionCandidatePrefersLocalNameForPluginSkill(t *testing.T) {
	workspace := t.TempDir()
	meta := skill.Meta{
		Name:        "superpowers-abc123:brainstorm",
		Description: "Generate alternatives before implementation.",
		Path:        filepath.Join(workspace, ".caelis", "plugins", "superpowers", "skills", "brainstorm", "SKILL.md"),
		Source:      skill.SourcePlugin,
		PluginID:    "superpowers-abc123",
		Namespace:   "superpowers-abc123",
		LocalName:   "brainstorm",
	}

	candidate := skillCompletionCandidate(meta)
	if candidate.Value != "superpowers-abc123:brainstorm" {
		t.Fatalf("candidate.Value = %q, want namespaced skill value", candidate.Value)
	}
	if candidate.Display != "brainstorm" {
		t.Fatalf("candidate.Display = %q, want local skill name", candidate.Display)
	}
	if candidate.Kind != "Plugin" {
		t.Fatalf("candidate.Kind = %q, want plugin badge", candidate.Kind)
	}
	if candidate.Detail != "superpowers-abc123 · Generate alternatives before implementation." {
		t.Fatalf("candidate.Detail = %q, want plugin source and skill description", candidate.Detail)
	}
	if strings.Contains(candidate.Detail, "namespace") {
		t.Fatalf("candidate.Detail = %q, should not include namespace hint", candidate.Detail)
	}
	if strings.Contains(candidate.Detail, "SKILL.md") {
		t.Fatalf("candidate.Detail = %q, should not include path metadata", candidate.Detail)
	}
	if _, ok := scoreSkillMeta("brainstorm", meta, workspace); !ok {
		t.Fatal("scoreSkillMeta(\"brainstorm\") did not match local skill name")
	}
	if _, ok := scoreSkillMeta("superpowers", meta, workspace); !ok {
		t.Fatal("scoreSkillMeta(\"superpowers\") did not match namespace")
	}
}

func TestAdapterCompleteMentionReturnsACPSidecarsOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "mention-complete-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "mention-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	activeSession, err := driver.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession() error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "side-1",
			Kind:         session.ParticipantKindACP,
			Role:         session.ParticipantRoleSidecar,
			AgentName:    "codex",
			Label:        "@jeff",
			SessionID:    "child-1",
			Source:       "custom_codex",
			DelegationID: "task-side",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(side) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "legacy-side-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleSidecar,
			AgentName:    "legacy",
			Label:        "@jill",
			SessionID:    "legacy-child-1",
			DelegationID: "task-legacy",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(legacy-side) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "task-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			Label:        "@jude",
			SessionID:    "child-2",
			DelegationID: "task-1",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(delegated) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "self-001",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			AgentName:    "self",
			Label:        "@jude",
			SessionID:    "self-child-1",
			DelegationID: "task-self",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(self) error = %v", err)
	}
	candidates, err := driver.CompleteMention(ctx, "j", 8)
	if err != nil {
		t.Fatalf("CompleteMention() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Value != "jeff" || candidates[0].Display != "jeff(codex)" {
		t.Fatalf("CompleteMention() = %#v, want side target", candidates)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 2 || status.Participants[0].ID != "side-1" || status.Participants[1].ID != "legacy-side-1" {
		t.Fatalf("AgentStatus().Participants = %#v, want visible side participants", status.Participants)
	}
	if len(status.DelegatedParticipants) != 2 || status.DelegatedParticipants[0].ID != "task-1" || status.DelegatedParticipants[1].ID != "self-001" {
		t.Fatalf("AgentStatus().DelegatedParticipants = %#v, want delegated task summary", status.DelegatedParticipants)
	}
}

func TestAdapterCompleteResumeUsesSummaryMetadataAndRecentFirst(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "resume-complete-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	first, err := stack.KernelSessions().StartSession(ctx, gateway.StartSessionRequest{
		AppName:    stack.AppName,
		UserID:     stack.UserID,
		Workspace:  stack.Workspace,
		Title:      "First Task",
		BindingKey: "first-binding",
	})
	if err != nil {
		t.Fatalf("StartSession(first) error = %v", err)
	}
	if _, err := stack.Sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: first.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		next[gateway.StateCurrentModelAlias] = "openai/gpt-4o-mini"
		return next, nil
	}}); err != nil {
		t.Fatalf("UpdateState(first) error = %v", err)
	}
	second, err := stack.KernelSessions().StartSession(ctx, gateway.StartSessionRequest{
		AppName:    stack.AppName,
		UserID:     stack.UserID,
		Workspace:  stack.Workspace,
		Title:      "Second Task",
		BindingKey: "second-binding",
	})
	if err != nil {
		t.Fatalf("StartSession(second) error = %v", err)
	}
	if _, err := stack.Sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: second.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		next[gateway.StateCurrentModelAlias] = "deepseek/deepseek-v4-flash"
		return next, nil
	}}); err != nil {
		t.Fatalf("UpdateState(second) error = %v", err)
	}

	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "resume-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	candidates, err := driver.CompleteResume(ctx, "task", 10)
	if err != nil {
		t.Fatalf("CompleteResume() error = %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("CompleteResume() = %#v, want at least two sessions", candidates)
	}
	if candidates[0].Title != "Second Task" {
		t.Fatalf("first resume candidate title = %q, want most recent Second Task", candidates[0].Title)
	}
	if candidates[0].Model != "" || candidates[0].Workspace == "" {
		t.Fatalf("first resume candidate = %#v, want summary workspace without history-loaded model", candidates[0])
	}
}

func TestAdapterDeleteModelRejectsUnknownAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "delete-unknown-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "delete-unknown-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if err := driver.DeleteModel(ctx, "minimax/minimax-m1"); err == nil {
		t.Fatal("DeleteModel() error = nil, want unknown alias error")
	}
}

func TestAdapterConnectModelCandidatesIncludeConfiguredProviderModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "connect-candidates-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "connect-candidates-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		APIKey:   "secret",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	models, err := driver.CompleteSlashArg(ctx, connectModelCompletionCommand(connectwizard.ConnectWizardState{
		Provider:       "minimax",
		BaseURL:        "https://api.minimaxi.com/anthropic",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
		TokenRef:       "secret",
	}), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model) error = %v", err)
	}
	found := false
	for _, item := range models {
		if item.Value == "MiniMax-M2.7-highspeed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("connect model candidates = %#v, want configured minimax model", models)
	}
}

func TestAdapterConnectRejectsMissingAPIKeyWithActionableError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "missing-key-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "missing-key-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "openai",
		Model:    "gpt-4o",
	}); err == nil || !strings.Contains(err.Error(), "env:OPENAI_API_KEY") {
		t.Fatalf("Connect() error = %v, want actionable env hint", err)
	}
}

func TestAdapterConnectRejectsInvalidBaseURL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "invalid-baseurl-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "invalid-baseurl-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "openai-compatible",
		Model:    "gpt-4o",
		BaseURL:  "not-a-url",
		APIKey:   "secret",
	}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "base url is invalid") {
		t.Fatalf("Connect() error = %v, want invalid base URL guidance", err)
	}
}

func TestAdapterStatusIncludesDoctorDiagnostics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "doctor-status-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "doctor-status-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		TokenEnv: "MINIMAX_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := driver.SetSessionMode(ctx, "manual"); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.StoreDir != root {
		t.Fatalf("status.Session.StoreDir = %q, want %q", status.Session.StoreDir, root)
	}
	if status.ModelStatus.Provider != "minimax" || status.ModelStatus.Name != "MiniMax-M2.7-highspeed" {
		t.Fatalf("status provider/model = %q/%q, want minimax/MiniMax-M2.7-highspeed", status.ModelStatus.Provider, status.ModelStatus.Name)
	}
	if !status.ModelStatus.MissingAPIKey {
		t.Fatal("status.ModelStatus.MissingAPIKey = false, want true when token env is unset")
	}
	if !status.SandboxStatus.HostExecution || status.SandboxStatus.FullAccessMode {
		t.Fatalf("status host/full_access = %v/%v, want true/false", status.SandboxStatus.HostExecution, status.SandboxStatus.FullAccessMode)
	}
}

type sideAgentRollbackGatewayService struct {
	activeSubmitGatewayService
	session    session.Session
	promptErr  error
	attachReqs []gateway.AttachParticipantRequest
	promptReqs []gateway.PromptParticipantRequest
	detachReqs []gateway.DetachParticipantRequest
}

func (g *sideAgentRollbackGatewayService) ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error) {
	participants := make([]gateway.ParticipantState, 0, len(g.session.Participants))
	for _, participant := range g.session.Participants {
		participants = append(participants, gateway.ParticipantState{
			ID:             participant.ID,
			Kind:           participant.Kind,
			Role:           participant.Role,
			AgentName:      participant.AgentName,
			Label:          participant.Label,
			SessionID:      participant.SessionID,
			Source:         participant.Source,
			ParentTurnID:   participant.ParentTurnID,
			DelegationID:   participant.DelegationID,
			ContextSyncSeq: participant.ContextSyncSeq,
			AttachedAt:     participant.AttachedAt,
			ControllerRef:  participant.ControllerRef,
		})
	}
	return gateway.ControlPlaneState{
		SessionRef: g.session.SessionRef,
		Controller: gateway.ControllerState{
			Kind:            g.session.Controller.Kind,
			ControllerID:    g.session.Controller.ControllerID,
			AgentName:       g.session.Controller.AgentName,
			Label:           g.session.Controller.Label,
			EpochID:         g.session.Controller.EpochID,
			RemoteSessionID: g.session.Controller.RemoteSessionID,
			ContextSyncSeq:  g.session.Controller.ContextSyncSeq,
			AttachedAt:      g.session.Controller.AttachedAt,
			Source:          g.session.Controller.Source,
		},
		Participants: participants,
	}, nil
}

func (g *sideAgentRollbackGatewayService) AttachParticipant(_ context.Context, req gateway.AttachParticipantRequest) (session.Session, error) {
	g.attachReqs = append(g.attachReqs, req)
	g.session.Participants = append(g.session.Participants, session.ParticipantBinding{
		ID:        "side-new",
		Kind:      session.ParticipantKindACP,
		Role:      req.Role,
		AgentName: req.Agent,
		Label:     req.Label,
		SessionID: "remote-new",
		Source:    req.Source,
	})
	return session.CloneSession(g.session), nil
}

func (g *sideAgentRollbackGatewayService) PromptParticipant(_ context.Context, req gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error) {
	g.promptReqs = append(g.promptReqs, req)
	return gateway.BeginTurnResult{}, g.promptErr
}

func (g *sideAgentRollbackGatewayService) StartParticipant(ctx context.Context, req gateway.StartParticipantRequest) (gateway.BeginTurnResult, error) {
	updated, err := g.AttachParticipant(ctx, gateway.AttachParticipantRequest{
		SessionRef: req.SessionRef,
		BindingKey: req.BindingKey,
		Agent:      req.Agent,
		Role:       req.Role,
		Source:     req.Source,
		Label:      req.Label,
	})
	if err != nil {
		return gateway.BeginTurnResult{}, err
	}
	result, err := g.PromptParticipant(ctx, gateway.PromptParticipantRequest{
		SessionRef:    updated.SessionRef,
		BindingKey:    req.BindingKey,
		ParticipantID: "side-new",
		Input:         req.Input,
		DisplayInput:  req.DisplayInput,
		DisplayTitle:  req.DisplayTitle,
		ContentParts:  req.ContentParts,
		Source:        req.Source,
	})
	if err != nil {
		if _, detachErr := g.DetachParticipant(ctx, gateway.DetachParticipantRequest{
			SessionRef:    updated.SessionRef,
			BindingKey:    req.BindingKey,
			ParticipantID: "side-new",
			Source:        "side_agent_prompt_rollback",
		}); detachErr != nil {
			return gateway.BeginTurnResult{}, errors.Join(err, detachErr)
		}
		return gateway.BeginTurnResult{}, err
	}
	if result.Session.SessionID == "" {
		result.Session = updated
	}
	if req.Lifecycle == gateway.ParticipantLifecycleTransient && result.Handle == nil {
		_, _ = g.DetachParticipant(ctx, gateway.DetachParticipantRequest{
			SessionRef:    updated.SessionRef,
			BindingKey:    req.BindingKey,
			ParticipantID: "side-new",
			Source:        firstNonEmpty(req.DetachSource, "side_agent_complete"),
		})
	}
	return result, nil
}

func (g *sideAgentRollbackGatewayService) DetachParticipant(_ context.Context, req gateway.DetachParticipantRequest) (session.Session, error) {
	g.detachReqs = append(g.detachReqs, req)
	kept := g.session.Participants[:0]
	for _, participant := range g.session.Participants {
		if participant.ID == req.ParticipantID {
			continue
		}
		kept = append(kept, participant)
	}
	g.session.Participants = kept
	return session.CloneSession(g.session), nil
}

type activeSubmitGatewayService struct {
	active        []gateway.ActiveTurnState
	activeSubmits []gateway.SubmitActiveTurnRequest
	activeErr     error
	beginReqs     []gateway.BeginTurnRequest
	beginCalls    int
	resume        session.Session
	resumeReq     gateway.ResumeSessionRequest
	bindReqs      []gateway.BindSessionRequest
	bindErr       error
}

func (g *activeSubmitGatewayService) Streams() stream.Service { return nil }

func (g *activeSubmitGatewayService) BeginTurn(_ context.Context, req gateway.BeginTurnRequest) (gateway.BeginTurnResult, error) {
	g.beginCalls++
	g.beginReqs = append(g.beginReqs, req)
	return gateway.BeginTurnResult{}, nil
}

func (g *activeSubmitGatewayService) SubmitActiveTurn(_ context.Context, req gateway.SubmitActiveTurnRequest) error {
	g.activeSubmits = append(g.activeSubmits, req)
	return g.activeErr
}

func (g *activeSubmitGatewayService) Interrupt(context.Context, gateway.InterruptRequest) error {
	return nil
}

func (g *activeSubmitGatewayService) ResumeSession(_ context.Context, req gateway.ResumeSessionRequest) (session.LoadedSession, error) {
	g.resumeReq = req
	return session.LoadedSession{Session: session.CloneSession(g.resume)}, nil
}

func (g *activeSubmitGatewayService) BindSession(_ context.Context, req gateway.BindSessionRequest) error {
	g.bindReqs = append(g.bindReqs, req)
	return g.bindErr
}

func (g *activeSubmitGatewayService) ListSessions(context.Context, gateway.ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}

func (g *activeSubmitGatewayService) ReplayEvents(context.Context, gateway.ReplayEventsRequest) (gateway.ReplayEventsResult, error) {
	return gateway.ReplayEventsResult{}, nil
}

func (g *activeSubmitGatewayService) ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error) {
	return gateway.ControlPlaneState{}, nil
}

func (g *activeSubmitGatewayService) HandoffController(context.Context, gateway.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *activeSubmitGatewayService) AttachParticipant(context.Context, gateway.AttachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *activeSubmitGatewayService) PromptParticipant(context.Context, gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{}, nil
}

func (g *activeSubmitGatewayService) StartParticipant(context.Context, gateway.StartParticipantRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{}, nil
}

func (g *activeSubmitGatewayService) DetachParticipant(context.Context, gateway.DetachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *activeSubmitGatewayService) ActiveTurns() []gateway.ActiveTurnState {
	return append([]gateway.ActiveTurnState(nil), g.active...)
}

type fixedReconnectReader struct {
	result controlclientport.ReconnectResult
	err    error
}

func (r *fixedReconnectReader) Reconnect(context.Context, controlclientport.ReconnectRequest) (controlclientport.ReconnectResult, error) {
	return r.result, r.err
}

func repoRootForAdapterTest(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func setHomeForAdapterTest(t *testing.T, home string) {
	t.Helper()
	testenv.SetHome(t, home)
}

func agentCandidatesHaveName(candidates []AgentCandidate, name string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func slashCandidatesHaveValue(candidates []SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Value), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func connectModelCompletionCommand(state connectwizard.ConnectWizardState) string {
	return "connect-model:" + state.EncodeCompletionState()
}

func candidateValues(candidates []SlashArgCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, strings.TrimSpace(candidate.Value))
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
