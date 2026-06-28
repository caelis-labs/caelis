package headless

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestRunOnceDrainsAssistantOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "e1",
			Kind:   eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "done"},
			},
		},
		{
			Cursor: "u1",
			Kind:   eventstream.KindSessionUpdate,
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}, nil),
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	result, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
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

func TestRunOnceIgnoresScopedTraceOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "main-1",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeMain,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "main answer"},
			},
		},
		{
			Cursor: "usage-main",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeMain,
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}, nil),
		},
		{
			Cursor:  "child-1",
			Kind:    eventstream.KindSessionUpdate,
			Scope:   eventstream.ScopeSubagent,
			ScopeID: "task-1",
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "child trace"},
			},
		},
		{
			Cursor: "usage-child",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeSubagent,
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{PromptTokens: 99}, nil),
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	result, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Input: "hello",
	}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "main answer" {
		t.Fatalf("RunOnce() output = %q, want main answer", result.Output)
	}
	if result.PromptTokens != 11 {
		t.Fatalf("RunOnce() prompt tokens = %d, want main-scope usage", result.PromptTokens)
	}
}

func TestRunOnceAutoDeniesApprovalByDefault(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "a1",
			Kind:   eventstream.KindRequestPermission,
			Permission: &schema.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
				},
			},
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	if _, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
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
	if got := handle.submissions[0]; got.Kind != gateway.SubmissionKindApproval || got.Approval == nil || got.Approval.Approved {
		t.Fatalf("approval submission = %+v, want auto-deny", got)
	}
}

func TestRunOnceApprovalCallbackReceivesPromptFields(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "a1",
			Kind:   eventstream.KindRequestPermission,
			Permission: &schema.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
					RawInput: map[string]any{
						"command":             "go test ./...",
						"approval_reason":     "needs execution",
						"justification":       "requested by user",
						"sandbox_permissions": "host",
					},
				},
				Options: []schema.PermissionOption{{
					OptionID: "allow_once",
					Name:     "Allow once",
					Kind:     "allow_once",
				}},
			},
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}
	called := false
	_, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Input: "hello",
	}, Options{
		ResolveApproval: func(_ context.Context, req *gateway.ApprovalPayload) (gateway.ApprovalDecision, error) {
			called = true
			if req == nil {
				t.Fatal("approval payload = nil")
			}
			if req.Reason != "needs execution" || req.Justification != "requested by user" || req.SandboxPermissions != "host" {
				t.Fatalf("approval fields = (%q, %q, %q), want restored prompt fields", req.Reason, req.Justification, req.SandboxPermissions)
			}
			return gateway.ApprovalDecision{Approved: true, Outcome: string(gateway.ApprovalStatusApproved)}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !called {
		t.Fatal("ResolveApproval was not called")
	}
	if len(handle.submissions) != 1 || handle.submissions[0].Approval == nil || !handle.submissions[0].Approval.Approved {
		t.Fatalf("submissions = %#v, want approved decision", handle.submissions)
	}
}

func TestRunOnceIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "r1",
			Kind:   eventstream.KindApprovalReview,
			ApprovalReview: &eventstream.ApprovalReview{
				ToolName: "RUN_COMMAND",
				Status:   string(gateway.ApprovalReviewStatusInProgress),
			},
		},
		{
			Cursor: "r2",
			Kind:   eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "done"},
			},
		},
	})
	gw := fakeStarter{
		result: gateway.BeginTurnResult{
			Session: session.Session{SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
			}},
			Handle: handle,
		},
	}

	result, err := RunOnce(context.Background(), gw, gateway.BeginTurnRequest{
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
	result gateway.BeginTurnResult
	err    error
}

func (f fakeStarter) BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error) {
	return f.result, f.err
}

type fakeTurnHandle struct {
	acpEvents   <-chan eventstream.Envelope
	submissions []gateway.SubmitRequest
}

func newFakeACPHandle(events []eventstream.Envelope) *fakeTurnHandle {
	ch := make(chan eventstream.Envelope, len(events))
	for _, env := range events {
		ch <- env
	}
	close(ch)
	return &fakeTurnHandle{acpEvents: ch}
}

func (h *fakeTurnHandle) HandleID() string                       { return "h1" }
func (h *fakeTurnHandle) RunID() string                          { return "run-1" }
func (h *fakeTurnHandle) TurnID() string                         { return "turn-1" }
func (h *fakeTurnHandle) SessionRef() session.SessionRef         { return session.SessionRef{} }
func (h *fakeTurnHandle) CreatedAt() time.Time                   { return time.Time{} }
func (h *fakeTurnHandle) ACPEvents() <-chan eventstream.Envelope { return h.acpEvents }
func (h *fakeTurnHandle) Submit(_ context.Context, req gateway.SubmitRequest) error {
	h.submissions = append(h.submissions, req)
	return nil
}
func (h *fakeTurnHandle) Cancel() gateway.CancelResult {
	return gateway.CancelResult{Status: gateway.CancelStatusCancelled}
}
func (h *fakeTurnHandle) Close() error { return nil }
