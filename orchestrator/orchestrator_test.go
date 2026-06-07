package orchestrator

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
)

func TestOrchestratorSpawnInternalAgent(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()

	parentSess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "user", WorkspaceKey: "ws",
	})
	if err != nil {
		t.Fatalf("Create parent session: %v", err)
	}

	childLLM := &mockTextLLM{response: "child done"}
	childAgent := &mockAgent{name: "reviewer", llm: childLLM}
	parentAgent := &mockAgent{name: "parent", subAgents: []agent.Agent{childAgent}}

	orch, err := New(Config{
		Sessions: svc,
	})
	if err != nil {
		t.Fatalf("New orchestrator: %v", err)
	}

	delegator := orch.SpawnDelegator(parentAgent, parentSess, "main", "run-1")

	result, err := delegator.Spawn(fakeToolCtx{ctx}, agent.SpawnRequest{
		AgentName: "reviewer",
		Prompt:    "review this code",
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if result.HandleID == "" {
		t.Fatal("expected non-empty handle ID")
	}
	if result.FinalMessage == "" {
		t.Fatal("expected non-empty final message")
	}

	// Wait for the child to complete.
	time.Sleep(100 * time.Millisecond)
	handle, ok := orch.GetChild(result.HandleID)
	if !ok {
		t.Fatalf("child handle not found for %q", result.HandleID)
	}

	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child did not complete in time")
	}

	if handle.State() != DelegationCompleted {
		t.Fatalf("child state = %q, want completed", handle.State())
	}
	if handle.Output() != "child done" {
		t.Fatalf("child output = %q, want 'child done'", handle.Output())
	}

	// Verify child session was created.
	events, err := svc.Events(ctx, session.EventsRequest{
		SessionRef: handle.Anchor().ChildSessionRef,
	})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("child events = %d, want >= 2", len(events))
	}
}

func TestOrchestratorCancel(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()

	parentSess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "user", WorkspaceKey: "ws",
	})
	if err != nil {
		t.Fatalf("Create parent session: %v", err)
	}

	childLLM := &blockingLLM{}
	childAgent := &mockAgent{name: "blocker", llm: childLLM}
	parentAgent := &mockAgent{name: "parent", subAgents: []agent.Agent{childAgent}}

	orch, err := New(Config{
		Sessions: svc,
	})
	if err != nil {
		t.Fatalf("New orchestrator: %v", err)
	}

	delegator := orch.SpawnDelegator(parentAgent, parentSess, "main", "run-1")
	result, err := delegator.Spawn(fakeToolCtx{ctx}, agent.SpawnRequest{
		AgentName: "blocker",
		Prompt:    "block forever",
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Give the child time to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel the child.
	if err := orch.Cancel(ctx, result.HandleID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	handle, _ := orch.GetChild(result.HandleID)
	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child did not cancel in time")
	}

	if handle.State() != DelegationCancelled {
		t.Fatalf("child state = %q, want cancelled", handle.State())
	}
}

func TestContextViewMainExcludesDelegatedTranscript(t *testing.T) {
	events := []session.Event{
		// Main user event.
		{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "hello"}},
			},
		},
		// Main assistant event.
		{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "hi"}},
			},
		},
		// SPAWN tool call (anchor — should be visible).
		{
			Kind:       session.EventKindToolCall,
			Visibility: session.VisibilityCanonical,
			ToolCallPayload: &session.ToolCallPayload{
				CallID: "spawn-1", Name: "SPAWN", Status: "pending",
			},
		},
		// Delegated child transcript (should be excluded).
		{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			Actor:      session.ActorRef{Source: "acp_subagent", ParticipantID: "child-1"},
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "child internal work"}},
			},
		},
		// SPAWN tool result (summary — should be visible).
		{
			Kind:       session.EventKindToolResult,
			Visibility: session.VisibilityCanonical,
			ToolResultPayload: &session.ToolResultPayload{
				CallID: "spawn-1", Name: "SPAWN", Status: "completed",
				Content: []session.EventPart{{Kind: session.PartKindText, Text: "child done"}},
			},
		},
		// Final assistant response.
		{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "result: child done"}},
			},
		},
	}

	mainCtx := MainContext(events)

	// Should include: user, assistant, SPAWN call, SPAWN result, final assistant = 5
	// Should exclude: delegated child transcript
	if len(mainCtx) != 5 {
		t.Fatalf("main context events = %d, want 5", len(mainCtx))
		for i, e := range mainCtx {
			t.Logf("  [%d] kind=%s source=%s text=%q", i, e.Kind, e.Actor.Source, e.TextContent())
		}
	}

	// Verify the delegated child transcript is excluded.
	for _, e := range mainCtx {
		if e.Actor.Source == "acp_subagent" && e.Kind == session.EventKindAssistant {
			t.Error("delegated child assistant event should not appear in main context")
		}
	}
}

func TestContextViewMainIncludesShareableParticipant(t *testing.T) {
	events := []session.Event{
		{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "hello"}},
			},
		},
		// Sidecar participant final assistant (shareable).
		{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			Actor:      session.ActorRef{Source: "acp_participant", ParticipantID: "sidecar-1"},
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "sidecar analysis"}},
			},
		},
	}

	mainCtx := MainContext(events)
	if len(mainCtx) != 2 {
		t.Fatalf("main context = %d, want 2 (user + shareable sidecar)", len(mainCtx))
	}
}

// ─── Test helpers ──────────────────────────────────────────────────────

type mockTextLLM struct {
	response string
}

func (m *mockTextLLM) Name() string { return "mock-text" }

func (m *mockTextLLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		yield(model.ResponseEvent{TextDelta: m.response}, nil)
	}
}

type mockAgent struct {
	name      string
	llm       model.LLM
	subAgents []agent.Agent
}

func (a *mockAgent) Name() string        { return a.name }
func (a *mockAgent) Description() string  { return "mock agent" }
func (a *mockAgent) SubAgents() []agent.Agent { return a.subAgents }
func (a *mockAgent) FindAgent(name string) agent.Agent {
	for _, s := range a.subAgents {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func (a *mockAgent) Run(inv agent.InvocationContext) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		if a.llm == nil {
			yield(session.Event{
				Kind:       session.EventKindAssistant,
				Visibility: session.VisibilityCanonical,
				AssistantPayload: &session.AssistantPayload{
					Parts: []session.EventPart{{Kind: session.PartKindText, Text: "no llm"}},
				},
			}, nil)
			return
		}
		var text string
		for evt, err := range a.llm.Generate(inv, model.Request{
			Messages: inv.PriorMessages(),
		}) {
			if err != nil {
				yield(session.Event{}, err)
				return
			}
			text += evt.TextDelta
		}
		yield(session.Event{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: text}},
			},
		}, nil)
	}
}

type blockingLLM struct{}

func (m *blockingLLM) Name() string { return "blocking" }

func (m *blockingLLM) Generate(ctx context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		<-ctx.Done()
		yield(model.ResponseEvent{}, ctx.Err())
	}
}

type fakeToolCtx struct {
	context.Context
}

func (c fakeToolCtx) SessionRef() string             { return "test-session" }
func (c fakeToolCtx) InvocationID() string           { return "test-inv" }
func (c fakeToolCtx) AgentName() string              { return "test-agent" }
func (c fakeToolCtx) FileSystem() sandbox.FileSystem { return nil }
