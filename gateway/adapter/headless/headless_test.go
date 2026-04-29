package headless

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestRunOnceDrainsAssistantOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeHandle([]gateway.EventEnvelope{
		{
			Cursor: "e1",
			Event: gateway.Event{
				Kind: gateway.EventKindAssistantMessage,
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleAssistant,
					Text:  "done",
					Final: true,
				},
				Usage: &gateway.UsageSnapshot{PromptTokens: 11},
			},
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: sdksession.Session{SessionRef: sdksession.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	result, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Input: "hello",
	}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunOnce() output = %q, want %q", result.Output, "done")
	}
	if result.PromptTokens != 11 {
		t.Fatalf("RunOnce() prompt tokens = %d, want %d", result.PromptTokens, 11)
	}
}

func TestRunOnceAutoDeniesApprovalByDefault(t *testing.T) {
	t.Parallel()

	handle := newFakeHandle([]gateway.EventEnvelope{
		{
			Cursor: "a1",
			Event: gateway.Event{
				Kind: gateway.EventKindApprovalRequested,
				ApprovalPayload: &gateway.ApprovalPayload{
					ToolName: "bash",
				},
			},
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: sdksession.Session{SessionRef: sdksession.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	if _, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Input: "hello",
	}, Options{}); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(handle.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(handle.submissions))
	}
	if got := handle.submissions[0]; got.Kind != gateway.SubmissionKindApproval || got.Approval == nil || got.Approval.Approved {
		t.Fatalf("approval submission = %+v, want auto-deny", got)
	}
}

type fakeStarter struct {
	result gateway.BeginTurnResult
	err    error
}

func (f fakeStarter) BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error) {
	return f.result, f.err
}

type fakeHandle struct {
	events      chan gateway.EventEnvelope
	submissions []gateway.SubmitRequest
}

func newFakeHandle(events []gateway.EventEnvelope) *fakeHandle {
	ch := make(chan gateway.EventEnvelope, len(events))
	for _, env := range events {
		ch <- env
	}
	close(ch)
	return &fakeHandle{events: ch}
}

func (h *fakeHandle) HandleID() string                     { return "h1" }
func (h *fakeHandle) RunID() string                        { return "run-1" }
func (h *fakeHandle) TurnID() string                       { return "turn-1" }
func (h *fakeHandle) SessionRef() sdksession.SessionRef    { return sdksession.SessionRef{} }
func (h *fakeHandle) CreatedAt() time.Time                 { return time.Time{} }
func (h *fakeHandle) Events() <-chan gateway.EventEnvelope { return h.events }
func (h *fakeHandle) EventsAfter(string) ([]gateway.EventEnvelope, string, error) {
	return nil, "", nil
}
func (h *fakeHandle) Submit(_ context.Context, req gateway.SubmitRequest) error {
	h.submissions = append(h.submissions, req)
	return nil
}
func (h *fakeHandle) Cancel() bool { return true }
func (h *fakeHandle) Close() error { return nil }
