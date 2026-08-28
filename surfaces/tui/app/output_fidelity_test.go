package tuiapp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const structuredFinalMessageForFidelityTest = "# 完成\n\n已创建文件。\n\n---\n\n### 结果\n\n- 第一项\n- 第二项\n\n| 文件 | 状态 |\n| --- | --- |\n| `hello.go` | 好 |\n\n```go\nfmt.Println(\"你好\")\n```\n\n创建文件\n\n> **结果**"

func TestDurableTaskWaitFinalCompletesOriginalSpawnRowWithoutInjectingChildWorkspace(t *testing.T) {
	t.Parallel()

	model := applyCanonicalOutputFidelitySequence(t, acpprojector.SessionEventTransport{})
	block := requireMainACPTurnBlockForTest(t, model)
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 {
		t.Fatalf("events = %#v, want one physical Spawn entry and no Task result row", block.Events)
	}
	spawn := physical[0]
	if !spawn.Done || spawn.Err || spawn.Name != surfaceToolSpawn {
		t.Fatalf("Spawn = %#v, want completed original Spawn call", spawn)
	}
	if spawn.Output != structuredFinalMessageForFidelityTest {
		t.Fatalf("Spawn output = %q, want exact canonical Final Message %q", spawn.Output, structuredFinalMessageForFidelityTest)
	}

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	wantMain := []string{"• Spawned reviewer: inspect"}
	if !reflect.DeepEqual(model.viewportPlainLines, wantMain) {
		t.Fatalf("rendered Spawn rows = %#v\nwant compact overlay entry = %#v", model.viewportPlainLines, wantMain)
	}
	if strings.Contains(plain, "hello.go") || strings.Contains(plain, "fmt.Println") {
		t.Fatalf("compact Spawn row leaked full child output into the main transcript:\n%s", plain)
	}
	model.width = 100
	model.height = 36
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-call-1") {
		t.Fatal("completed Spawn row did not open its canonical output overlay")
	}
	plain = subagentOutputOverlayPlain(model)
	if strings.Contains(plain, "hello.go") || strings.Contains(plain, "fmt.Println") {
		t.Fatalf("parent Task result was injected into child workspace order:\n%s", plain)
	}
}

func TestCanonicalTaskSequenceRendersIdenticallyLiveAndReplay(t *testing.T) {
	t.Parallel()

	replay := applyCanonicalOutputFidelityReplay(t)
	live := applyCanonicalOutputFidelitySequence(t, acpprojector.SessionEventTransport{
		HandleID: "handle-1",
		RunID:    "run-1",
	})
	replayBlock := requireMainACPTurnBlockForTest(t, replay)
	liveBlock := requireMainACPTurnBlockForTest(t, live)
	if !reflect.DeepEqual(liveBlock.Events, replayBlock.Events) {
		t.Fatalf("live events = %#v\nreplay events = %#v", liveBlock.Events, replayBlock.Events)
	}
	live.syncViewportContent()
	replay.syncViewportContent()
	if !reflect.DeepEqual(live.viewportPlainLines, replay.viewportPlainLines) {
		t.Fatalf("live output = %#v\nreplay output = %#v", live.viewportPlainLines, replay.viewportPlainLines)
	}
}

func applyCanonicalOutputFidelityReplay(t *testing.T) *Model {
	t.Helper()

	backfill := make(chan eventstream.Envelope, len(canonicalOutputFidelityEvents()))
	for _, event := range canonicalOutputFidelityEvents() {
		event = roundTripCanonicalOutputFidelityEvent(t, event)
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{},
		)
		envelopes := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(envelopes) != 1 {
			t.Fatalf("projection for %s = %#v, want one envelope", event.ID, envelopes)
		}
		backfill <- envelopes[0]
	}
	close(backfill)

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	reconnect := &tuiReconnect{backfill: backfill}
	err := streamReconnectBackfill(context.Background(), reconnect, func(message tea.Msg) {
		next, _ := model.Update(message)
		model = next.(*Model)
	})
	if err != nil {
		t.Fatalf("replay canonical output fidelity sequence: %v", err)
	}
	return model
}

func applyCanonicalOutputFidelitySequence(t *testing.T, transport acpprojector.SessionEventTransport) *Model {
	t.Helper()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	for _, event := range canonicalOutputFidelityEvents() {
		if transport.HandleID == "" && transport.RunID == "" {
			event = roundTripCanonicalOutputFidelityEvent(t, event)
		}
		base := acpprojector.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, transport)
		envelopes := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(envelopes) != 1 {
			t.Fatalf("projection for %s = %#v, want one envelope", event.ID, envelopes)
		}
		model = applyACPEnvelopeForTest(t, model, envelopes[0])
	}
	return model
}

func roundTripCanonicalOutputFidelityEvent(t *testing.T, event *session.Event) *session.Event {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal canonical event %s: %v", event.ID, err)
	}
	var rebuilt session.Event
	if err := json.Unmarshal(raw, &rebuilt); err != nil {
		t.Fatalf("unmarshal canonical event %s: %v", event.ID, err)
	}
	return &rebuilt
}

func canonicalOutputFidelityEvents() []*session.Event {
	return []*session.Event{
		{
			ID:         "spawn-start-1",
			SessionID:  "session-1",
			Seq:        1,
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(300, 0),
			Scope:      &session.EventScope{TurnID: "turn-1"},
			Meta:       acpToolNameMeta("Spawn"),
			Tool: &session.EventTool{
				ID:     "spawn-call-1",
				Name:   "Spawn",
				Kind:   "execute",
				Title:  "Spawn reviewer: inspect",
				Status: "running",
				Input:  map[string]any{"agent": "reviewer", "prompt": "inspect"},
			},
		},
		{
			ID:         "task-wait-final-1",
			SessionID:  "session-1",
			Seq:        2,
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(301, 0),
			Scope:      &session.EventScope{TurnID: "turn-2"},
			Meta:       acpToolNameMeta("Task"),
			Tool: &session.EventTool{
				ID:     "task-wait-call-1",
				Name:   "Task",
				Kind:   "execute",
				Title:  "Task wait reviewer",
				Status: "completed",
				Input:  map[string]any{"action": "wait", "task_id": "reviewer"},
				Output: map[string]any{
					"task_id":       "reviewer",
					"state":         "completed",
					"target_kind":   "subagent",
					"parent_call":   "spawn-call-1",
					"parent_tool":   "Spawn",
					"final_message": structuredFinalMessageForFidelityTest,
				},
			},
		},
	}
}

func TestNarrativePreservesRepeatedLongLinesParagraphsAndUnicodeMarkers(t *testing.T) {
	t.Parallel()

	long := "这是一行完全合法而且会有意重复的长文本内容。"
	paragraph := "这是一个完全相同且超过十六个字符的合法段落内容。"
	raw := long + "\n" + long + "\n\n" + paragraph + "\n\n" + paragraph
	if got := normalizeNarrativeLineEndings(raw); got != raw {
		t.Fatalf("narrative = %q, want repeated lines and paragraphs preserved %q", got, raw)
	}

	markers := "\uFEFF开头\uFFFD中间"
	if got := appendDeltaStreamChunk("", markers); got != markers {
		t.Fatalf("first chunk = %q, want U+FEFF/U+FFFD preserved", got)
	}
	if got := appendDeltaStreamChunk("已有", markers); got != "已有"+markers {
		t.Fatalf("later chunk = %q, want U+FEFF/U+FFFD preserved", got)
	}
	if got := sanitizeRenderableText(markers); got != markers {
		t.Fatalf("renderable text = %q, want U+FEFF/U+FFFD preserved", got)
	}

	block := NewParticipantTurnBlock("participant-1", "@reviewer")
	block.AppendStreamEvent(SEAssistant, paragraph, narrativeTestSource())
	block.UpdateToolWithMeta("hidden-tool-1", "Read", "file.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.ReplaceFinalStreamEvent(
		SEAssistant,
		paragraph,
		newNarrativeSourceIdentity("test-message-2", "test-event-2", "test-projection-2"),
	)
	if len(block.Events) != 3 || block.Events[0].Text != paragraph || block.Events[2].Text != paragraph {
		t.Fatalf("events across tool barrier = %#v, want both identical legal paragraphs preserved", block.Events)
	}
}

func TestHiddenChildToolWithoutMessageIDCreatesMarkdownBoundary(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-call-1",
			Title: "Spawn explorer: inspect", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "explorer", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	child := func(update schema.Update) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"}, Update: update,
		}
	}
	model = applyACPEnvelopeForTest(t, model, child(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage, Content: schema.TextContent{Type: "text", Text: "任务 3 完成。\n---"},
	}))
	model = applyACPEnvelopeForTest(t, model, child(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall, ToolCallID: "child-tool-1", Title: "Write", Kind: schema.ToolKindEdit, Status: schema.ToolStatusInProgress,
	}))
	model = applyACPEnvelopeForTest(t, model, child(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage, Content: schema.TextContent{Type: "text", Text: "### 任务 4：创建文件"},
	}))

	view := requireSubagentOutputViewForTest(t, model, "spawn-call-1")
	physical := physicalTranscriptEventsForTest(view.block.Events)
	if len(physical) != 3 ||
		physical[0].Kind != SEAssistant || physical[0].Text != "任务 3 完成。\n---" ||
		physical[1].Kind != SEToolCall ||
		physical[2].Kind != SEAssistant || physical[2].Text != "### 任务 4：创建文件" {
		t.Fatalf("detached child events = %#v, want a structured tool boundary around Markdown messages", physical)
	}
}

func TestTerminalGapIsRenderedOnceWithoutChangingExactBytes(t *testing.T) {
	t.Parallel()

	const retained = "retained 1\nretained 2\nretained 3\nretained 4\nretained 5\nretained 6\nretained tail\n"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1", Title: "RunCommand long job",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "long job"}, Content: []schema.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
			Meta: acpToolNameMeta("RunCommand"),
		},
	})
	runningMeta := runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", retained, "append")
	runningMeta = metautil.WithRuntimeSection(runningMeta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode:      "append",
		metautil.RuntimeStreamTruncated: true,
		metautil.RuntimeStreamBefore:    int64(65539),
	})
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &running, Meta: runningMeta,
		},
	})

	completed := schema.ToolStatusCompleted
	finalMeta := metautil.WithTerminalInfo(acpToolNameMeta("RunCommand"), "terminal-1")
	final := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &completed, Meta: finalMeta,
		},
	}
	model = applyACPEnvelopeForTest(t, model, final)
	model = applyACPEnvelopeForTest(t, model, final)

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one terminal panel", block.Events)
	}
	terminal := block.Events[0]
	if terminal.Output != retained || strings.Contains(terminal.Output, terminalOutputGapNotice) {
		t.Fatalf("exact terminal bytes = %q, want retained bytes without synthetic gap", terminal.Output)
	}
	if !terminal.OutputGapBefore || !terminal.Done || strings.Contains(terminal.Output, "(no output)") {
		t.Fatalf("terminal event = %#v, want one remembered gap and streamed output after duplicate empty final", terminal)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if count := strings.Count(plain, terminalOutputGapNotice); count != 1 {
		t.Fatalf("gap count = %d, want exactly one render-only notice:\n%s", count, plain)
	}
	if !strings.Contains(plain, "retained tail") || strings.Contains(plain, "(no output)") {
		t.Fatalf("rendered terminal output lost retained bytes or regressed to placeholder:\n%s", plain)
	}
}
