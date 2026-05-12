package headless

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestRunOnceDrainsAssistantOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Cursor: "e1",
			Event: kernel.Event{
				Kind: kernel.EventKindAssistantMessage,
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Text:  "done",
					Final: true,
				},
				Usage: &kernel.UsageSnapshot{PromptTokens: 11},
			},
		},
	})
	gw := fakeStarter{
		result: kernel.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	result, err := RunOnce(context.Background(), gw, kernel.BeginTurnRequest{
		SessionRef: session.SessionRef{
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

	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Cursor: "a1",
			Event: kernel.Event{
				Kind: kernel.EventKindApprovalRequested,
				ApprovalPayload: &kernel.ApprovalPayload{
					ToolName: "bash",
				},
			},
		},
	})
	gw := fakeStarter{
		result: kernel.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	if _, err := RunOnce(context.Background(), gw, kernel.BeginTurnRequest{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Input: "hello",
	}, Options{}); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(handle.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(handle.submissions))
	}
	if got := handle.submissions[0]; got.Kind != kernel.SubmissionKindApproval || got.Approval == nil || got.Approval.Approved {
		t.Fatalf("approval submission = %+v, want auto-deny", got)
	}
}

func TestRunOnceIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	t.Parallel()

	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Cursor: "r1",
			Event: kernel.Event{
				Kind: kernel.EventKindApprovalReview,
				ApprovalPayload: &kernel.ApprovalPayload{
					ToolName:       "bash",
					ReviewStatus:   kernel.ApprovalReviewStatusInProgress,
					DecisionSource: "auto-review",
				},
			},
		},
		{
			Cursor: "r2",
			Event: kernel.Event{
				Kind: kernel.EventKindAssistantMessage,
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Text:  "done",
					Final: true,
				},
			},
		},
	})
	gw := fakeStarter{
		result: kernel.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	result, err := RunOnce(context.Background(), gw, kernel.BeginTurnRequest{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Input: "hello",
	}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunOnce() output = %q, want done", result.Output)
	}
	if len(handle.submissions) != 0 {
		t.Fatalf("submissions = %d, want no manual decision for auto-review event", len(handle.submissions))
	}
}

type fakeStarter struct {
	result kernel.BeginTurnResult
	err    error
}

func (f fakeStarter) BeginTurn(context.Context, kernel.BeginTurnRequest) (kernel.BeginTurnResult, error) {
	return f.result, f.err
}

type fakeHandle struct {
	events      chan kernel.EventEnvelope
	submissions []kernel.SubmitRequest
}

func newFakeHandle(events []kernel.EventEnvelope) *fakeHandle {
	ch := make(chan kernel.EventEnvelope, len(events))
	for _, env := range events {
		ch <- env
	}
	close(ch)
	return &fakeHandle{events: ch}
}

func (h *fakeHandle) HandleID() string                    { return "h1" }
func (h *fakeHandle) RunID() string                       { return "run-1" }
func (h *fakeHandle) TurnID() string                      { return "turn-1" }
func (h *fakeHandle) SessionRef() session.SessionRef      { return session.SessionRef{} }
func (h *fakeHandle) CreatedAt() time.Time                { return time.Time{} }
func (h *fakeHandle) Events() <-chan kernel.EventEnvelope { return h.events }
func (h *fakeHandle) EventsAfter(string) ([]kernel.EventEnvelope, string, error) {
	return nil, "", nil
}
func (h *fakeHandle) Submit(_ context.Context, req kernel.SubmitRequest) error {
	h.submissions = append(h.submissions, req)
	return nil
}
func (h *fakeHandle) Cancel() kernel.CancelResult {
	return kernel.CancelResult{Status: kernel.CancelStatusCancelled}
}
func (h *fakeHandle) Close() error { return nil }
