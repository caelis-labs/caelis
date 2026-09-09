package subagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestBoundChildPermissionHandlerFailClosedBeforeBridge(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		boundID   string
		requestID string
		wantErr   bool
	}{
		{name: "unknown before session bind", requestID: "child-1", wantErr: true},
		{name: "mismatch", boundID: "child-1", requestID: "other", wantErr: true},
		{name: "empty request", boundID: "child-1", wantErr: true},
		{name: "whitespace mismatch", boundID: "child-1", requestID: " child-1 ", wantErr: true},
		{name: "valid", boundID: "child-1", requestID: "child-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			bridge := &recordingPermissionBridge{}
			runner := &Runner{permissionBridge: bridge}
			run := &childRun{anchor: delegation.Anchor{SessionID: test.boundID}}
			handler := boundChildPermissionHandler(run, runner.permissionCallback(tasksubagent.SpawnContext{
				SessionRef: session.SessionRef{SessionID: "root-session"},
				TaskID:     "task-1",
			}, AgentConfig{Name: "helper"}, "helper-1"))

			response, err := handler(context.Background(), testPermissionRequest(test.requestID))
			if test.wantErr {
				if !errors.Is(err, errACPPermissionSessionMismatchChild) {
					t.Fatalf("permission error = %v, want bound-child mismatch", err)
				}
				if bridge.calls.Load() != 0 {
					t.Fatalf("permission bridge calls = %d, want zero before bind", bridge.calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatalf("permission error = %v", err)
			}
			if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow_once" {
				t.Fatalf("permission response = %#v, want selected allow_once", response)
			}
			if bridge.calls.Load() != 1 {
				t.Fatalf("permission bridge calls = %d, want 1", bridge.calls.Load())
			}
		})
	}
}

func TestReconnectPermissionEntryUsesBoundChildSession(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bridge := &recordingPermissionBridge{}
	runner := childInputTestRunner(t, "permission-resume")
	runner.permissionBridge = bridge
	events := make(chan childInputTestEvent, 12)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-permission-resume", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	if bridge.calls.Load() != 0 {
		t.Fatalf("spawn permission bridge calls = %d, want zero", bridge.calls.Load())
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.client.Close(ctx)
	run.mu.Lock()
	run.state = delegation.StateFailed
	run.running = false
	run.mu.Unlock()
	result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "resume prompt",
	})
	if err != nil || !result.StartedActivity {
		t.Fatalf("SubmitChildInput() after reconnect = (%#v, %v)", result, err)
	}
	terminal := waitChildActivityTerminalFor(t, ctx, events, result.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("reconnected terminal = %#v", terminal)
	}
	if bridge.calls.Load() != 1 {
		t.Fatalf("reconnect permission bridge calls = %d, want only the exact bound Session", bridge.calls.Load())
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

type recordingPermissionBridge struct {
	calls atomic.Int64
}

func (b *recordingPermissionBridge) RequestPermission(context.Context, PermissionRequest) (client.RequestPermissionResponse, error) {
	b.calls.Add(1)
	return client.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow_once"),
	}, nil
}

func testPermissionRequest(sessionID string) client.RequestPermissionRequest {
	kind := acpsdk.ToolKindExecute
	status := acpsdk.ToolCallStatusPending
	title := "Run command"
	return client.RequestPermissionRequest{
		SessionId: acpsdk.SessionId(sessionID),
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Kind:       &kind,
			Title:      &title,
			Status:     &status,
			RawInput:   map[string]any{"command": "pwd"},
		},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow_once",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
		}},
	}
}
