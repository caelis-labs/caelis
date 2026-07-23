package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestRuntimeAgentACPSpawnLifecycleGolden(t *testing.T) {
	spawnStatus := acp.ToolStatusInProgress
	completed := acp.ToolStatusCompleted
	spawnTitle := "Spawn breeze: explain your capability"
	spawnKind := acp.ToolKindExecute
	waitTitle := "Task wait"
	waitKind := acp.ToolKindExecute
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"}
	turn := &testControlTurn{events: make(chan eventstream.Envelope)}
	subscription := &spawnGoldenSubscription{events: make(chan eventstream.Envelope), closed: make(chan struct{})}
	streams := &spawnGoldenTaskStreams{
		subscribed:   make(chan taskstream.SubscribeRequest, 1),
		subscription: subscription,
		descriptor: taskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-yara", Handle: "yara", AgentHandle: "breeze",
			Kind: task.KindSubagent, State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "spawn-call-1", ToolName: "Spawn"},
		},
	}
	runtimeAgent, sessionID := newSpawnGoldenAgent(t, turn, streams)
	callbacks := &spawnGoldenCallbacks{updates: make(chan acp.SessionNotification, 32)}
	promptErr := make(chan error, 1)
	go func() {
		_, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
			SessionID: sessionID,
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run"}`)},
		}, callbacks)
		promptErr <- err
	}()

	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: acp.ToolCall{
			SessionUpdate: acp.UpdateToolCall, ToolCallID: "spawn-call-1", Title: spawnTitle,
			Kind: spawnKind, Status: acp.ToolStatusPending,
			RawInput: map[string]any{"agent": "breeze", "prompt": "explain your capability"},
			Content:  []acp.ToolCallContent{{Type: "terminal", TerminalID: "spawn-call-1"}},
		},
	})
	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "spawn-call-1", Title: &spawnTitle,
			Kind: &spawnKind, Status: &spawnStatus,
			RawInput: map[string]any{"agent": "breeze", "prompt": "explain your capability"},
			RawOutput: map[string]any{
				"handle": "yara", "parent_call": "spawn-call-1", "parent_tool": "Spawn",
				"state": "running", "target_kind": "subagent",
			},
			Content: []acp.ToolCallContent{{Type: "terminal", TerminalID: "spawn-call-1"}},
		},
	})
	select {
	case request := <-streams.subscribed:
		if request.SessionID != "session-1" || request.TaskID != "task-yara" {
			t.Fatalf("Spawn Task Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("ACP bridge did not subscribe the Spawn Task stream")
	}

	child := func(update acp.Update, final bool) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
			Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent,
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Final: final, Update: update,
		}
	}
	sendGoldenTaskEnvelope(t, subscription, callbacks, child(acp.ContentChunk{
		SessionUpdate: acp.UpdateAgentMessage, MessageID: "child-message-opening",
		Content: acp.TextContent{Type: "text", Text: "child opening"},
	}, false))
	sendGoldenTaskEnvelope(t, subscription, callbacks, child(acp.ContentChunk{
		SessionUpdate: acp.UpdateAgentThought,
		Content:       acp.TextContent{Type: "text", Text: "child thought"},
	}, false))
	sendGoldenTaskEnvelope(t, subscription, callbacks, eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent,
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Notice:   "child live output resumed after a transient gap",
	})
	sendGoldenTaskEnvelope(t, subscription, callbacks, child(acp.ToolCall{
		SessionUpdate: acp.UpdateToolCall, ToolCallID: "child-patch-1",
		Title: "Apply child patch", Kind: acp.ToolKindEdit, Status: acp.ToolStatusInProgress,
	}, false))
	childCommandTitle := "Run child command"
	sendGoldenTaskEnvelope(t, subscription, callbacks, child(acp.ToolCallUpdate{
		SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "child-command-1",
		Title: &childCommandTitle, Status: &spawnStatus,
		Content: []acp.ToolCallContent{{
			Type: "terminal", TerminalID: "child-terminal-1",
			Content: acp.TextContent{Type: "text", Text: "nested output\n"},
		}},
	}, false))
	sendGoldenTaskEnvelope(t, subscription, callbacks, child(schema.PlanUpdate{
		SessionUpdate: schema.UpdatePlan,
		Entries:       []schema.PlanEntry{{Content: "inspect child output", Status: "in_progress"}},
	}, false))

	sendGoldenTaskEnvelope(t, subscription, callbacks, child(acp.ContentChunk{
		SessionUpdate: acp.UpdateAgentMessage, MessageID: "child-message-final",
		Content: acp.TextContent{Type: "text", Text: "I can inspect, edit, test, and review code."},
	}, true))
	subscription.events <- eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent, Final: true,
		Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
	}
	// The typed child lifecycle must close the Spawn terminal without requiring
	// any Task wait tool call.
	waitGoldenNotification(t, callbacks)
	select {
	case <-subscription.closed:
	case <-time.After(time.Second):
		t.Fatal("Spawn Task subscription remained open after its lifecycle boundary")
	}

	// A later canonical batch wait remains visible as its own standard ACP tool
	// result, but must not emit a second parent terminal_exit.
	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: acp.ToolCall{
			SessionUpdate: acp.UpdateToolCall, ToolCallID: "wait-call-1", Title: waitTitle,
			Kind: waitKind, Status: acp.ToolStatusPending,
			RawInput: map[string]any{"action": "wait", "handle": "yara"},
		},
	})
	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "wait-call-1", Title: &waitTitle,
			Kind: &waitKind, Status: &completed,
			RawInput: map[string]any{"action": "wait", "handle": "yara"},
			RawOutput: map[string]any{
				"action":              "wait",
				"actual_wait_time_ms": 702,
				"count":               1,
				"failed":              0,
				"tasks": []any{map[string]any{
					"actual_wait_time_ms": 702,
					"final_message":       "I can inspect, edit, test, and review code.",
					"handle":              "yara",
					"parent_call":         "spawn-call-1",
					"parent_tool":         "Spawn",
					"state":               "completed",
					"target_kind":         "subagent",
				}},
			},
		},
	})
	close(turn.events)
	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Prompt did not finish after the Spawn lifecycle closed")
	}

	got, err := json.MarshalIndent(callbacks.snapshot(), "", "  ")
	if err != nil {
		t.Fatalf("marshal ACP notifications: %v", err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/golden/acp_stdio_spawn_lifecycle.golden.json")
	if err != nil {
		t.Fatalf("read ACP Spawn golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("ACP Spawn lifecycle changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func newSpawnGoldenAgent(t *testing.T, turn *testControlTurn, streams taskstream.Service) (*runtimeacp.RuntimeAgent, string) {
	t.Helper()
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	router := &testPromptRouter{result: controlprompt.Result{Handled: true, Turn: turn}}
	runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
		Runtime: runtime, Sessions: sessions, TaskStreams: streams,
		TaskStreamPrincipal: taskstream.Principal{ID: "user-1"},
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled prompt")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		AppName: "caelis", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := runtimeAgent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	router.result.ActiveSessionID = "session-1"
	return runtimeAgent, activeSession.SessionID
}

func sendGoldenTurnEnvelope(t *testing.T, turn *testControlTurn, callbacks *spawnGoldenCallbacks, envelope eventstream.Envelope) {
	t.Helper()
	turn.events <- envelope
	waitGoldenNotification(t, callbacks)
}

func sendGoldenTaskEnvelope(t *testing.T, subscription *spawnGoldenSubscription, callbacks *spawnGoldenCallbacks, envelope eventstream.Envelope) {
	t.Helper()
	subscription.events <- envelope
	waitGoldenNotification(t, callbacks)
}

func waitGoldenNotification(t *testing.T, callbacks *spawnGoldenCallbacks) acp.SessionNotification {
	t.Helper()
	select {
	case notification := <-callbacks.updates:
		return notification
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ACP session/update")
		return acp.SessionNotification{}
	}
}

type spawnGoldenCallbacks struct {
	mu            sync.Mutex
	notifications []acp.SessionNotification
	updates       chan acp.SessionNotification
}

func (c *spawnGoldenCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.mu.Lock()
	c.notifications = append(c.notifications, notification)
	c.mu.Unlock()
	c.updates <- notification
	return nil
}

func (*spawnGoldenCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (c *spawnGoldenCallbacks) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.notifications...)
}

type spawnGoldenTaskStreams struct {
	subscribed   chan taskstream.SubscribeRequest
	subscription *spawnGoldenSubscription
	descriptor   taskstream.TaskDescriptor
}

func (s *spawnGoldenTaskStreams) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{s.descriptor}}, nil
}

func (*spawnGoldenTaskStreams) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.Batch, error) {
	return taskstream.Batch{}, nil
}

func (s *spawnGoldenTaskStreams) Subscribe(_ context.Context, _ taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.subscribed <- request
	return taskstream.SubscribeResult{Subscription: s.subscription, ResumeMode: taskstream.ResumeModeExact}, nil
}

type spawnGoldenSubscription struct {
	events chan eventstream.Envelope
	closed chan struct{}
	once   sync.Once
}

func (s *spawnGoldenSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (*spawnGoldenSubscription) Err() error                            { return nil }
func (*spawnGoldenSubscription) LastCursor() string                    { return "" }
func (s *spawnGoldenSubscription) Close() error {
	s.once.Do(func() {
		close(s.events)
		close(s.closed)
	})
	return nil
}
