package runner

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/agent/llmagent"
	"github.com/OnslaughtSnail/caelis/model"
	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/skill"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/tool/mcp"
	"github.com/OnslaughtSnail/caelis/trace"
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

type runnerScriptedLLM struct {
	responses []runnerScriptedResponse
	requests  []model.Request
}

type runnerScriptedResponse struct {
	text      string
	toolCalls []model.ToolCallDelta
}

func (m *runnerScriptedLLM) Name() string { return "runner-scripted" }

func (m *runnerScriptedLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		if len(m.responses) == 0 {
			return
		}
		resp := m.responses[0]
		m.responses = m.responses[1:]
		if resp.text != "" {
			yield(model.ResponseEvent{TextDelta: resp.text}, nil)
		}
		for _, tc := range resp.toolCalls {
			yield(model.ResponseEvent{ToolCall: &tc}, nil)
		}
	}
}

type fakeRunnerMCPClient struct {
	tools    []mcp.RemoteTool
	callName string
	callArgs map[string]any
	closed   bool
}

func (c *fakeRunnerMCPClient) ListTools(context.Context) ([]mcp.RemoteTool, error) {
	return append([]mcp.RemoteTool(nil), c.tools...), nil
}

func (c *fakeRunnerMCPClient) CallTool(_ context.Context, name string, args map[string]any) (mcp.CallResult, error) {
	c.callName = name
	c.callArgs = args
	return mcp.CallResult{
		Content: []mcp.ContentPart{{Kind: mcp.ContentKindText, Text: "memory contents"}},
	}, nil
}

func (c *fakeRunnerMCPClient) Close() error {
	c.closed = true
	return nil
}

type fakeRunnerMCPFactory struct {
	client     *fakeRunnerMCPClient
	serverName string
	pluginRoot string
}

func (f *fakeRunnerMCPFactory) NewClient(_ context.Context, server caelisplugin.MCPServer, pluginRoot string) (mcp.Client, error) {
	f.serverName = server.Name
	f.pluginRoot = pluginRoot
	return f.client, nil
}

type runnerSandboxFactory struct {
	backend sandbox.Backend
}

func (f runnerSandboxFactory) Available(context.Context) ([]sandbox.Descriptor, error) {
	return []sandbox.Descriptor{{Name: f.backend.Name()}}, nil
}

func (f runnerSandboxFactory) Create(context.Context, sandbox.Config) (sandbox.Backend, error) {
	return f.backend, nil
}

type failingSandboxFactory struct{}

func (f failingSandboxFactory) Available(context.Context) ([]sandbox.Descriptor, error) {
	return []sandbox.Descriptor{{Name: "missing"}}, nil
}

func (f failingSandboxFactory) Create(context.Context, sandbox.Config) (sandbox.Backend, error) {
	return nil, fmt.Errorf("backend unavailable")
}

type blockingRunnerBackend struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRunnerBackend() *blockingRunnerBackend {
	return &blockingRunnerBackend{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingRunnerBackend) Name() string { return "blocking" }
func (b *blockingRunnerBackend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: "blocking"}, nil
}
func (b *blockingRunnerBackend) Run(ctx context.Context, _ sandbox.CommandRequest) (sandbox.CommandResult, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return sandbox.CommandResult{Stdout: []byte("later output"), ExitCode: 0}, nil
	case <-ctx.Done():
		return sandbox.CommandResult{}, ctx.Err()
	}
}
func (b *blockingRunnerBackend) FileSystem(context.Context, sandbox.Constraints) (sandbox.FileSystem, error) {
	return nil, nil
}
func (b *blockingRunnerBackend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}
func (b *blockingRunnerBackend) Close() error { return nil }

type fakeRunnerSpawnDelegator struct {
	req spawn.SpawnRequest
}

func (d *fakeRunnerSpawnDelegator) Spawn(_ tool.Context, req spawn.SpawnRequest) (spawn.SpawnResult, error) {
	d.req = req
	return spawn.SpawnResult{HandleID: "child-1", FinalMessage: "child done"}, nil
}

type overflowRetryLLM struct {
	requests []model.Request
}

func (m *overflowRetryLLM) Name() string { return "overflow-retry" }

func (m *overflowRetryLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		if callIndex == 0 {
			yield(model.ResponseEvent{}, &model.ContextOverflowError{Cause: fmt.Errorf("prompt is too long")})
			return
		}
		yield(model.ResponseEvent{TextDelta: "recovered"}, nil)
	}
}

type retryCompactor struct {
	calls int
}

func (c *retryCompactor) ShouldCompact([]model.Message, int) (bool, string) {
	return false, ""
}

func (c *retryCompactor) Compact(_ context.Context, msgs []model.Message, _ int) ([]model.Message, *session.Event, bool) {
	c.calls++
	return []model.Message{{
			Role:    model.RoleSystem,
			Content: []model.Part{{Text: "compact checkpoint"}},
		}},
		&session.Event{
			Kind:       session.EventKindCompaction,
			Visibility: session.VisibilityCanonical,
			CompactionPayload: &session.CompactionPayload{
				Reason:      "context overflow retry",
				Previous:    len(msgs),
				Remaining:   1,
				SummaryText: "compact checkpoint",
			},
		},
		true
}

type spawnTaskLoopLLM struct {
	requests []model.Request
	taskID   string
}

func (m *spawnTaskLoopLLM) Name() string { return "spawn-task-loop" }

func (m *spawnTaskLoopLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "spawn-1",
				Name:   "SPAWN",
				Args:   map[string]any{"agent": "reviewer", "prompt": "inspect files"},
			}}, nil)
		case 1:
			taskID := taskIDFromToolResult(req.Messages)
			if taskID == "" {
				yield(model.ResponseEvent{}, fmt.Errorf("missing SPAWN task id in model context"))
				return
			}
			m.taskID = taskID
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "wait-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "wait", "task_id": taskID},
			}}, nil)
		case 2:
			if !lastToolResultContains(req.Messages, "child done") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing child result in model context"))
				return
			}
			yield(model.ResponseEvent{TextDelta: "parent done"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

func taskIDFromToolResult(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, part := range messages[i].Content {
			if part.ToolResult == nil {
				continue
			}
			text := strings.TrimSpace(part.ToolResult.Content)
			if after, ok := strings.CutPrefix(text, "task started: "); ok {
				return strings.TrimSpace(after)
			}
		}
	}
	return ""
}

func lastToolResultContains(messages []model.Message, want string) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, part := range messages[i].Content {
			if part.ToolResult != nil && strings.Contains(part.ToolResult.Content, want) {
				return true
			}
		}
	}
	return false
}

type spawnCancelLoopLLM struct {
	requests []model.Request
	taskID   string
}

func (m *spawnCancelLoopLLM) Name() string { return "spawn-cancel-loop" }

func (m *spawnCancelLoopLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "spawn-1",
				Name:   "SPAWN",
				Args:   map[string]any{"agent": "reviewer", "prompt": "inspect files"},
			}}, nil)
		case 1:
			taskID := taskIDFromToolResult(req.Messages)
			if taskID == "" {
				yield(model.ResponseEvent{}, fmt.Errorf("missing SPAWN task id in model context"))
				return
			}
			m.taskID = taskID
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "cancel-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "cancel", "task_id": taskID},
			}}, nil)
		case 2:
			yield(model.ResponseEvent{TextDelta: "cancelled"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

type blockingTextLLM struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingTextLLM() *blockingTextLLM {
	return &blockingTextLLM{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (m *blockingTextLLM) Name() string { return "blocking-text" }

func (m *blockingTextLLM) Generate(ctx context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.once.Do(func() { close(m.started) })
	return func(yield func(model.ResponseEvent, error) bool) {
		select {
		case <-ctx.Done():
			yield(model.ResponseEvent{}, ctx.Err())
		case <-m.release:
			yield(model.ResponseEvent{TextDelta: "child done after cancel"}, nil)
		}
	}
}

type spawnContinueLoopLLM struct {
	requests []model.Request
	taskID   string
}

func (m *spawnContinueLoopLLM) Name() string { return "spawn-continue-loop" }

func (m *spawnContinueLoopLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		switch callIndex {
		case 0:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "spawn-1",
				Name:   "SPAWN",
				Args:   map[string]any{"agent": "reviewer", "prompt": "inspect files"},
			}}, nil)
		case 1:
			taskID := taskIDFromToolResult(req.Messages)
			if taskID == "" {
				yield(model.ResponseEvent{}, fmt.Errorf("missing SPAWN task id in model context"))
				return
			}
			m.taskID = taskID
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "wait-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "wait", "task_id": taskID},
			}}, nil)
		case 2:
			if !lastToolResultContains(req.Messages, "first done") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing first child result in model context"))
				return
			}
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "write-1",
				Name:   "TASK",
				Args:   map[string]any{"action": "write", "task_id": m.taskID, "input": "continue this"},
			}}, nil)
		case 3:
			yield(model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: "wait-2",
				Name:   "TASK",
				Args:   map[string]any{"action": "wait", "task_id": m.taskID},
			}}, nil)
		case 4:
			if !lastToolResultContains(req.Messages, "continued done") {
				yield(model.ResponseEvent{}, fmt.Errorf("missing continued child result in model context"))
				return
			}
			yield(model.ResponseEvent{TextDelta: "parent done"}, nil)
		default:
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected model call %d", callIndex))
		}
	}
}

type sequenceTextLLM struct {
	responses []string
	requests  []model.Request
}

func (m *sequenceTextLLM) Name() string { return "sequence-text" }

func (m *sequenceTextLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	callIndex := len(m.requests)
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		if callIndex >= len(m.responses) {
			yield(model.ResponseEvent{}, fmt.Errorf("unexpected child model call %d", callIndex))
			return
		}
		yield(model.ResponseEvent{TextDelta: m.responses[callIndex]}, nil)
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

type staticSkillRegistry struct {
	bundles []skill.Bundle
}

func (r staticSkillRegistry) List(context.Context) ([]skill.Bundle, error) {
	return append([]skill.Bundle(nil), r.bundles...), nil
}

func (r staticSkillRegistry) Load(_ context.Context, name string) (skill.Bundle, error) {
	for _, one := range r.bundles {
		if one.Name == name {
			return one, nil
		}
	}
	return skill.Bundle{}, fmt.Errorf("skill %q not found", name)
}

func TestRunnerIncludesSkillsInSystemPrompt(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	ml := &mockLLM{responses: []string{"ok"}}
	a = prepareAgent(a, ml)

	r, _ := New(Config{
		Agent:        a,
		Sessions:     svc,
		SystemPrompt: "base instructions",
		Skills: staticSkillRegistry{bundles: []skill.Bundle{{
			Name:        "lint",
			Description: "Run lint checks.",
			Path:        "/skills/lint/SKILL.md",
		}}},
	})

	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	}

	if len(ml.requests) != 1 {
		t.Fatalf("got %d model requests, want 1", len(ml.requests))
	}
	if len(ml.requests[0].Messages) == 0 || ml.requests[0].Messages[0].Role != model.RoleSystem {
		t.Fatalf("first model message = %#v, want system prompt", ml.requests[0].Messages)
	}
	text := ml.requests[0].Messages[0].Content[0].Text
	for _, want := range []string{"base instructions", "## Skills", "lint", "Run lint checks.", "/skills/lint/SKILL.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt = %q, want %q", text, want)
		}
	}
}

type staticPluginRegistry struct {
	resolved []caelisplugin.Resolved
}

func (r staticPluginRegistry) List(context.Context) ([]caelisplugin.Resolved, error) {
	return append([]caelisplugin.Resolved(nil), r.resolved...), nil
}

func (r staticPluginRegistry) Load(_ context.Context, name string) (caelisplugin.Resolved, error) {
	for _, one := range r.resolved {
		if one.Manifest.Name == name {
			return one, nil
		}
	}
	return caelisplugin.Resolved{}, fmt.Errorf("plugin %q not found", name)
}

func TestRunnerIncludesPluginSkillsInSystemPrompt(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	ml := &mockLLM{responses: []string{"ok"}}
	a = prepareAgent(a, ml)

	r, _ := New(Config{
		Agent:    a,
		Sessions: svc,
		Plugins: staticPluginRegistry{resolved: []caelisplugin.Resolved{{
			Manifest: caelisplugin.Manifest{Name: "superpowers"},
			Skills: []skill.Bundle{{
				Name:        "brainstorming",
				Description: "Refine ideas before implementation.",
				Path:        "/plugins/superpowers/skills/brainstorming/SKILL.md",
			}},
		}}},
	})

	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	}

	if len(ml.requests) != 1 {
		t.Fatalf("got %d model requests, want 1", len(ml.requests))
	}
	text := ml.requests[0].Messages[0].Content[0].Text
	for _, want := range []string{"## Skills", "brainstorming", "Refine ideas before implementation.", "/plugins/superpowers/skills/brainstorming/SKILL.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt = %q, want %q", text, want)
		}
	}
}

func TestRunnerIncludesPluginSystemPrompt(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	ml := &mockLLM{responses: []string{"ok"}}
	a = prepareAgent(a, ml)

	r, _ := New(Config{
		Agent:        a,
		Sessions:     svc,
		SystemPrompt: "base instructions",
		Plugins: staticPluginRegistry{resolved: []caelisplugin.Resolved{{
			Manifest: caelisplugin.Manifest{Name: "ops-pack"},
			Runtime: caelisplugin.RuntimeContributions{
				SystemPrompt: "plugin guardrails",
			},
		}}},
	})

	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	}

	text := ml.requests[0].Messages[0].Content[0].Text
	for _, want := range []string{"base instructions", "plugin guardrails"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt = %q, want %q", text, want)
		}
	}
}

func TestRunnerLoadsPluginMCPToolsIntoExecutor(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	ml := &runnerScriptedLLM{responses: []runnerScriptedResponse{
		{toolCalls: []model.ToolCallDelta{{
			CallID: "call-1",
			Name:   "memory.read",
			Args:   map[string]any{"path": "notes.md"},
		}}},
		{text: "done"},
	}}
	a := llmagent.New(llmagent.Config{
		Name: "test-agent", ModelRef: model.Ref{ModelID: "mock"},
	})
	a = a.Prepare(agent.PrepareRequest{LLM: ml}).(*llmagent.Agent)

	client := &fakeRunnerMCPClient{tools: []mcp.RemoteTool{{
		Name:        "read",
		Description: "Read memory.",
		InputSchema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"path": {Type: "string"},
			},
		},
	}}}
	factory := &fakeRunnerMCPFactory{client: client}

	r, _ := New(Config{
		Agent:            a,
		Sessions:         svc,
		MCPClientFactory: factory,
		Plugins: staticPluginRegistry{resolved: []caelisplugin.Resolved{{
			Manifest: caelisplugin.Manifest{Name: "superpowers"},
			Root:     "/plugins/superpowers",
			MCPServers: []caelisplugin.MCPServer{{
				Name:      "memory",
				Transport: "stdio",
				Command:   "node",
			}},
		}}},
	})

	var toolResult session.Event
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "read memory"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindToolResult {
			toolResult = evt
		}
	}

	if got, want := factory.serverName, "memory"; got != want {
		t.Fatalf("opened MCP server = %q, want %q", got, want)
	}
	if got, want := factory.pluginRoot, "/plugins/superpowers"; got != want {
		t.Fatalf("plugin root = %q, want %q", got, want)
	}
	if len(ml.requests) == 0 || len(ml.requests[0].Tools) != 1 || ml.requests[0].Tools[0].Name != "memory.read" {
		t.Fatalf("model tools = %#v, want memory.read", ml.requests)
	}
	if client.callName != "read" || client.callArgs["path"] != "notes.md" {
		t.Fatalf("mcp call = %s %#v, want read path notes.md", client.callName, client.callArgs)
	}
	if !client.closed {
		t.Fatal("MCP client was not closed after runner invocation")
	}
	if toolResult.ToolResultPayload == nil || toolResult.ToolResultPayload.Name != "memory.read" || len(toolResult.ToolResultPayload.Content) != 1 || toolResult.ToolResultPayload.Content[0].Text != "memory contents" {
		t.Fatalf("tool result = %#v, want memory.read result", toolResult)
	}
}

func TestRunnerTaskWaitResumesAcrossInvocations(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(&mockShellTool{})
	store := NewMemoryTaskStore()
	backend := newBlockingRunnerBackend()

	startLLM := &runnerScriptedLLM{responses: []runnerScriptedResponse{
		{toolCalls: []model.ToolCallDelta{{
			CallID: "call-start",
			Name:   "RUN_COMMAND",
			Args:   map[string]any{"command": "long", "wait": false},
		}}},
		{text: "started"},
	}}
	startAgent := llmagent.New(llmagent.Config{Name: "test-agent", Tools: []string{"RUN_COMMAND"}})
	startAgent = startAgent.Prepare(agent.PrepareRequest{LLM: startLLM}).(*llmagent.Agent)
	startRunner, _ := New(Config{
		Agent:        startAgent,
		Sessions:     svc,
		ToolRegistry: registry,
		Sandbox:      runnerSandboxFactory{backend: backend},
		TaskStore:    store,
	})

	var startResult string
	for evt, err := range startRunner.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "start"}}},
	}) {
		if err != nil {
			t.Fatalf("start Run error: %v", err)
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil && evt.ToolResultPayload.Name == "RUN_COMMAND" {
			startResult = evt.ToolResultPayload.Content[0].Text
		}
	}
	taskID := strings.TrimSpace(strings.TrimPrefix(startResult, "task started: "))
	if taskID == "" || taskID == startResult {
		t.Fatalf("start result = %q, want task started handle", startResult)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("backend command did not start")
	}

	waitLLM := &runnerScriptedLLM{responses: []runnerScriptedResponse{
		{toolCalls: []model.ToolCallDelta{{
			CallID: "call-wait",
			Name:   "TASK",
			Args:   map[string]any{"action": "wait", "task_id": taskID},
		}}},
		{text: "done"},
	}}
	waitAgent := llmagent.New(llmagent.Config{Name: "test-agent", Tools: []string{"RUN_COMMAND"}})
	waitAgent = waitAgent.Prepare(agent.PrepareRequest{LLM: waitLLM}).(*llmagent.Agent)
	waitRunner, _ := New(Config{
		Agent:        waitAgent,
		Sessions:     svc,
		ToolRegistry: registry,
		Sandbox:      runnerSandboxFactory{backend: backend},
		TaskStore:    store,
	})

	done := make(chan session.Event, 1)
	errs := make(chan error, 1)
	go func() {
		var waitResult session.Event
		for evt, err := range waitRunner.Run(ctx, RunRequest{
			SessionRef:  sess.Ref,
			UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "wait"}}},
		}) {
			if err != nil {
				errs <- err
				return
			}
			if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil && evt.ToolResultPayload.Name == "TASK" {
				waitResult = evt
			}
		}
		done <- waitResult
	}()

	select {
	case evt := <-done:
		t.Fatalf("TASK wait returned before backend completed: %#v", evt)
	case err := <-errs:
		t.Fatalf("wait Run error before backend completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(backend.release)

	var waitResult session.Event
	select {
	case err := <-errs:
		t.Fatalf("wait Run error: %v", err)
	case waitResult = <-done:
	case <-time.After(time.Second):
		t.Fatal("TASK wait did not resume after backend completed")
	}
	if waitResult.ToolResultPayload == nil || len(waitResult.ToolResultPayload.Content) != 1 || waitResult.ToolResultPayload.Content[0].Text != "later output" {
		t.Fatalf("wait result = %#v, want later output", waitResult)
	}
}

func TestRunnerWiresSpawnDelegator(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(spawn.All()[0])
	delegator := &fakeRunnerSpawnDelegator{}

	ml := &runnerScriptedLLM{responses: []runnerScriptedResponse{
		{toolCalls: []model.ToolCallDelta{{
			CallID: "spawn-1",
			Name:   "SPAWN",
			Args:   map[string]any{"agent": "reviewer", "prompt": "review this"},
		}}},
		{text: "done"},
	}}
	a := llmagent.New(llmagent.Config{Name: "test-agent", Tools: []string{"SPAWN"}})
	a = a.Prepare(agent.PrepareRequest{LLM: ml}).(*llmagent.Agent)
	r, _ := New(Config{
		Agent:          a,
		Sessions:       svc,
		ToolRegistry:   registry,
		SpawnDelegator: delegator,
	})

	var result session.Event
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil && evt.ToolResultPayload.Name == "SPAWN" {
			result = evt
		}
	}

	if delegator.req.AgentName != "reviewer" || delegator.req.Prompt != "review this" || delegator.req.RunID == "" {
		t.Fatalf("delegator request = %#v", delegator.req)
	}
	if result.ToolResultPayload == nil || len(result.ToolResultPayload.Content) != 1 || result.ToolResultPayload.Content[0].Text != "child done" {
		t.Fatalf("spawn result = %#v, want child done", result)
	}
}

func TestRunnerSpawnCreatesChildSession(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	parentSession, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test",
		UserID:  "u", WorkspaceKey: "ws",
		Workspace: session.Workspace{Root: "/workspace"},
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(spawn.All()[0])
	store := NewMemoryTaskStore()

	child := llmagent.New(llmagent.Config{Name: "reviewer"})
	child = child.Prepare(agent.PrepareRequest{LLM: &mockLLM{responses: []string{"child done"}}}).(*llmagent.Agent)
	parentLLM := &runnerScriptedLLM{responses: []runnerScriptedResponse{
		{toolCalls: []model.ToolCallDelta{{
			CallID: "spawn-1",
			Name:   "SPAWN",
			Args:   map[string]any{"agent": "reviewer", "prompt": "inspect files"},
		}}},
		{text: "parent done"},
	}}
	parent := llmagent.New(llmagent.Config{Name: "parent", Tools: []string{"SPAWN"}, SubAgents: []agent.Agent{child}})
	parent = parent.Prepare(agent.PrepareRequest{LLM: parentLLM}).(*llmagent.Agent)

	r, _ := New(Config{
		Agent:        parent,
		Sessions:     svc,
		ToolRegistry: registry,
		TaskStore:    store,
	})

	var spawnResult session.Event
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  parentSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil && evt.ToolResultPayload.Name == "SPAWN" {
			spawnResult = evt
		}
	}
	if spawnResult.ToolResultPayload == nil || len(spawnResult.ToolResultPayload.Content) != 1 {
		t.Fatalf("spawn result = %#v, want task handle", spawnResult)
	}
	taskID := strings.TrimSpace(strings.TrimPrefix(spawnResult.ToolResultPayload.Content[0].Text, "task started: "))
	if taskID == "" || taskID == spawnResult.ToolResultPayload.Content[0].Text {
		t.Fatalf("spawn output = %q, want task started handle", spawnResult.ToolResultPayload.Content[0].Text)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	snap, err := store.WaitTask(waitCtx, taskID)
	if err != nil {
		t.Fatalf("WaitTask() error = %v", err)
	}
	if snap.State != TaskStateCompleted || snap.Output != "child done" {
		t.Fatalf("spawn task snapshot = %#v, want completed child output", snap)
	}

	listed, err := svc.List(ctx, session.ListRequest{AppName: "test", UserID: "u", WorkspaceKey: "ws"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed.Sessions) != 2 {
		t.Fatalf("sessions = %#v, want parent and child", listed.Sessions)
	}
	var childSession session.Session
	for _, one := range listed.Sessions {
		if one.Ref.SessionID != parentSession.Ref.SessionID {
			childSession = one
		}
	}
	if childSession.Ref.SessionID == "" || childSession.Workspace.Root != "/workspace" || childSession.State["parent_session"] != parentSession.Ref.String() {
		t.Fatalf("child session = %#v, want parent-linked child workspace", childSession)
	}
	events, err := svc.Events(ctx, session.EventsRequest{SessionRef: childSession.Ref})
	if err != nil {
		t.Fatalf("child Events() error = %v", err)
	}
	if len(events) < 2 || events[0].TextContent() != "inspect files" || events[1].TextContent() != "child done" {
		t.Fatalf("child events = %#v, want prompt and final answer", events)
	}
}

func TestRunnerSpawnIsTaskBackedAndWaitable(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	parentSession, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test",
		UserID:  "u", WorkspaceKey: "ws",
		Workspace: session.Workspace{Root: "/workspace"},
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(spawn.All()[0])
	store := NewMemoryTaskStore()

	child := llmagent.New(llmagent.Config{Name: "reviewer"})
	child = child.Prepare(agent.PrepareRequest{LLM: &mockLLM{responses: []string{"child done"}}}).(*llmagent.Agent)
	parentLLM := &spawnTaskLoopLLM{}
	parent := llmagent.New(llmagent.Config{Name: "parent", Tools: []string{"SPAWN"}, SubAgents: []agent.Agent{child}})
	parent = parent.Prepare(agent.PrepareRequest{LLM: parentLLM}).(*llmagent.Agent)

	r, _ := New(Config{
		Agent:        parent,
		Sessions:     svc,
		ToolRegistry: registry,
		TaskStore:    store,
	})

	var (
		spawnResult session.Event
		taskResult  session.Event
		final       string
	)
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  parentSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindAssistant {
			final = evt.TextContent()
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil {
			switch evt.ToolResultPayload.Name {
			case "SPAWN":
				spawnResult = evt
			case "TASK":
				taskResult = evt
			}
		}
	}

	if parentLLM.taskID == "" {
		t.Fatal("model did not receive SPAWN task id")
	}
	if spawnResult.ToolResultPayload == nil || len(spawnResult.ToolResultPayload.Content) != 1 {
		t.Fatalf("spawn result = %#v, want task handle output", spawnResult)
	}
	if got := spawnResult.ToolResultPayload.Content[0].Text; got != "task started: "+parentLLM.taskID {
		t.Fatalf("spawn output = %q, want task handle %q", got, parentLLM.taskID)
	}
	if taskResult.ToolResultPayload == nil || len(taskResult.ToolResultPayload.Content) != 1 || taskResult.ToolResultPayload.Content[0].Text != "child done" {
		t.Fatalf("task result = %#v, want child output", taskResult)
	}
	if final != "parent done" {
		t.Fatalf("final = %q, want parent done", final)
	}
	snap, ok, err := store.LoadTask(ctx, parentLLM.taskID)
	if err != nil {
		t.Fatalf("LoadTask() error = %v", err)
	}
	if !ok || snap.State != TaskStateCompleted || snap.Output != "child done" {
		t.Fatalf("spawn task snapshot = %#v ok=%v, want completed child output", snap, ok)
	}
}

func TestRunnerSpawnCancelKeepsTaskCancelled(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	parentSession, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test",
		UserID:  "u", WorkspaceKey: "ws",
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(spawn.All()[0])
	store := NewMemoryTaskStore()

	childLLM := newBlockingTextLLM()
	child := llmagent.New(llmagent.Config{Name: "reviewer"})
	child = child.Prepare(agent.PrepareRequest{LLM: childLLM}).(*llmagent.Agent)
	parentLLM := &spawnCancelLoopLLM{}
	parent := llmagent.New(llmagent.Config{Name: "parent", Tools: []string{"SPAWN"}, SubAgents: []agent.Agent{child}})
	parent = parent.Prepare(agent.PrepareRequest{LLM: parentLLM}).(*llmagent.Agent)

	r, _ := New(Config{
		Agent:        parent,
		Sessions:     svc,
		ToolRegistry: registry,
		TaskStore:    store,
	})

	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  parentSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	}
	if parentLLM.taskID == "" {
		t.Fatal("model did not receive SPAWN task id")
	}
	select {
	case <-childLLM.started:
	case <-time.After(time.Second):
		t.Fatal("child model did not start")
	}
	snap, ok, err := store.LoadTask(ctx, parentLLM.taskID)
	if err != nil {
		t.Fatalf("LoadTask() error = %v", err)
	}
	if !ok || snap.State != TaskStateCancelled {
		t.Fatalf("snapshot after cancel = %#v ok=%v, want cancelled", snap, ok)
	}
	close(childLLM.release)
	time.Sleep(20 * time.Millisecond)
	snap, ok, err = store.LoadTask(ctx, parentLLM.taskID)
	if err != nil {
		t.Fatalf("LoadTask() after release error = %v", err)
	}
	if !ok || snap.State != TaskStateCancelled {
		t.Fatalf("snapshot after child completion = %#v ok=%v, want still cancelled", snap, ok)
	}
}

func TestRunnerSpawnTaskWriteContinuesChildSession(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	parentSession, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test",
		UserID:  "u", WorkspaceKey: "ws",
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(spawn.All()[0])
	store := NewMemoryTaskStore()

	childLLM := &sequenceTextLLM{responses: []string{"first done", "continued done"}}
	child := llmagent.New(llmagent.Config{Name: "reviewer"})
	child = child.Prepare(agent.PrepareRequest{LLM: childLLM}).(*llmagent.Agent)
	parentLLM := &spawnContinueLoopLLM{}
	parent := llmagent.New(llmagent.Config{Name: "parent", Tools: []string{"SPAWN"}, SubAgents: []agent.Agent{child}})
	parent = parent.Prepare(agent.PrepareRequest{LLM: parentLLM}).(*llmagent.Agent)

	r, _ := New(Config{
		Agent:        parent,
		Sessions:     svc,
		ToolRegistry: registry,
		TaskStore:    store,
	})

	var final string
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  parentSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "delegate"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindAssistant {
			final = evt.TextContent()
		}
	}
	if final != "parent done" {
		t.Fatalf("final = %q, want parent done", final)
	}
	if parentLLM.taskID == "" {
		t.Fatal("model did not receive SPAWN task id")
	}
	snap, ok, err := store.LoadTask(ctx, parentLLM.taskID)
	if err != nil {
		t.Fatalf("LoadTask() error = %v", err)
	}
	if !ok || snap.State != TaskStateCompleted || snap.Output != "continued done" {
		t.Fatalf("snapshot = %#v ok=%v, want continued child output", snap, ok)
	}
	if len(childLLM.requests) != 2 {
		t.Fatalf("child model calls = %d, want initial and continuation", len(childLLM.requests))
	}
	if childLLM.requests[1].Messages[len(childLLM.requests[1].Messages)-1].TextContent() != "continue this" {
		t.Fatalf("continuation request messages = %#v, want continuation prompt", childLLM.requests[1].Messages)
	}
}

func TestRunnerRetriesAfterContextOverflowWithCompaction(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})
	_, _ = svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind:       session.EventKindUser,
		Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "large prior context"}},
		},
	})

	llm := &overflowRetryLLM{}
	compactor := &retryCompactor{}
	a := llmagent.New(llmagent.Config{Name: "test-agent"})
	a = a.Prepare(agent.PrepareRequest{LLM: llm}).(*llmagent.Agent)
	r, _ := New(Config{Agent: a, Sessions: svc, Compactor: compactor})

	var final string
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "current turn"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindAssistant {
			final = evt.TextContent()
		}
	}
	if final != "recovered" {
		t.Fatalf("final = %q, want recovered", final)
	}
	if len(llm.requests) != 2 {
		t.Fatalf("model requests = %d, want initial overflow and retry", len(llm.requests))
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want overflow retry compaction", compactor.calls)
	}
	if len(llm.requests[1].Messages) == 0 || llm.requests[1].Messages[0].Content[0].Text != "compact checkpoint" {
		t.Fatalf("retry messages = %#v, want compact checkpoint", llm.requests[1].Messages)
	}
	persisted, err := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var sawCompaction bool
	for _, evt := range persisted {
		if evt.Kind == session.EventKindCompaction && evt.CompactionPayload != nil && evt.CompactionPayload.Reason == "context overflow retry" {
			sawCompaction = true
		}
	}
	if !sawCompaction {
		t.Fatalf("persisted events = %#v, want overflow compaction event", persisted)
	}
}

func TestRunnerFailsClosedWhenSandboxCreateFails(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(&mockShellTool{})
	llm := &runnerScriptedLLM{responses: []runnerScriptedResponse{{text: "should not run"}}}
	a := llmagent.New(llmagent.Config{Name: "test-agent", Tools: []string{"RUN_COMMAND"}})
	a = a.Prepare(agent.PrepareRequest{LLM: llm}).(*llmagent.Agent)
	r, _ := New(Config{
		Agent:        a,
		Sessions:     svc,
		ToolRegistry: registry,
		Sandbox:      failingSandboxFactory{},
	})

	var runErr error
	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "run"}}},
	}) {
		if err != nil {
			runErr = err
			break
		}
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "create sandbox") {
		t.Fatalf("run error = %v, want sandbox create failure", runErr)
	}
	if len(llm.requests) != 0 {
		t.Fatalf("model requests = %d, want fail closed before model call", len(llm.requests))
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

func TestRunnerToolObserverEmitsTransientNotices(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})

	registry := tool.NewMemoryRegistry()
	registry.Register(&echoTool{})

	a := llmagent.New(llmagent.Config{
		Name:  "test-agent",
		Tools: []string{"ECHO"},
	})
	a = a.Prepare(agent.PrepareRequest{
		LLM: &runnerScriptedLLM{responses: []runnerScriptedResponse{
			{toolCalls: []model.ToolCallDelta{{CallID: "c1", Name: "ECHO", Args: map[string]any{"text": "observed"}}}},
			{text: "done"},
		}},
	}).(*llmagent.Agent)

	r, _ := New(Config{Agent: a, Sessions: svc, ToolRegistry: registry})

	var transientNotices int
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "go"}}},
	}) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if evt.Kind == session.EventKindNotice && evt.Visibility == session.VisibilityUIOnly {
			transientNotices++
			if evt.SessionRef != sess.Ref || evt.RunID == "" {
				t.Fatalf("transient notice identity = %#v/%q, want session and run id", evt.SessionRef, evt.RunID)
			}
		}
	}
	if transientNotices < 2 {
		t.Fatalf("transient observer notices = %d, want before and after notices", transientNotices)
	}

	persisted, err := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	for _, evt := range persisted {
		if evt.Kind == session.EventKindNotice && evt.Visibility == session.VisibilityUIOnly {
			t.Fatalf("ui_only observer notice persisted: %#v", evt)
		}
	}
}

func TestRunnerInvokesHooksAndTracer(t *testing.T) {
	svc := session.InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "test", UserID: "u", WorkspaceKey: "ws",
	})
	registry := tool.NewMemoryRegistry()
	registry.Register(&echoTool{})
	hook := &recordingHook{}
	tracer := &recordingTracer{}

	a := llmagent.New(llmagent.Config{
		Name:  "hook-agent",
		Tools: []string{"ECHO"},
	})
	a = a.Prepare(agent.PrepareRequest{
		LLM: &runnerScriptedLLM{responses: []runnerScriptedResponse{
			{toolCalls: []model.ToolCallDelta{{CallID: "call-1", Name: "ECHO", Args: map[string]any{"text": "hi"}}}},
			{text: "done"},
		}},
	}).(*llmagent.Agent)

	r, _ := New(Config{
		Agent:        a,
		Sessions:     svc,
		ToolRegistry: registry,
		Hooks:        []agent.Hook{hook},
		Tracer:       tracer,
	})
	for _, err := range r.Run(ctx, RunRequest{
		SessionRef:  sess.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "run echo"}}},
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}

	wantHookEvents := []string{
		"before-invocation:hook-agent",
		"before-tool:ECHO:call-1",
		"after-tool:ECHO:call-1:false",
		"after-invocation:hook-agent:false",
	}
	if strings.Join(hook.events, "|") != strings.Join(wantHookEvents, "|") {
		t.Fatalf("hook events = %#v, want %#v", hook.events, wantHookEvents)
	}
	if !tracer.hasSpan("runner.invocation", true) || !tracer.hasSpan("tool.ECHO", true) {
		t.Fatalf("trace spans = %#v, want invocation and tool spans ended", tracer.spans)
	}
}

type recordingHook struct {
	events []string
}

func (h *recordingHook) BeforeInvocation(_ context.Context, evt agent.InvocationHook) error {
	h.events = append(h.events, "before-invocation:"+evt.AgentName)
	return nil
}

func (h *recordingHook) AfterInvocation(_ context.Context, evt agent.InvocationHookResult) error {
	h.events = append(h.events, fmt.Sprintf("after-invocation:%s:%t", evt.AgentName, evt.Error != nil))
	return nil
}

func (h *recordingHook) BeforeTool(_ context.Context, evt agent.ToolHook) error {
	h.events = append(h.events, "before-tool:"+evt.ToolName+":"+evt.CallID)
	return nil
}

func (h *recordingHook) AfterTool(_ context.Context, evt agent.ToolHookResult) error {
	h.events = append(h.events, fmt.Sprintf("after-tool:%s:%s:%t", evt.ToolName, evt.CallID, evt.Result.IsError))
	return nil
}

type recordingTracer struct {
	spans []*recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, start trace.SpanStart) (context.Context, trace.Span) {
	span := &recordingSpan{name: start.Name}
	t.spans = append(t.spans, span)
	return ctx, span
}

func (t *recordingTracer) hasSpan(name string, ended bool) bool {
	for _, span := range t.spans {
		if span.name == name && span.ended == ended {
			return true
		}
	}
	return false
}

type recordingSpan struct {
	name  string
	ended bool
}

func (s *recordingSpan) End(trace.SpanEnd) {
	s.ended = true
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
