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
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const structuredFinalMessageForFidelityTest = "# 完成\n\n已创建文件。\n\n---\n\n### 结果\n\n- 第一项\n- 第二项\n\n| 文件 | 状态 |\n| --- | --- |\n| `hello.go` | 好 |\n\n```go\nfmt.Println(\"你好\")\n```\n\n创建文件\n\n> **结果**"

func TestDurableTaskWaitFinalCompletesOriginalSpawnPanel(t *testing.T) {
	t.Parallel()

	model := applyCanonicalOutputFidelitySequence(t, acpprojector.SessionEventTransport{})
	block := requireMainACPTurnBlockForTest(t, model)
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 {
		t.Fatalf("events = %#v, want one physical Spawn panel and no TASK result panel", block.Events)
	}
	spawn := physical[0]
	if !spawn.Done || spawn.Err || !strings.EqualFold(toolSemanticName(spawn.Name, spawn.ToolKind), "SPAWN") {
		t.Fatalf("Spawn = %#v, want completed original Spawn call", spawn)
	}
	if spawn.Output != structuredFinalMessageForFidelityTest {
		t.Fatalf("Spawn output = %q, want exact canonical Final Message %q", spawn.Output, structuredFinalMessageForFidelityTest)
	}

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	wantPreview := []string{
		"• Spawned reviewer: inspect",
		"  └ # 完成",
		"    已创建文件。",
		"    ... +10 lines",
		"    创建文件",
		"    > **结果**",
	}
	if !reflect.DeepEqual(model.viewportPlainLines, wantPreview) {
		t.Fatalf("rendered Spawn preview rows = %#v\nwant bounded preview rows = %#v", model.viewportPlainLines, wantPreview)
	}
	if strings.Contains(plain, "hello.go") || strings.Contains(plain, "fmt.Println") {
		t.Fatalf("bounded Spawn preview leaked full child output into the main transcript:\n%s", plain)
	}
	if !block.toggleToolPanelClick("spawn-call-1") {
		t.Fatal("completed Spawn preview did not expose its canonical full output")
	}
	model.markViewportBlockDirty(block.BlockID())
	model.syncViewportContent()
	plain = strings.Join(model.viewportPlainLines, "\n")
	if len(model.viewportPlainLines) <= len(wantPreview) {
		t.Fatalf("expanded Spawn output stayed compact: %#v", model.viewportPlainLines)
	}
	for index, row := range model.viewportPlainLines[1:] {
		if index == 0 {
			if !strings.HasPrefix(row, "  └ ") {
				t.Fatalf("first expanded Spawn row escaped its panel: %q", row)
			}
			continue
		}
		if strings.TrimSpace(row) == "" {
			continue
		}
		if !strings.HasPrefix(row, "    ") {
			t.Fatalf("expanded Spawn row %d escaped its panel: %q", index+2, row)
		}
	}
	for _, want := range []string{"完成", "第一项", "第二项", "hello.go", "fmt.Println", "创建文件", "结果"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expanded Final Message missing %q:\n%s", want, plain)
		}
	}
	for _, forbidden := range []string{"(wait subagent output)", "---###", "创建文件>"} {
		if strings.Contains(plain, forbidden) {
			t.Fatalf("rendered Final Message contains glued/placeholder text %q:\n%s", forbidden, plain)
		}
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
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(300, 0),
			Scope:      &session.EventScope{TurnID: "turn-1"},
			Meta:       acpToolNameMeta("SPAWN"),
			Tool: &session.EventTool{
				ID:     "spawn-call-1",
				Name:   "SPAWN",
				Kind:   "execute",
				Title:  "SPAWN reviewer: inspect",
				Status: "running",
				Input:  map[string]any{"agent": "reviewer", "prompt": "inspect"},
			},
		},
		{
			ID:         "task-wait-final-1",
			SessionID:  "session-1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(301, 0),
			Scope:      &session.EventScope{TurnID: "turn-2"},
			Meta:       acpToolNameMeta("TASK"),
			Tool: &session.EventTool{
				ID:     "task-wait-call-1",
				Name:   "TASK",
				Kind:   "execute",
				Title:  "TASK wait reviewer",
				Status: "completed",
				Input:  map[string]any{"action": "wait", "task_id": "reviewer"},
				Output: map[string]any{
					"task_id":       "reviewer",
					"state":         "completed",
					"target_kind":   "subagent",
					"parent_call":   "spawn-call-1",
					"parent_tool":   "SPAWN",
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
	block.UpdateToolWithMeta("hidden-tool-1", "READ", "file.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
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
			Title: "SPAWN explorer: inspect", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "explorer", "prompt": "inspect"}, Meta: acpToolNameMeta("SPAWN"),
		},
	})
	child := func(update schema.Update) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "SPAWN"}, Update: update,
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

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Output != "任务 3 完成。\n---\n\n### 任务 4：创建文件" {
		t.Fatalf("Spawn events = %#v, want a stable Markdown boundary around hidden child tool", block.Events)
	}
}

func TestTerminalGapIsRenderedOnceWithoutChangingExactBytes(t *testing.T) {
	t.Parallel()

	const retained = "retained 1\nretained 2\nretained 3\nretained 4\nretained 5\nretained 6\nretained tail\n"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1", Title: "RUN_COMMAND long job",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "long job"}, Content: []schema.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
			Meta: acpToolNameMeta("RUN_COMMAND"),
		},
	})
	runningMeta := runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", retained, "append")
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
	finalMeta := metautil.WithTerminalInfo(acpToolNameMeta("RUN_COMMAND"), "terminal-1")
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
