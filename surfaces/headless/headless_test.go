package headless

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
		turn: handle,
	}

	result, err := RunOnce(context.Background(), gw, control.Submission{Text: "hello"}, Options{})
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

func TestRunOnceAppendsPrefixGrowingACPMessageDeltasExactly(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "a"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "ab"},
			},
		},
	})
	result, err := RunOnce(context.Background(), fakeStarter{turn: handle}, control.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "aab" {
		t.Fatalf("RunOnce() output = %q, want exact ACP deltas aab", result.Output)
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
		turn: handle,
	}

	result, err := RunOnce(context.Background(), gw, control.Submission{Text: "hello"}, Options{})
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
			Cursor:            "a1",
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: eventstream.ApprovalRequestID("approval-1"),
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
		turn: handle,
	}

	if _, err := RunOnce(context.Background(), gw, control.Submission{Text: "hello"}, Options{}); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(handle.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(handle.submissions))
	}
	if got := handle.submissions[0]; got.Approved {
		t.Fatalf("approval submission = %+v, want auto-deny", got)
	}
}

func TestRunOnceApprovalCallbackReceivesPromptFields(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor:            "a1",
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: eventstream.ApprovalRequestID("approval-2"),
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
		turn: handle,
	}
	called := false
	_, err := RunOnce(context.Background(), gw, control.Submission{Text: "hello"}, Options{
		ResolveApproval: func(_ context.Context, req ApprovalRequest) (approval.Decision, error) {
			called = true
			if req.Payload == nil {
				t.Fatal("approval payload = nil")
			}
			if req.RequestID != "approval-2" {
				t.Fatalf("approval request id = %q, want approval-2", req.RequestID)
			}
			if req.Payload.Reason != "needs execution" || req.Payload.Justification != "requested by user" || req.Payload.SandboxPermissions != "host" {
				t.Fatalf("approval fields = (%q, %q, %q), want restored prompt fields", req.Payload.Reason, req.Payload.Justification, req.Payload.SandboxPermissions)
			}
			return approval.Decision{Approved: true, Outcome: string(approval.StatusApproved)}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !called {
		t.Fatal("ResolveApproval was not called")
	}
	if len(handle.submissions) != 1 || !handle.submissions[0].Approved {
		t.Fatalf("submissions = %#v, want approved decision", handle.submissions)
	}
	if handle.submissions[0].RequestID != "approval-2" {
		t.Fatalf("approval request id = %q, want approval-2", handle.submissions[0].RequestID)
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
				Status:   string(approval.ReviewStatusInProgress),
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
		turn: handle,
	}

	result, err := RunOnce(context.Background(), gw, control.Submission{Text: "hello"}, Options{})
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
	turn control.Turn
	err  error
}

func (f fakeStarter) Submit(context.Context, control.Submission) (control.Turn, error) {
	return f.turn, f.err
}

type fakeTurnHandle struct {
	acpEvents   <-chan eventstream.Envelope
	submissions []control.ApprovalDecision
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
func (h *fakeTurnHandle) ACPEvents() <-chan eventstream.Envelope { return h.acpEvents }
func (h *fakeTurnHandle) Events() <-chan eventstream.Envelope    { return h.acpEvents }
func (h *fakeTurnHandle) SubmitApproval(_ context.Context, decision control.ApprovalDecision) error {
	h.submissions = append(h.submissions, decision)
	return nil
}
func (h *fakeTurnHandle) Cancel()      {}
func (h *fakeTurnHandle) Close() error { return nil }
