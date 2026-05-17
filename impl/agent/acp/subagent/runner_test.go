package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

func TestRunnerHandleUpdatePublishesChildStream(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor: delegation.Anchor{
			TaskID:    "task-1",
			SessionID: "child-1",
			Agent:     "self",
			AgentID:   "self-1",
		},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	raw, _ := json.Marshal(client.TextChunk{Type: "text", Text: "child output"})

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ContentChunk{
			SessionUpdate: client.UpdateAgentMessage,
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
	if got.Event == nil || got.Event.Type != session.EventTypeAssistant || got.Event.Text != "child output" {
		t.Fatalf("stream event = %#v, want assistant child output", got.Event)
	}
	if got.Event.Scope == nil || got.Event.Scope.Participant.Kind != session.ParticipantKindSubagent || got.Event.Scope.Participant.DelegationID != "task-1" {
		t.Fatalf("stream event scope = %#v, want subagent task scope", got.Event.Scope)
	}
}

func TestRunnerHandleUpdatePublishesStructuredToolAndPlanEvents(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "call-1",
			Kind:          "execute",
			Title:         "run go test",
			Status:        "pending",
			RawInput:      map[string]any{"command": "go test ./surfaces/tui/app/..."},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Kind:          stringPtr("execute"),
			Title:         stringPtr("run go test"),
			Status:        stringPtr("completed"),
			RawInput:      map[string]any{"command": "go test ./surfaces/tui/app/..."},
			RawOutput:     map[string]any{"stdout": "ok\n", "exit_code": 0},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.PlanUpdate{
			SessionUpdate: client.UpdatePlan,
			Entries:       []client.PlanEntry{{Content: "Run tests", Status: "completed"}},
		},
	})

	if got := len(sink.frames); got != 3 {
		t.Fatalf("stream frames = %#v, want three structured updates", sink.frames)
	}
	wantText := []string{
		"run go test\n",
		"run go test completed\n",
		"Plan Run tests completed\n",
	}
	for i, frame := range sink.frames {
		if frame.Text != wantText[i] {
			t.Fatalf("structured frame %d text = %q, want %q", i, frame.Text, wantText[i])
		}
	}
	callEvent := sink.frames[0].Event
	if callEvent == nil || callEvent.Type != session.EventTypeToolCall || callEvent.Protocol == nil || callEvent.Protocol.ToolCall == nil {
		t.Fatalf("tool call event = %#v", callEvent)
	}
	if callEvent.Protocol.ToolCall.Name != "execute" || callEvent.Protocol.ToolCall.Title != "run go test" || callEvent.Protocol.ToolCall.Kind != "execute" || callEvent.Protocol.ToolCall.RawInput["command"] != "go test ./surfaces/tui/app/..." {
		t.Fatalf("tool call payload = %#v", callEvent.Protocol.ToolCall)
	}
	resultEvent := sink.frames[1].Event
	if resultEvent == nil || resultEvent.Type != session.EventTypeToolResult || resultEvent.Protocol == nil || resultEvent.Protocol.ToolCall == nil {
		t.Fatalf("tool result event = %#v", resultEvent)
	}
	if resultEvent.Protocol.ToolCall.RawOutput["stdout"] != "ok\n" {
		t.Fatalf("tool result payload = %#v", resultEvent.Protocol.ToolCall)
	}
	planEvent := sink.frames[2].Event
	if planEvent == nil || planEvent.Type != session.EventTypePlan || planEvent.Protocol == nil || planEvent.Protocol.Plan == nil {
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
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "codex", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
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
	if frame.Text != "Searching for: weather: Shanghai, China\n" {
		t.Fatalf("stream frame text = %q, want readable search trace", frame.Text)
	}
	event := frame.Event
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		t.Fatalf("stream event = %#v, want structured tool call", event)
	}
	if got := event.Protocol.ToolCall.Name; got != "fetch" {
		t.Fatalf("tool name = %q, want ACP kind", got)
	}
	if got := event.Protocol.ToolCall.Title; got != "Searching for: weather: Shanghai, China" {
		t.Fatalf("tool title = %q, want ACP title", got)
	}
	if got := event.Protocol.ToolCall.Kind; got != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", got)
	}
	if got := event.Protocol.ToolCall.RawInput["query"]; got != "weather: Shanghai, China" {
		t.Fatalf("raw input query = %#v", got)
	}
}

func TestRunnerPublishesChildTerminalOutputMetaAsStreamText(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "claude", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "bash-1",
			Kind:          stringPtr("execute"),
			Title:         stringPtr("run date loop"),
			Meta: map[string]any{
				"terminal_output": map[string]any{
					"terminal_id": "bash-1",
					"data":        "17:21:17\n",
				},
			},
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one terminal output frame", sink.frames)
	}
	if got := sink.frames[0].Text; got != "17:21:17\n" {
		t.Fatalf("stream frame text = %q, want terminal output data", got)
	}
	event := sink.frames[0].Event
	if event == nil || event.Protocol == nil || event.Protocol.Update == nil || event.Protocol.Update.Meta["terminal_output"] == nil {
		t.Fatalf("stream event = %#v, want structured terminal meta preserved", event)
	}
}

func TestRunnerHandleUpdateUsesAgentMessageDeltas(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateUserMessage, "ignored prompt"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "我来按步骤"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "我来按步骤执行"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "我来按步骤执行这个任务。"))

	if got := len(sink.frames); got != 3 {
		t.Fatalf("stream frames = %#v, want three agent delta updates", sink.frames)
	}
	var rendered string
	var renderedEvents string
	for _, frame := range sink.frames {
		rendered += frame.Text
		if frame.Event == nil {
			t.Fatalf("stream frame event = nil, want structured delta event")
		}
		renderedEvents += frame.Event.Text
	}
	if rendered != "我来按步骤执行这个任务。" {
		t.Fatalf("rendered stream = %q, want deduped final text", rendered)
	}
	if renderedEvents != "我来按步骤执行这个任务。" {
		t.Fatalf("rendered event stream = %q, want deduped final text", renderedEvents)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "我来按步骤执行这个任务。" {
		t.Fatalf("run.result = %q, want deduped final text", result)
	}
}

func TestRunnerResultKeepsOnlyLatestAssistantSegmentAfterTools(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "claude", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "我先读取文件。"))
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "read-1",
			Kind:          "read",
			Title:         "Read hello_spawn.txt",
			Status:        "in_progress",
			RawInput:      map[string]any{"path": "hello_spawn.txt"},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "read-1",
			Kind:          stringPtr("read"),
			Title:         stringPtr("Read hello_spawn.txt"),
			Status:        stringPtr("completed"),
		},
	})
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "总结一下执行结果：\n步骤 操作 结果"))

	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "总结一下执行结果：\n步骤 操作 结果" {
		t.Fatalf("run.result = %q, want latest assistant segment only", result)
	}
	var streamed string
	for _, frame := range sink.frames {
		streamed += frame.Text
	}
	for _, want := range []string{"我先读取文件。", "Read hello_spawn.txt", "总结一下执行结果"} {
		if !strings.Contains(streamed, want) {
			t.Fatalf("streamed text = %q, want %q preserved in running trace", streamed, want)
		}
	}
}

func TestRunnerAgentMessageDeltaMergeDoesNotUseOverlapHeuristic(t *testing.T) {
	t.Parallel()

	run := &childRun{}
	if got := run.appendAgentMessageLocked("abcabc"); got != "abcabc" {
		t.Fatalf("first delta = %q, want abcabc", got)
	}
	if got := run.appendAgentMessageLocked("abcXYZ"); got != "abcXYZ" {
		t.Fatalf("overlapping delta = %q, want full incoming chunk", got)
	}
	if run.result != "abcabcabcXYZ" {
		t.Fatalf("run.result = %q, want exact appended chunks", run.result)
	}
}

func TestRunnerAgentMessageDeltaMergePreservesMixedLanguageChunks(t *testing.T) {
	t.Parallel()

	chunks := []string{
		"Let me quickly inspect these files to understand the project.",
		"\n\nNow I can summarize: ",
		"这是一个包含 calc 和 hello 模块的 Python 项目，覆盖主模块功能和边界测试。",
	}
	run := &childRun{}
	var rendered string
	for _, chunk := range chunks {
		rendered += run.appendAgentMessageLocked(chunk)
	}
	want := strings.Join(chunks, "")
	if rendered != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
	if run.result != want {
		t.Fatalf("run.result = %q, want %q", run.result, want)
	}
}

func TestRunnerHandleUpdatePublishesStructuredThoughtEvent(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, "thinking about the command"))

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one thought frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Text != "" {
		t.Fatalf("stream frame text = %q, want structured thought event only", got.Text)
	}
	if got.Event == nil || got.Event.Protocol == nil || got.Event.Protocol.UpdateType != client.UpdateAgentThought || got.Event.Text != "thinking about the command" {
		t.Fatalf("stream event = %#v, want structured thought event", got.Event)
	}
}

func TestRunnerHandleUpdatePreservesWhitespaceThoughtChunk(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, " "))

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one whitespace thought frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Text != "" {
		t.Fatalf("stream frame text = %q, want structured thought event only", got.Text)
	}
	if got.Event == nil || got.Event.Text != " " {
		t.Fatalf("stream event = %#v, want single-space structured thought event", got.Event)
	}
}

func TestRunnerHandleUpdateDoesNotHoldRunLockWhilePublishing(t *testing.T) {
	t.Parallel()

	sink := &blockingStreams{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	done := make(chan struct{})
	go func() {
		runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "blocked output"))
		close(done)
	}()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("PublishStream was not called")
	}

	locked := make(chan struct{})
	go func() {
		run.mu.Lock()
		run.outputPreview = "lock was available"
		run.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("run lock stayed held while PublishStream was blocked")
	}

	close(sink.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleUpdate did not return after releasing PublishStream")
	}
}

func TestRunnerHandleUpdateAcceptsStringContentChunks(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "copilot-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	raw, err := json.Marshal("string chunk")
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ContentChunk{
			SessionUpdate: client.UpdateAgentMessage,
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

func contentUpdate(t *testing.T, kind string, text string) client.UpdateEnvelope {
	t.Helper()
	raw, err := json.Marshal(client.TextChunk{Type: "text", Text: text})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ContentChunk{
			SessionUpdate: kind,
			Content:       raw,
		},
	}
}

func stringPtr(value string) *string {
	return &value
}

type recordingStreams struct {
	frames []stream.Frame
}

func (s *recordingStreams) PublishStream(frame stream.Frame) {
	s.frames = append(s.frames, stream.CloneFrame(frame))
}

type blockingStreams struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingStreams) PublishStream(stream.Frame) {
	close(s.entered)
	<-s.release
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
