package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestRuntimeAgentDirectRunnerSpawnFallbackGolden(t *testing.T) {
	runnerEvents := make(chan *session.Event)
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
	runtimeAgent, sessionID := newSpawnGoldenDirectRunnerAgent(t, runnerEvents, streams)
	callbacks := &spawnGoldenCallbacks{updates: make(chan eventstream.SessionNotification, 16)}
	promptErr := make(chan error, 1)
	go func() {
		_, err := runtimeAgent.Prompt(context.Background(), runtimeacp.PromptInput{
			SessionID: sessionID,
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run"}`)},
		}, callbacks)
		promptErr <- err
	}()

	sendGoldenRunnerEvent(t, runnerEvents, callbacks,
		loadGoldenToolEvent(
			session.EventTypeToolCall,
			"spawn-call-1",
			"Spawn",
			"pending",
			map[string]any{"agent": "breeze", "prompt": "explain your capability"},
			nil,
		),
	)
	sendGoldenRunnerEvent(t, runnerEvents, callbacks,
		loadGoldenToolEvent(
			session.EventTypeToolResult,
			"spawn-call-1",
			"Spawn",
			"running",
			nil,
			map[string]any{
				"handle": "yara", "parent_call": "spawn-call-1", "parent_tool": "Spawn",
				"state": "running", "target_kind": "subagent",
			},
		),
	)
	select {
	case request := <-streams.subscribed:
		if request.SessionID != "session-1" || request.TaskID != "task-yara" {
			t.Fatalf("Spawn Task Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("direct Runtime runner did not subscribe the Spawn Task stream")
	}

	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"}
	subscription.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent,
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			MessageID:     "child-message-1",
			Content:       eventstream.TextContent{Type: "text", Text: "child output before wait"},
		},
	}
	// Direct Runtime delivery still consumes Task output while the parent Runner
	// is waiting, but the nested child stream produces no ACP notification.
	if err := subscription.Close(); err != nil {
		t.Fatalf("close Task subscription: %v", err)
	}

	sendGoldenRunnerEvent(t, runnerEvents, callbacks,
		loadGoldenToolEvent(
			session.EventTypeToolCall,
			"wait-call-1",
			"Task",
			"pending",
			map[string]any{"action": "wait", "handle": "yara"},
			nil,
		),
	)
	runnerEvents <- loadGoldenToolEvent(
		session.EventTypeToolResult,
		"wait-call-1",
		"Task",
		"completed",
		map[string]any{"action": "wait", "handle": "yara"},
		map[string]any{
			"action": "wait",
			"tasks": []any{map[string]any{
				"final_message": "exact fallback final",
				"handle":        "yara",
				"parent_call":   "spawn-call-1",
				"parent_tool":   "Spawn",
				"state":         "completed",
				"target_kind":   "subagent",
			}},
		},
	)
	waitGoldenNotification(t, callbacks) // canonical Task wait result
	waitGoldenNotification(t, callbacks) // observed parent Spawn fallback close
	close(runnerEvents)

	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("direct Runtime Prompt did not finish")
	}

	notifications := callbacks.snapshot()
	spawnCompleted := 0
	for _, notification := range notifications {
		update, ok := notification.Update.(eventstream.ToolCallUpdate)
		if !ok || update.ToolCallID != "spawn-call-1" || update.Status == nil || *update.Status != eventstream.ToolStatusCompleted {
			continue
		}
		spawnCompleted++
		if update.Meta != nil || len(update.Content) != 1 || update.Content[0].Type != "content" {
			t.Fatalf("Spawn fallback update = %#v, want one standard result without terminal metadata", update)
		}
		text, ok := update.Content[0].Content.(eventstream.TextContent)
		if !ok || text.Text != "exact fallback final" {
			t.Fatalf("Spawn fallback content = %#v, want exact final message", update.Content)
		}
	}
	if spawnCompleted != 1 {
		t.Fatalf("Spawn completed updates = %d, want exactly one", spawnCompleted)
	}

	got, err := json.MarshalIndent(notifications, "", "  ")
	if err != nil {
		t.Fatalf("marshal direct Runtime ACP notifications: %v", err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/golden/acp_stdio_direct_runner_spawn_fallback.golden.json")
	if err != nil {
		t.Fatalf("read direct Runtime Spawn golden: %v\n--- got ---\n%s", err, got)
	}
	if string(got) != string(want) {
		t.Fatalf("direct Runtime Spawn fallback changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRuntimeAgentACPSpawnLifecycleGolden(t *testing.T) {
	spawnStatus := eventstream.ToolStatusInProgress
	completed := eventstream.ToolStatusCompleted
	spawnTitle := "Spawn breeze: explain your capability"
	spawnKind := eventstream.ToolKindExecute
	waitTitle := "Task wait"
	waitKind := eventstream.ToolKindExecute
	spawnMeta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Spawn",
	})
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
	callbacks := &spawnGoldenCallbacks{updates: make(chan eventstream.SessionNotification, 32)}
	promptErr := make(chan error, 1)
	go func() {
		_, err := runtimeAgent.Prompt(context.Background(), runtimeacp.PromptInput{
			SessionID: sessionID,
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run"}`)},
		}, callbacks)
		promptErr <- err
	}()

	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-call-1", Title: spawnTitle,
			Kind: spawnKind, Status: eventstream.ToolStatusPending,
			RawInput: map[string]any{"agent": "breeze", "prompt": "explain your capability"},
			Content:  []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "spawn-call-1"}},
			Meta:     spawnMeta,
		},
	})
	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-call-1", Title: &spawnTitle,
			Kind: &spawnKind, Status: &spawnStatus,
			RawInput: map[string]any{"agent": "breeze", "prompt": "explain your capability"},
			RawOutput: map[string]any{
				"handle": "yara", "parent_call": "spawn-call-1", "parent_tool": "Spawn",
				"state": "running", "target_kind": "subagent",
			},
			Content: []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "spawn-call-1"}},
			Meta:    spawnMeta,
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

	child := func(update eventstream.Update, final bool) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
			Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent,
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Final: final, Update: update,
		}
	}
	subscription.events <- child(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "child-progress-message",
		Content: eventstream.TextContent{Type: "text", Text: "I will inspect the request."},
	}, false)
	subscription.events <- child(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentThought,
		Content:       eventstream.TextContent{Type: "text", Text: "child thought"},
	}, false)
	subscription.events <- eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent,
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Notice:   "child live output resumed after a transient gap",
	}
	subscription.events <- child(eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "child-patch-1",
		Title: "Apply child patch", Kind: eventstream.ToolKindEdit, Status: eventstream.ToolStatusInProgress,
	}, false)
	childCommandTitle := "Run child command"
	subscription.events <- child(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "child-command-1",
		Title: &childCommandTitle, Status: &spawnStatus,
		Content: []eventstream.ToolCallContent{{
			Type: "terminal", TerminalID: "child-terminal-1",
			Content: eventstream.TextContent{Type: "text", Text: "nested output\n"},
		}},
	}, false)
	subscription.events <- child(eventstream.PlanUpdate{
		SessionUpdate: eventstream.UpdatePlan,
		Entries:       []eventstream.PlanEntry{{Content: "inspect child output", Status: "in_progress"}},
	}, false)

	subscription.events <- child(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "child-final-message",
		Content: eventstream.TextContent{Type: "text", Text: "I can inspect, "},
	}, false)
	subscription.events <- child(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "child-final-message",
		Content: eventstream.TextContent{Type: "text", Text: "edit, test, and review code."},
	}, false)
	subscription.events <- eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-yara", ParentTool: parent, Final: true,
		Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
	}
	if err := subscription.Close(); err != nil {
		t.Fatalf("close completed Task subscription: %v", err)
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
	// result, but must not emit a second parent FinalResponse.
	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "wait-call-1", Title: waitTitle,
			Kind: waitKind, Status: eventstream.ToolStatusPending,
			RawInput: map[string]any{"action": "wait", "handle": "yara"},
		},
	})
	sendGoldenTurnEnvelope(t, turn, callbacks, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "wait-call-1", Title: &waitTitle,
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

func newSpawnGoldenDirectRunnerAgent(
	t *testing.T,
	events <-chan *session.Event,
	streams taskstream.Service,
) (*runtimeacp.RuntimeAgent, string) {
	t.Helper()
	sessions := inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	})
	runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
		Runtime: spawnGoldenDirectRuntime{events: events}, Sessions: sessions, TaskStreams: streams,
		TaskStreamPrincipal: taskstream.Principal{ID: "user-1"},
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "golden-direct-runtime"}, nil
		},
		AppName: "caelis", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := runtimeAgent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return runtimeAgent, string(activeSession.SessionId)
}

func newSpawnGoldenAgent(t *testing.T, turn *testControlTurn, streams taskstream.Service) (*runtimeacp.RuntimeAgent, string) {
	t.Helper()
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	router := &testPromptRouter{result: controlprompt.Result{Handled: true, Turn: turn}}
	runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
		Runtime: runtime, Sessions: sessions, TaskStreams: streams,
		TaskStreamPrincipal: taskstream.Principal{ID: "user-1"},
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled prompt")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := runtimeAgent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	router.result.ActiveSessionID = "session-1"
	return runtimeAgent, string(activeSession.SessionId)
}

func sendGoldenRunnerEvent(
	t *testing.T,
	events chan<- *session.Event,
	callbacks *spawnGoldenCallbacks,
	event *session.Event,
) {
	t.Helper()
	events <- event
	waitGoldenNotification(t, callbacks)
}

func sendGoldenTurnEnvelope(t *testing.T, turn *testControlTurn, callbacks *spawnGoldenCallbacks, envelope eventstream.Envelope) {
	t.Helper()
	turn.events <- envelope
	waitGoldenNotification(t, callbacks)
}

func waitGoldenNotification(t *testing.T, callbacks *spawnGoldenCallbacks) eventstream.SessionNotification {
	t.Helper()
	select {
	case notification := <-callbacks.updates:
		return notification
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ACP session/update")
		return eventstream.SessionNotification{}
	}
}

type spawnGoldenCallbacks struct {
	mu            sync.Mutex
	notifications []eventstream.SessionNotification
	updates       chan eventstream.SessionNotification
}

func (c *spawnGoldenCallbacks) SessionUpdate(_ context.Context, notification eventstream.SessionNotification) error {
	c.mu.Lock()
	c.notifications = append(c.notifications, notification)
	c.mu.Unlock()
	c.updates <- notification
	return nil
}

func (*spawnGoldenCallbacks) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{}, nil
}

func (c *spawnGoldenCallbacks) snapshot() []eventstream.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]eventstream.SessionNotification(nil), c.notifications...)
}

type spawnGoldenDirectRuntime struct {
	events <-chan *session.Event
}

func (r spawnGoldenDirectRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle:  spawnGoldenDirectRunner(r),
	}, nil
}

func (spawnGoldenDirectRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type spawnGoldenDirectRunner struct {
	events <-chan *session.Event
}

func (spawnGoldenDirectRunner) RunID() string { return "direct-runner-1" }

func (r spawnGoldenDirectRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for event := range r.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (spawnGoldenDirectRunner) Submit(agent.Submission) error { return nil }

func (spawnGoldenDirectRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (spawnGoldenDirectRunner) Close() error { return nil }

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
