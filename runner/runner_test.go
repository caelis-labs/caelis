package runner

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/agent/llmagent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

// mockLLM returns a fixed text response and records model requests.
type mockLLM struct {
	responses []string
	callCount int
	requests  []model.Request
}

func (m *mockLLM) Name() string { return "mock" }

func (m *mockLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		if m.callCount < len(m.responses) {
			text := m.responses[m.callCount]
			m.callCount++
			yield(model.ResponseEvent{TextDelta: text}, nil)
		}
	}
}

func prepareAgent(a *llmagent.Agent, llm *mockLLM) *llmagent.Agent {
	prepared := a.Prepare(agent.PrepareRequest{LLM: llm})
	return prepared.(*llmagent.Agent)
}

func TestRunnerBasicFlow(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "user", WorkspaceKey: "ws",
	})

	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	ml := &mockLLM{responses: []string{"Hello, world!"}}
	a = prepareAgent(a, ml)

	r, _ := New(Config{Agent: a, Sessions: svc})

	var events []session.Event
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		events = append(events, evt)
	}

	if len(events) < 2 {
		t.Fatalf("got %d events, want >= 2", len(events))
	}
	if events[0].Kind != session.EventKindUser {
		t.Errorf("event 0 kind: got %q, want %q", events[0].Kind, session.EventKindUser)
	}
	if events[1].Kind != session.EventKindAssistant {
		t.Errorf("event 1 kind: got %q, want %q", events[1].Kind, session.EventKindAssistant)
	}
	if events[1].TextContent() != "Hello, world!" {
		t.Errorf("assistant text: got %q, want %q", events[1].TextContent(), "Hello, world!")
	}
}

func TestRunnerNewSession(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()

	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	a = prepareAgent(a, &mockLLM{responses: []string{"ok"}})

	r, _ := New(Config{Agent: a, Sessions: svc})

	var count int
	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  session.Ref{AppName: "test", UserID: "u", WorkspaceKey: "ws", SessionID: "new"},
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "create me"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		count++
	}
	if count < 2 {
		t.Errorf("got %d events, want >= 2", count)
	}
}

func TestRunnerRequiresAgent(t *testing.T) {
	_, err := New(Config{Sessions: session.InMemoryService()})
	if err == nil {
		t.Error("expected error for nil agent")
	}
}

func TestRunnerRequiresSessions(t *testing.T) {
	_, err := New(Config{Agent: llmagent.New(llmagent.Config{Name: "test"})})
	if err == nil {
		t.Error("expected error for nil sessions")
	}
}

// ─── Golden test: runtime model request == replay model request ──────

func TestReplayContextIsWired(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	// Pre-populate session with prior conversation.
	svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "previous question"}},
		},
	})
	svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind: session.EventKindAssistant, Visibility: session.VisibilityCanonical,
		AssistantPayload: &session.AssistantPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "previous answer"}},
		},
	})

	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	ml := &mockLLM{responses: []string{"new answer"}}
	a = prepareAgent(a, ml)

	r, _ := New(Config{Agent: a, Sessions: svc})

	var count int
	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "new question"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		count++
	}

	// Verify the model request includes prior messages.
	if len(ml.requests) != 1 {
		t.Fatalf("got %d model requests, want 1", len(ml.requests))
	}
	req := ml.requests[0]
	// Expected: prior user, prior assistant, current user
	if len(req.Messages) < 3 {
		t.Fatalf("got %d messages in model request, want >= 3", len(req.Messages))
	}
	if req.Messages[0].Content[0].Text != "previous question" {
		t.Errorf("msg 0: got %q, want %q", req.Messages[0].Content[0].Text, "previous question")
	}
	if req.Messages[1].Content[0].Text != "previous answer" {
		t.Errorf("msg 1: got %q, want %q", req.Messages[1].Content[0].Text, "previous answer")
	}
	if req.Messages[len(req.Messages)-1].Content[0].Text != "new question" {
		t.Errorf("last msg: got %q, want %q", req.Messages[len(req.Messages)-1].Content[0].Text, "new question")
	}
}

// ─── Transient event filtering ───────────────────────────────────────

// transientAgent yields predetermined events without needing LLM.
type transientAgent struct {
	events []session.Event
}

func (a *transientAgent) Name() string                   { return "transient-mock" }
func (a *transientAgent) Description() string            { return "mock" }
func (a *transientAgent) SubAgents() []agent.Agent       { return nil }
func (a *transientAgent) FindAgent(_ string) agent.Agent { return nil }
func (a *transientAgent) Run(_ agent.InvocationContext) iter.Seq2[session.Event, error] {
	events := a.events
	return func(yield func(session.Event, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}

func TestTransientEventsNotPersisted(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	mockAgent := &transientAgent{
		events: []session.Event{
			{
				Kind: session.EventKindNotice, Visibility: session.VisibilityUIOnly,
				NoticePayload: &session.NoticePayload{Text: "thinking..."},
			},
			{
				Kind: session.EventKindAssistant, Visibility: session.VisibilityCanonical,
				AssistantPayload: &session.AssistantPayload{
					Parts: []session.EventPart{{Kind: session.PartKindText, Text: "done"}},
				},
			},
		},
	}

	r, _ := New(Config{Agent: mockAgent, Sessions: svc})

	var yielded []session.Event
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "go"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		yielded = append(yielded, evt)
	}

	// Should yield user + notice + assistant.
	if len(yielded) < 3 {
		t.Fatalf("got %d yielded events, want >= 3", len(yielded))
	}

	// But only canonical events should be persisted.
	persisted, _ := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	for _, e := range persisted {
		if e.Visibility == session.VisibilityUIOnly {
			t.Error("ui_only event should not be persisted")
		}
		if e.Visibility == session.VisibilityOverlay {
			t.Error("overlay event should not be persisted")
		}
	}

	// Verify transient events have session/run identity.
	for _, e := range yielded {
		if e.Visibility.IsTransient() {
			if e.SessionRef != sess.Ref {
				t.Errorf("transient event missing SessionRef")
			}
			if e.RunID == "" {
				t.Errorf("transient event missing RunID")
			}
		}
	}
}

func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Error("nil should not be not-found")
	}
	if !isNotFound(fmt.Errorf("session not found: x")) {
		t.Error("expected not-found")
	}
	if isNotFound(fmt.Errorf("permission denied")) {
		t.Error("should not be not-found")
	}
}
