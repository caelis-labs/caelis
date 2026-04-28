package acp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
	sdksubagent "github.com/OnslaughtSnail/caelis/sdk/subagent"
)

func TestRunnerHandleUpdatePublishesChildStream(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor: sdkdelegation.Anchor{
			TaskID:    "task-1",
			SessionID: "child-1",
			Agent:     "self",
			AgentID:   "self-1",
		},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	raw, _ := json.Marshal(sdkacpclient.TextChunk{Type: "text", Text: "child output"})

	runner.handleUpdate(run, sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.ContentChunk{
			SessionUpdate: sdkacpclient.UpdateAgentMessage,
			Content:       raw,
		},
	})

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Ref.TaskID != "task-1" || got.Ref.SessionID != "child-1" || got.Text != "child output" || !got.Running {
		t.Fatalf("stream frame = %#v", got)
	}
	if got.Event == nil || got.Event.Type != sdksession.EventTypeAssistant || got.Event.Text != "child output" {
		t.Fatalf("stream event = %#v, want assistant child output", got.Event)
	}
	if got.Event.Scope == nil || got.Event.Scope.Participant.Kind != sdksession.ParticipantKindSubagent || got.Event.Scope.Participant.DelegationID != "task-1" {
		t.Fatalf("stream event scope = %#v, want subagent task scope", got.Event.Scope)
	}
}

func TestRunnerHandleUpdatePublishesStructuredToolAndPlanEvents(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  sdkdelegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.ToolCall{
			SessionUpdate: sdkacpclient.UpdateToolCall,
			ToolCallID:    "call-1",
			Kind:          "execute",
			Title:         "run go test",
			Status:        "pending",
			RawInput:      map[string]any{"command": "go test ./tui/tuiapp/..."},
		},
	})
	runner.handleUpdate(run, sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.ToolCallUpdate{
			SessionUpdate: sdkacpclient.UpdateToolCallState,
			ToolCallID:    "call-1",
			Kind:          stringPtr("execute"),
			Title:         stringPtr("run go test"),
			Status:        stringPtr("completed"),
			RawInput:      map[string]any{"command": "go test ./tui/tuiapp/..."},
			RawOutput:     map[string]any{"stdout": "ok\n", "exit_code": 0},
		},
	})
	runner.handleUpdate(run, sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.PlanUpdate{
			SessionUpdate: sdkacpclient.UpdatePlan,
			Entries:       []sdkacpclient.PlanEntry{{Content: "Run tests", Status: "completed"}},
		},
	})

	if got := len(sink.frames); got != 3 {
		t.Fatalf("stream frames = %#v, want three structured updates", sink.frames)
	}
	for i, frame := range sink.frames {
		if frame.Text != "" {
			t.Fatalf("structured frame %d text = %q, want empty text fallback", i, frame.Text)
		}
	}
	callEvent := sink.frames[0].Event
	if callEvent == nil || callEvent.Type != sdksession.EventTypeToolCall || callEvent.Protocol == nil || callEvent.Protocol.ToolCall == nil {
		t.Fatalf("tool call event = %#v", callEvent)
	}
	if callEvent.Protocol.ToolCall.Name != "run go test" || callEvent.Protocol.ToolCall.Kind != "execute" || callEvent.Protocol.ToolCall.RawInput["command"] != "go test ./tui/tuiapp/..." {
		t.Fatalf("tool call payload = %#v", callEvent.Protocol.ToolCall)
	}
	resultEvent := sink.frames[1].Event
	if resultEvent == nil || resultEvent.Type != sdksession.EventTypeToolResult || resultEvent.Protocol == nil || resultEvent.Protocol.ToolCall == nil {
		t.Fatalf("tool result event = %#v", resultEvent)
	}
	if resultEvent.Protocol.ToolCall.RawOutput["stdout"] != "ok\n" {
		t.Fatalf("tool result payload = %#v", resultEvent.Protocol.ToolCall)
	}
	planEvent := sink.frames[2].Event
	if planEvent == nil || planEvent.Type != sdksession.EventTypePlan || planEvent.Protocol == nil || planEvent.Protocol.Plan == nil {
		t.Fatalf("plan event = %#v", planEvent)
	}
	if len(planEvent.Protocol.Plan.Entries) != 1 || planEvent.Protocol.Plan.Entries[0].Content != "Run tests" {
		t.Fatalf("plan entries = %#v", planEvent.Protocol.Plan.Entries)
	}
}

func TestRunnerKeepsCodexWebSearchToolIdentity(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  sdkdelegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "codex", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.ToolCallUpdate{
			SessionUpdate: sdkacpclient.UpdateToolCallState,
			ToolCallID:    "ws_1",
			Kind:          stringPtr("fetch"),
			Title:         stringPtr("Searching for: weather: Shanghai, China"),
			Status:        stringPtr("in_progress"),
			RawInput:      map[string]any{"query": "weather: Shanghai, China"},
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one structured search update", sink.frames)
	}
	frame := sink.frames[0]
	if frame.Text != "" {
		t.Fatalf("stream frame text = %q, want no fallback text", frame.Text)
	}
	event := frame.Event
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		t.Fatalf("stream event = %#v, want structured tool call", event)
	}
	if got := event.Protocol.ToolCall.Name; got != "Searching for: weather: Shanghai, China" {
		t.Fatalf("tool name = %q, want ACP title", got)
	}
	if got := event.Protocol.ToolCall.Kind; got != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", got)
	}
	if got := event.Protocol.ToolCall.RawInput["query"]; got != "weather: Shanghai, China" {
		t.Fatalf("raw input query = %#v", got)
	}
}

func TestRunnerCodexACPWebSearchLiveE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAELIS_CODEX_ACP_E2E")) != "1" {
		t.Skip("set CAELIS_CODEX_ACP_E2E=1 to run local Codex ACP live E2E")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx is not installed")
	}

	repo := repoRootForRunnerTest(t)
	registry, err := NewRegistry([]AgentConfig{{
		Name:        "codex",
		Description: "local Codex ACP",
		Command:     "npx",
		Args:        []string{"--yes", "@zed-industries/codex-acp@^0.12.0"},
		WorkDir:     repo,
		Env: map[string]string{
			"npm_config_cache": "/tmp/caelis-npm-cache",
		},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	sink := &recordingStreams{}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	anchor, first, err := runner.Spawn(ctx, sdksubagent.SpawnContext{
		TaskID:  "live-codex-weather",
		CWD:     repo,
		Streams: sink,
	}, sdkdelegation.Request{
		Agent:  "codex",
		Prompt: "查询一下上海今天的天气",
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if !first.Running {
		t.Fatalf("Spawn() result = %+v, want yielded running task", first)
	}
	result, err := runner.Wait(ctx, anchor, 180_000)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.State != sdkdelegation.StateCompleted {
		t.Fatalf("Wait() result = %+v, want completed", result)
	}

	var sawFetchTool bool
	var sawAssistant bool
	for i, frame := range sink.frames {
		if frame.Event != nil && frame.Event.Protocol != nil && frame.Event.Protocol.ToolCall != nil {
			call := frame.Event.Protocol.ToolCall
			t.Logf("frame[%d] tool text=%q name=%q kind=%q title=%q status=%q", i, frame.Text, call.Name, call.Kind, call.Title, call.Status)
			if strings.EqualFold(strings.TrimSpace(call.Kind), "fetch") {
				sawFetchTool = true
			}
			if frame.Text != "" {
				t.Fatalf("frame[%d] structured tool text = %q, want empty text fallback", i, frame.Text)
			}
			continue
		}
		if frame.Text != "" {
			t.Logf("frame[%d] text stream=%q text=%q", i, frame.Stream, frame.Text)
			if strings.Contains(frame.Text, "Searching the Web") || strings.Contains(frame.Text, "Searching for:") {
				t.Fatalf("frame[%d] rendered ACP tool activity as text: %q", i, frame.Text)
			}
			if strings.Contains(frame.Text, "上海") {
				sawAssistant = true
			}
		}
	}
	if !sawFetchTool {
		t.Fatalf("live frames did not include a structured fetch tool event; frames=%#v", sink.frames)
	}
	if !sawAssistant || !strings.Contains(result.Result, "上海") {
		t.Fatalf("live result = %+v, sawAssistant=%v", result, sawAssistant)
	}
}

func TestRunnerHandleUpdateUsesAgentMessageDeltas(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  sdkdelegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, sdkacpclient.UpdateUserMessage, "ignored prompt"))
	runner.handleUpdate(run, contentUpdate(t, sdkacpclient.UpdateAgentMessage, "我来按步骤"))
	runner.handleUpdate(run, contentUpdate(t, sdkacpclient.UpdateAgentMessage, "我来按步骤执行"))
	runner.handleUpdate(run, contentUpdate(t, sdkacpclient.UpdateAgentMessage, "我来按步骤执行这个任务。"))

	if got := len(sink.frames); got != 3 {
		t.Fatalf("stream frames = %#v, want three agent delta updates", sink.frames)
	}
	var rendered string
	for _, frame := range sink.frames {
		rendered += frame.Text
	}
	if rendered != "我来按步骤执行这个任务。" {
		t.Fatalf("rendered stream = %q, want deduped final text", rendered)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "我来按步骤执行这个任务。" {
		t.Fatalf("run.result = %q, want deduped final text", result)
	}
}

func TestRunnerHandleUpdatePublishesStructuredThoughtEvent(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  sdkdelegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, sdkacpclient.UpdateAgentThought, "thinking about the command"))

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one thought frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Stream != "reasoning" || got.Text != "thinking about the command" {
		t.Fatalf("stream frame = %#v, want reasoning thought text", got)
	}
	if got.Event == nil || got.Event.Protocol == nil || got.Event.Protocol.UpdateType != sdkacpclient.UpdateAgentThought || got.Event.Text != "thinking about the command" {
		t.Fatalf("stream event = %#v, want structured thought event", got.Event)
	}
}

func TestRunnerHandleUpdatePreservesWhitespaceThoughtChunk(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  sdkdelegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, sdkacpclient.UpdateAgentThought, " "))

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one whitespace thought frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Stream != "reasoning" || got.Text != " " {
		t.Fatalf("stream frame = %#v, want single-space reasoning chunk", got)
	}
	if got.Event == nil || got.Event.Text != " " {
		t.Fatalf("stream event = %#v, want single-space structured thought event", got.Event)
	}
}

func TestRunnerHandleUpdateAcceptsStringContentChunks(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  sdkdelegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "copilot-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   sdkdelegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	raw, err := json.Marshal("string chunk")
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	runner.handleUpdate(run, sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.ContentChunk{
			SessionUpdate: sdkacpclient.UpdateAgentMessage,
			Content:       raw,
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one string-content frame", sink.frames)
	}
	if got := sink.frames[0].Text; got != "string chunk" {
		t.Fatalf("stream frame text = %q, want string chunk", got)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "string chunk" {
		t.Fatalf("run.result = %q, want string chunk", result)
	}
}

func TestRunnerSpawnChildSurvivesCallerContextCancelAfterYield(t *testing.T) {
	repo := repoRootForRunnerTest(t)
	root := t.TempDir()
	childBin := filepath.Join(t.TempDir(), "e2eagent")
	build := exec.Command("go", "build", "-o", childBin, "./acpbridge/cmd/e2eagent")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build e2eagent: %v\n%s", err, string(output))
	}
	registry, err := NewRegistry([]AgentConfig{{
		Name:        "self",
		Description: "self child",
		Command:     childBin,
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY":    "child survived caller cancel",
			"SDK_ACP_STUB_DELAY_MS": "150",
			"SDK_ACP_SESSION_ROOT":  filepath.Join(root, "child-sessions"),
			"SDK_ACP_TASK_ROOT":     filepath.Join(root, "child-tasks"),
		},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	anchor, result, err := runner.Spawn(ctx, sdksubagent.SpawnContext{
		TaskID: "task-cancel",
		CWD:    t.TempDir(),
	}, sdkdelegation.Request{
		Agent:  "self",
		Prompt: "Reply exactly: child survived caller cancel",
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if !result.Running {
		t.Fatalf("Spawn() result = %+v, want yielded running task", result)
	}
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer waitCancel()
	result, err = runner.Wait(waitCtx, anchor, 10_000)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.Running || result.State != sdkdelegation.StateCompleted {
		t.Fatalf("Wait() result = %+v, want completed child", result)
	}
	if result.Result != "child survived caller cancel" {
		t.Fatalf("Wait() result text = %q, want child reply", result.Result)
	}
}

func contentUpdate(t *testing.T, kind string, text string) sdkacpclient.UpdateEnvelope {
	t.Helper()
	raw, err := json.Marshal(sdkacpclient.TextChunk{Type: "text", Text: text})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return sdkacpclient.UpdateEnvelope{
		SessionID: "child-1",
		Update: sdkacpclient.ContentChunk{
			SessionUpdate: kind,
			Content:       raw,
		},
	}
}

func stringPtr(value string) *string {
	return &value
}

type recordingStreams struct {
	frames []sdkstream.Frame
}

func (s *recordingStreams) PublishStream(frame sdkstream.Frame) {
	s.frames = append(s.frames, sdkstream.CloneFrame(frame))
}

func repoRootForRunnerTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}
