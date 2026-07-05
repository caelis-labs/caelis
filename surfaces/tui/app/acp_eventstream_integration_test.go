package tuiapp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func TestHandleACPEventEnvelopeAppliesToolTerminalSequence(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "echo ok",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"command": "echo ok"},
			Content:       []schema.ToolCallContent{{Type: "terminal", TerminalID: "call-1"}},
			Meta:          metautil.WithTerminalInfo(nil, "call-1"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("echo ok"),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(schema.ToolStatusCompleted),
			RawInput:      map[string]any{"command": "echo ok"},
			Content:       []schema.ToolCallContent{{Type: "terminal", TerminalID: "call-1"}},
			Meta:          metautil.WithTerminalOutput(nil, "call-1", "ok\n"),
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one tool event", block.Events)
	}
	event := block.Events[0]
	if event.Kind != SEToolCall || event.CallID != "call-1" || !event.Terminal || !event.Done {
		t.Fatalf("tool event = %#v, want completed ACP terminal call", event)
	}
	if !strings.Contains(event.Output, "ok") {
		t.Fatalf("tool output = %q, want terminal output", event.Output)
	}
	model.syncViewportContent()
	if strings.Contains(strings.Join(model.viewportPlainLines, "\n"), "• Tool") {
		t.Fatalf("viewport rendered generic Tool header: %#v", model.viewportPlainLines)
	}
}

func TestHandleACPEventEnvelopeMergesGrokGlobUpdate(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "glob-1",
			Title:         "Glob",
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "glob-1",
			Title:         stringPtr("Glob `**/*.py`"),
			Kind:          stringPtr(schema.ToolKindSearch),
			RawInput:      map[string]any{"variant": "CursorGlob", "glob_pattern": "**/*.py"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one Grok glob event", block.Events)
	}
	event := block.Events[0]
	if event.Name != schema.ToolKindSearch || event.ToolKind != schema.ToolKindSearch || event.Args != "`**/*.py`" {
		t.Fatalf("glob event = %#v, want merged search update with pattern args", event)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "Search `**/*.py`") || strings.Contains(plain, "Glob Glob") {
		t.Fatalf("viewport rendered unexpected glob header:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeMergesGrokShellUpdate(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "shell-1",
			Title:         "Shell",
			RawInput: map[string]any{
				"command":     "pwd",
				"description": "Print current working directory",
			},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "shell-1",
			Title:         stringPtr("Execute `pwd`"),
			Kind:          stringPtr(schema.ToolKindExecute),
			RawInput: map[string]any{
				"variant":     "CursorShell",
				"command":     "pwd",
				"description": "Print current working directory",
			},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one Grok shell event", block.Events)
	}
	event := block.Events[0]
	if event.Name != schema.ToolKindExecute || event.Args != "pwd" || event.ToolKind != schema.ToolKindExecute {
		t.Fatalf("shell event = %#v, want merged execute kind with command args", event)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "Ran pwd") || strings.Contains(plain, "Ran Shell") {
		t.Fatalf("viewport rendered unexpected shell header:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeMergesPartialToolUpdateFromCaelisOutput(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RUN_COMMAND pwd",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "pwd"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        stringPtr(schema.ToolStatusCompleted),
			RawOutput:     map[string]any{"stdout": "/tmp/work\n", "exit_code": 0},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one merged tool event", block.Events)
	}
	event := block.Events[0]
	if event.Name != schema.ToolKindExecute || event.ToolKind != schema.ToolKindExecute || event.Args != "pwd" || !event.Done {
		t.Fatalf("tool event = %#v, want partial update to preserve existing identity and complete", event)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "Ran pwd") || strings.Contains(plain, "Ran Tool") {
		t.Fatalf("viewport rendered unexpected partial update header:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeDoesNotDuplicateRunningSnapshotAfterTerminalStream(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	kind := schema.ToolKindExecute
	status := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RUN_COMMAND echo ok",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "echo ok"},
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "", ""),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RUN_COMMAND echo ok"),
			Kind:          &kind,
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "Step 1/5\nStep 2/5\n", "append"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RUN_COMMAND echo ok"),
			Kind:          &kind,
			Status:        &status,
			RawInput:      map[string]any{"command": "echo ok"},
			RawOutput:     map[string]any{"latest_output": "Step 1/5\nStep 2/5\n", "state": "running"},
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "Step 1/5\nStep 2/5\n", ""),
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RUN_COMMAND event", block.Events)
	}
	if got, want := block.Events[0].Output, "Step 1/5\nStep 2/5\n"; got != want {
		t.Fatalf("tool output = %q, want live stream output once %q", got, want)
	}
}

func TestHandleACPEventEnvelopePreservesSplitTerminalNewlineFrame(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RUN_COMMAND for i in 1 2",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "for i in 1 2; do echo Step $i/2; done"},
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "", ""),
		},
	})
	for _, text := range []string{"Step 1/2", "\n", "Step 2/2\n"} {
		model = applyACPEnvelopeForTest(t, model, terminalMetaStreamEnvelope("call-1", text))
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RUN_COMMAND event", block.Events)
	}
	if got, want := block.Events[0].Output, "Step 1/2\nStep 2/2\n"; got != want {
		t.Fatalf("tool output = %q, want %q", got, want)
	}
}

func TestHandleACPEventEnvelopePreservesStandardTerminalPatchNewlines(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RUN_COMMAND for i in 1 2",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "for i in 1 2; do echo Step $i/2; done"},
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "", ""),
		},
	})
	for _, text := range []string{"[1/2] 14:57:57\n", "[2/2] 14:57:58\n"} {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "call-1", text),
			},
		})
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RUN_COMMAND event", block.Events)
	}
	const want = "[1/2] 14:57:57\n[2/2] 14:57:58\n"
	if got := block.Events[0].Output; got != want {
		t.Fatalf("tool output = %q, want %q", got, want)
	}
}

func TestHandleACPEventEnvelopeAppliesParticipantSequence(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindParticipant,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		ParticipantID: "agent-1",
		Actor:         "@agent",
		Participant:   &eventstream.Participant{State: "attached"},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		ParticipantID: "agent-1",
		Actor:         "@agent",
		Final:         true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "participant answer"},
		},
	})

	block := model.findParticipantTurnBlock("participant-turn-1")
	if block == nil {
		t.Fatal("participant block missing")
	}
	if block.Actor != "@agent" || block.Status != "completed" {
		t.Fatalf("participant block = %#v, want @agent completed", block)
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "participant answer" {
		t.Fatalf("participant events = %#v, want assistant answer", block.Events)
	}
}

func TestHandleACPEventEnvelopeDisplaysParticipantSkillContentReadAsSkill(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "side-reviewer",
		ParticipantID: "side-reviewer",
		Actor:         "@reviewer",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "skill-review-1",
			Title:         `Read <skill_content name="review">`,
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusInProgress,
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "side-reviewer",
		ParticipantID: "side-reviewer",
		Actor:         "@reviewer",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "skill-review-1",
			Title:         stringPtr(`Read <skill_content name="review">`),
			Kind:          stringPtr(schema.ToolKindRead),
			Status:        stringPtr(schema.ToolStatusCompleted),
		},
	})

	block := model.findParticipantTurnBlock("side-reviewer")
	if block == nil {
		t.Fatal("participant block missing")
	}
	if len(block.Events) != 1 || block.Events[0].Name != "SKILL" || block.Events[0].Args != "review" || !block.Events[0].Done {
		t.Fatalf("participant events = %#v, want completed Skill review", block.Events)
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "Skill review") || strings.Contains(plain, "<skill_content") {
		t.Fatalf("rendered participant skill rows:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeAnchorsSubagentOutputToSpawnTool(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	spawnMeta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "SPAWN reviewer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"agent": "reviewer", "prompt": "inspect"},
			Meta:          spawnMeta,
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		Actor:     "reviewer",
		Final:     true,
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeStream, map[string]any{
			metautil.RuntimeStreamParentCallID: "spawn-1",
			metautil.RuntimeStreamParentTool:   "SPAWN",
		}),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "subagent found the issue"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want anchored spawn event", block.Events)
	}
	event := block.Events[0]
	if event.Kind != SEToolCall || event.CallID != "spawn-1" || event.Name != "SPAWN" {
		t.Fatalf("spawn event = %#v, want SPAWN tool call", event)
	}
	if event.TaskID != "task-1" || !strings.Contains(event.Output, "subagent found the issue") {
		t.Fatalf("spawn event = %#v, want anchored subagent output", event)
	}
}

func TestHandleACPEventEnvelopeSuppressesSubagentEventsMirroredToParentPanel(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "task-call-1",
			Title:         "TASK wait akio",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"action": "wait", "task_id": "akio"},
			Meta:          acpToolNameMeta("TASK"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "task-call-1",
			Meta:          metautil.WithTerminalOutput(acpToolNameMeta("TASK"), "task-call-1", "child stream summary\n"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		Meta:      parentToolStreamMeta("task-call-1", "TASK", true),
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "child-read-1",
			Title:         "READ README.md",
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusCompleted,
			RawInput:      map[string]any{"path": "README.md"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		Final:     true,
		Meta:      parentToolStreamMeta("task-call-1", "TASK", true),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "child final answer"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want only parent TASK event", block.Events)
	}
	if event := block.Events[0]; event.CallID != "task-call-1" || !strings.Contains(event.Output, "child stream summary") {
		t.Fatalf("task event = %#v, want parent TASK output only", event)
	}
	if participant := model.findParticipantTurnBlock("akio"); participant != nil {
		t.Fatalf("subagent participant block = %#v, want mirrored TASK events suppressed", participant)
	}
	for _, docBlock := range model.doc.Blocks() {
		if participant, ok := docBlock.(*ParticipantTurnBlock); ok {
			t.Fatalf("unexpected participant block = %#v", participant)
		}
	}
}

func TestHandleACPEventEnvelopeRoutesAnchoredSubagentOutputToParentPanel(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "task-call-1",
			Title:         "TASK write akio",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"action": "write", "task_id": "akio"},
			Meta:          acpToolNameMeta("TASK"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		Final:     true,
		Meta:      parentToolStreamMeta("task-call-1", "TASK", false),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "child write response"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want only parent panel event", block.Events)
	}
	if event := block.Events[0]; event.CallID != "task-call-1" || !strings.Contains(event.Output, "child write response") {
		t.Fatalf("task event = %#v, want anchored subagent output in parent panel", event)
	}
	if participant := model.findParticipantTurnBlock("akio"); participant != nil {
		t.Fatalf("subagent participant block = %#v, want anchored output kept in parent panel", participant)
	}
}

func TestHandleACPEventEnvelopeReplacesSpawnStreamWithFinalRuntimeResult(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	spawnMeta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	spawnMeta = metautil.WithRuntimeSection(spawnMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-1",
		metautil.RuntimeTaskTerminalID: "subagent-task-1",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "SPAWN reviewer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"agent": "reviewer", "prompt": "inspect"},
			Meta:          spawnMeta,
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Meta:          metautil.WithTerminalOutput(spawnMeta, "spawn-1", "LIST /tmp completed"),
		},
	})
	completed := schema.ToolStatusCompleted
	finalMeta := metautil.WithRuntimeSection(spawnMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-1",
		metautil.RuntimeTaskTerminalID: "subagent-task-1",
		"running":                      false,
		"state":                        "completed",
		"result":                       "Final child result",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Status:        &completed,
			Meta:          metautil.WithTerminalOutput(finalMeta, "spawn-1", "Final child result"),
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one SPAWN tool event", block.Events)
	}
	event := block.Events[0]
	if !event.Done || event.Output != "Final child result" {
		t.Fatalf("spawn event = %#v, want final result to replace streamed output", event)
	}
}

func TestHandleACPEventEnvelopePreservesSpawnTerminalPatchNewlines(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "SPAWN reviewer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "reviewer", "prompt": "inspect"},
			Meta:          runningSnapshotTerminalMeta("SPAWN", "task-1", "terminal-1", "", ""),
		},
	})
	for _, text := range []string{"LIST /tmp completed\n", "READ /tmp/file completed\n"} {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "spawn-1",
				Meta:          metautil.WithTerminalOutput(acpToolNameMeta("SPAWN"), "spawn-1", text),
			},
		})
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one SPAWN event", block.Events)
	}
	const want = "LIST /tmp completed\nREAD /tmp/file completed\n"
	if got := block.Events[0].Output; got != want {
		t.Fatalf("spawn output = %q, want %q", got, want)
	}
}

func TestHandleACPEventEnvelopeAppliesApprovalReview(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindApprovalReview,
		SessionID: "session-1",
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID: "call-1",
			ToolName:   "RUN_COMMAND",
			RawInput:   map[string]any{"command": "go test ./..."},
			Status:     "in_progress",
		},
	})
	if model.runningActivity.Kind != runningActivityApprovalReview || !strings.Contains(model.runningActivity.Detail, "go test") {
		t.Fatalf("runningActivity = %#v, want approval review command hint", model.runningActivity)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindApprovalReview,
		SessionID: "session-1",
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID:    "call-1",
			ToolName:      "RUN_COMMAND",
			RawInput:      map[string]any{"command": "go test ./..."},
			Status:        "approved",
			Risk:          "low",
			Authorization: "allow",
			Text:          "approved by policy",
		},
	})
	if model.runningActivity.Kind == runningActivityApprovalReview {
		t.Fatalf("runningActivity = %#v, want cleared after terminal review", model.runningActivity)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SEApproval || block.Events[0].ApprovalStatus != "approved" {
		t.Fatalf("main events = %#v, want approved approval review", block.Events)
	}
	if block.Events[0].ApprovalText != "approved by policy" || block.Events[0].ApprovalRisk != "low" || block.Events[0].ApprovalAuth != "allow" {
		t.Fatalf("approval event = %#v, want review fields", block.Events[0])
	}
}

func TestForwardTurnEventStreamQueuesLiveACPEnvelopes(t *testing.T) {
	t.Parallel()

	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(124, 0))
	terminal.SessionID = "session-1"
	terminal.ScopeID = "session-1"
	events := make(chan eventstream.Envelope, 2)
	events <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "live answer"},
		},
	}
	events <- terminal
	close(events)

	var sent []tea.Msg
	result := forwardTurnEventStream(context.Background(), nil, &eventstreamIntegrationTurn{events: events}, &ProgramSender{
		Send: func(msg tea.Msg) {
			sent = append(sent, msg)
		},
	})
	if !result.queued {
		t.Fatalf("forwardTurnEventStream() queued = false, want true")
	}
	if len(sent) != 2 {
		t.Fatalf("sent messages = %#v, want content + terminal lifecycle", sent)
	}
	first, ok := sent[0].(eventstream.Envelope)
	if !ok || first.Kind != eventstream.KindSessionUpdate {
		t.Fatalf("first message = %#v, want session/update envelope", sent[0])
	}
	last, ok := sent[1].(eventstream.Envelope)
	if !ok || !eventstream.IsTerminalLifecycle(last) || last.Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("last message = %#v, want completed terminal lifecycle", sent[1])
	}

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	for _, msg := range sent {
		env, ok := msg.(eventstream.Envelope)
		if !ok {
			t.Fatalf("sent message = %T, want eventstream.Envelope", msg)
		}
		model = applyACPEnvelopeForTest(t, model, env)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if block.Status != "completed" {
		t.Fatalf("main ACP turn status = %q, want completed", block.Status)
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "live answer" {
		t.Fatalf("main ACP events = %#v, want live assistant answer", block.Events)
	}
	if model.turnRunning() {
		t.Fatal("model turn still running after terminal lifecycle")
	}
}

func TestHandleACPEventEnvelopeMergesAttemptResetNoticeInMainTurn(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "partial"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "attempt_reset"},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{
						"attempt":        1,
						"cause":          "model: http status 424 body=exceeds the context window",
						"max_retries":    5,
						"retry_delay_ms": 1000,
						"retrying":       true,
					},
				},
			},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "attempt_reset"},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{
						"attempt":        2,
						"cause":          "model: http status 500 body=Internal Server Error",
						"max_retries":    5,
						"retry_delay_ms": 5000,
						"retrying":       true,
					},
				},
			},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "final"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 2 {
		t.Fatalf("main ACP events = %#v, want retry notice then final answer", block.Events)
	}
	if block.Events[0].Kind != SENotice || block.Events[0].Text != "Retrying model request (2/5, retry in 5s)" {
		t.Fatalf("first event = %#v, want merged retry notice", block.Events[0])
	}
	if block.Events[0].NoticeKind != transcript.NoticeKindModelRetry {
		t.Fatalf("first event notice kind = %q, want model retry", block.Events[0].NoticeKind)
	}
	if strings.Contains(block.Events[0].Text, "Internal Server Error") {
		t.Fatalf("retry notice leaked provider error: %q", block.Events[0].Text)
	}
	if block.Events[1].Kind != SEAssistant || block.Events[1].Text != "final" {
		t.Fatalf("second event = %#v, want final assistant", block.Events[1])
	}
}

func TestHandleACPEventEnvelopeSuppressesAdjacentDuplicateUserMessage(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	for range 2 {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			ScopeID:   "session-1",
			TurnID:    "turn-1",
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateUserMessage,
				Content:       schema.TextContent{Type: "text", Text: "继续"},
			},
		})
	}

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == "继续" {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want one gateway user echo", userBlocks)
	}
}

func TestHandleACPEventEnvelopeSuppressesDuplicateUserMessageAfterMainTurnStarts(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine("继续")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "running"},
	})
	if strings.TrimSpace(model.activeMainACPTurnID) == "" {
		t.Fatal("main ACP turn did not start before user echo")
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "继续"},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == "继续" {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want started main ACP turn to absorb gateway user echo", userBlocks)
	}
	if strings.TrimSpace(model.activeMainACPTurnID) == "" {
		t.Fatal("duplicate user echo closed the active main ACP turn")
	}
}

func TestHandleACPEventEnvelopeSuppressesDuplicateParticipantUserMessage(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "@bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want participant user echo deduped", userBlocks)
	}
}

func TestHandleACPEventEnvelopeSuppressesLateParticipantUserEchoAfterTurnStarts(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "@bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "checking weather"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want late participant user echo deduped", userBlocks)
	}
	if got := strings.TrimSpace(model.activeParticipantTurnSessionID); got != "participant-turn-1" {
		t.Fatalf("activeParticipantTurnSessionID = %q, want participant-turn-1", got)
	}
}

func TestHandleACPEventEnvelopeSuppressesLateParticipantUserEchoAfterTurnCompletes(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "@bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Final:         true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "weather result"},
		},
	})
	if got := strings.TrimSpace(model.activeParticipantTurnSessionID); got != "" {
		t.Fatalf("activeParticipantTurnSessionID = %q, want cleared after completion", got)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want completed participant turn to absorb late user echo", userBlocks)
	}
}

func TestHandleACPEventEnvelopeRendersQueuedRepeatedParticipantUserMessage(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "@bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    display,
		displayLine: display,
		dispatched:  true,
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 2 {
		t.Fatalf("userBlocks = %d, want queued repeated participant prompt rendered", userBlocks)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %#v, want queued participant prompt dequeued after echo", model.pendingQueue)
	}
}

func TestHandleACPEventEnvelopeRendersRepeatedParticipantUserMessageAcrossEmptyTurns(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "@bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	for _, turnID := range []string{"participant-turn-1", "participant-turn-2"} {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:          eventstream.KindParticipant,
			SessionID:     "session-1",
			Scope:         eventstream.ScopeParticipant,
			ScopeID:       turnID,
			TurnID:        turnID,
			ParticipantID: "grok-1",
			Actor:         "@bela",
			Participant:   &eventstream.Participant{State: "attached"},
		})
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-2",
		TurnID:        "participant-turn-2",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 2 {
		t.Fatalf("userBlocks = %d, want repeated prompt preserved across unrelated empty participant turns", userBlocks)
	}
}

func TestDequeuePendingUserMessageAnyIgnoresEmptyNeedles(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "@bela hello",
		displayLine: "@bela hello",
		dispatched:  true,
	})
	if model.dequeuePendingUserMessageAny("", "   ") {
		t.Fatal("dequeuePendingUserMessageAny(empty) = true, want false")
	}
	if len(model.pendingQueue) != 1 {
		t.Fatalf("pendingQueue = %#v, want preserved queue", model.pendingQueue)
	}
}

func TestHandleACPEventEnvelopeRendersQueuedRepeatedUserMessage(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine("继续")
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "继续",
		displayLine: "继续",
		dispatched:  true,
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "running"},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "继续"},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == "继续" {
			userBlocks++
		}
	}
	if userBlocks != 2 {
		t.Fatalf("userBlocks = %d, want queued repeated prompt rendered as a new user message", userBlocks)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %#v, want queued prompt dequeued after gateway echo", model.pendingQueue)
	}
}

func TestHandleACPEventEnvelopeSuppressesImageAttachmentUserEcho(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine("[image #1] hello")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello"},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if _, ok := block.(*UserNarrativeBlock); ok {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want local image display line to absorb gateway echo", userBlocks)
	}
}

func TestHandleACPEventEnvelopeAnchorsCompactNoticeInMainTurn(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SENotice || block.Events[0].Text != "• "+transcript.CompactNoticeLabel {
		t.Fatalf("main ACP events = %#v, want compact notice", block.Events)
	}
	if block.Events[0].NoticeKind != transcript.NoticeKindCompact {
		t.Fatalf("compact notice kind = %q, want compact", block.Events[0].NoticeKind)
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)})
	if plain := joinRenderedPlain(rows); !strings.Contains(plain, "• "+transcript.CompactNoticeLabel) {
		t.Fatalf("rendered compact notice = %q", plain)
	}
	if len(rows) < 3 {
		t.Fatalf("rendered compact notice rows = %#v, want blank, notice, blank", rows)
	}
	noticeIndex := -1
	for i, row := range rows {
		if row.Plain == "• "+transcript.CompactNoticeLabel {
			noticeIndex = i
			break
		}
	}
	if noticeIndex <= 0 || noticeIndex >= len(rows)-1 {
		t.Fatalf("compact notice index = %d rows=%#v, want surrounding blank rows", noticeIndex, rows)
	}
	if strings.TrimSpace(rows[noticeIndex-1].Plain) != "" || strings.TrimSpace(rows[noticeIndex+1].Plain) != "" {
		t.Fatalf("compact notice rows = %#v, want surrounding blank rows", rows)
	}
}

func TestRenderEventPolicyForACPEnvelopeRoutesStandardUpdates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  eventstream.Envelope
		lane renderEventLane
	}{
		{
			name: "tool",
			env: eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
				},
			},
			lane: renderLaneToolStream,
		},
		{
			name: "participant",
			env: eventstream.Envelope{
				Kind:        eventstream.KindParticipant,
				Participant: &eventstream.Participant{State: "attached"},
			},
			lane: renderLaneParticipant,
		},
		{
			name: "approval",
			env: eventstream.Envelope{
				Kind: eventstream.KindRequestPermission,
				Permission: &schema.RequestPermissionRequest{
					ToolCall: schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "call-1"},
				},
			},
			lane: renderLaneToolStream,
		},
		{
			name: "lifecycle",
			env: eventstream.Envelope{
				Kind:      eventstream.KindLifecycle,
				Lifecycle: &eventstream.Lifecycle{State: "completed"},
			},
			lane: renderLaneLifecycle,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			policy := renderEventPolicyForACPEnvelope(tt.env)
			if policy.lane != tt.lane {
				t.Fatalf("lane = %q, want %q", policy.lane, tt.lane)
			}
			if !policy.flushLogChunks {
				t.Fatalf("policy = %#v, want log flush for ACP render event", policy)
			}
		})
	}
}

func TestProjectResumeReplayEventsUsesACPEnvelopeTrace(t *testing.T) {
	t.Parallel()

	events := projectResumeReplayEvents([]eventstream.Envelope{{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RUN_COMMAND echo ok"),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(schema.ToolStatusCompleted),
			RawInput:      map[string]any{"command": "echo ok"},
			RawOutput:     map[string]any{"stdout": "ok\n"},
			Meta:          acpToolNameMeta("RUN_COMMAND"),
		},
	}, {
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		Lifecycle: &eventstream.Lifecycle{
			State: "completed",
		},
	}})
	if len(events) != 2 {
		t.Fatalf("projectResumeReplayEvents() = %#v, want tool + lifecycle", events)
	}
	if events[0].Kind != TranscriptEventTool || events[0].ToolCallID != "call-1" || events[0].ToolName != "RUN_COMMAND" {
		t.Fatalf("tool replay event = %#v, want RUN_COMMAND ACP tool event", events[0])
	}
	if events[1].Kind != TranscriptEventLifecycle || events[1].State != "completed" {
		t.Fatalf("lifecycle replay event = %#v, want completed lifecycle", events[1])
	}
}

func TestResumeReplayMainTurnBlockIsolation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		replay  []eventstream.Envelope
		prompt  string
		live    eventstream.Envelope
		oldText string
		newText string
	}{
		{
			name: "lifecycle terminated replay",
			replay: []eventstream.Envelope{
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "session-1",
					ScopeID:   "session-1",
					TurnID:    "turn-1",
					Final:     true,
					Update: schema.ContentChunk{
						SessionUpdate: schema.UpdateAgentMessage,
						Content:       schema.TextContent{Type: "text", Text: "interrupted output"},
					},
				},
				{
					Kind:      eventstream.KindLifecycle,
					SessionID: "session-1",
					ScopeID:   "session-1",
					TurnID:    "turn-1",
					Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateInterrupted},
				},
			},
			prompt: "continue after resume",
			live: eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				ScopeID:   "session-1",
				TurnID:    "turn-2",
				Final:     true,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "new answer"},
				},
			},
			oldText: "interrupted output",
			newText: "new answer",
		},
		{
			name: "final message only replay",
			replay: []eventstream.Envelope{
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "session-1",
					Scope:     eventstream.ScopeMain,
					ScopeID:   "turn-5",
					TurnID:    "turn-5",
					Final:     true,
					Update: schema.ContentChunk{
						SessionUpdate: schema.UpdateAgentMessage,
						Content:       schema.TextContent{Type: "text", Text: "old async bash summary"},
					},
				},
			},
			prompt: "new replay check prompt",
			live: eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				Scope:     eventstream.ScopeMain,
				ScopeID:   "turn-9",
				TurnID:    "turn-9",
				Final:     true,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "new replay check answer"},
				},
			},
			oldText: "old async bash summary",
			newText: "new replay check answer",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			replayEvents := projectResumeReplayEvents(tc.replay)
			next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: replayEvents})
			model = next.(*Model)
			if id := strings.TrimSpace(model.activeMainACPTurnID); id != "" {
				t.Fatalf("activeMainACPTurnID after replay = %q, want empty", id)
			}

			model.commitUserDisplayLine(tc.prompt)
			model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
			model = applyACPEnvelopeForTest(t, model, tc.live)
			assertMainTurnDocumentOrder(t, model, tc.oldText, tc.prompt, tc.newText)
		})
	}
}

func assertMainTurnDocumentOrder(t *testing.T, model *Model, oldText string, prompt string, newText string) {
	t.Helper()
	var order []string
	for _, docBlock := range model.doc.Blocks() {
		switch block := docBlock.(type) {
		case *MainACPTurnBlock:
			switch {
			case mainACPBlockContainsText(block, oldText):
				order = append(order, "old")
				if mainACPBlockContainsText(block, newText) {
					t.Fatalf("old replay block contains new turn answer: %#v", block.Events)
				}
			case mainACPBlockContainsText(block, newText):
				order = append(order, "new")
			}
		case *UserNarrativeBlock:
			if strings.TrimSpace(block.Raw) == prompt {
				order = append(order, "user")
			}
		}
	}
	want := []string{"old", "user", "new"}
	if !slices.Equal(order, want) {
		t.Fatalf("document order = %#v, want %#v", order, want)
	}
}

func TestTranscriptMainTurnKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		scope   ACPProjectionScope
		turnID  string
		scopeID string
		want    string
	}{
		{name: "main prefers turn id", scope: ACPProjectionMain, turnID: "turn-9", scopeID: "session-1", want: "turn-9"},
		{name: "main falls back to scope id", scope: ACPProjectionMain, scopeID: "session-1", want: "session-1"},
		{name: "participant keeps scope id", scope: ACPProjectionParticipant, turnID: "participant-turn-1", scopeID: "agent-session-1", want: "agent-session-1"},
		{name: "empty", scope: ACPProjectionMain},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := transcriptMainTurnKey(TranscriptEvent{
				Scope:   tc.scope,
				TurnID:  tc.turnID,
				ScopeID: tc.scopeID,
			})
			if got != tc.want {
				t.Fatalf("transcriptMainTurnKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApprovalPayloadFromACPEventUsesStandardPermission(t *testing.T) {
	t.Parallel()

	req := approvalPayloadFromACPEvent(eventstream.Envelope{
		Kind: eventstream.KindRequestPermission,
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         stringPtr("RUN_COMMAND go test ./..."),
				Kind:          stringPtr(schema.ToolKindExecute),
				RawInput: map[string]any{
					"command":             "go test ./...",
					"approval_reason":     "needs execution",
					"justification":       "requested by user",
					"sandbox_permissions": "workspace-write",
				},
			},
			Options: []schema.PermissionOption{{
				OptionID: "allow_once",
				Name:     "Allow once",
				Kind:     "allow_once",
			}},
		},
		Meta: acpToolNameMeta("RUN_COMMAND"),
	})
	if req == nil {
		t.Fatal("approvalPayloadFromACPEvent() = nil")
	}
	if req.ToolCallID != "call-1" || req.ToolName != "RUN_COMMAND" || req.Reason != "needs execution" {
		t.Fatalf("approval payload = %#v, want ACP permission fields", req)
	}
	if len(req.Options) != 1 || req.Options[0].ID != "allow_once" {
		t.Fatalf("approval options = %#v, want allow_once", req.Options)
	}
}

func parentToolStreamMeta(callID string, toolName string, mirrored bool) map[string]any {
	return metautil.WithRuntimeSection(nil, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamParentCallID:         strings.TrimSpace(callID),
		metautil.RuntimeStreamParentTool:           strings.TrimSpace(toolName),
		metautil.RuntimeStreamMirroredToParentTool: mirrored,
	})
}

func applyACPEnvelopeForTest(t *testing.T, model *Model, env eventstream.Envelope) *Model {
	t.Helper()
	next, _ := model.handleACPEventEnvelope(env)
	typed, ok := next.(*Model)
	if !ok {
		t.Fatalf("model = %T, want *Model", next)
	}
	return typed
}

func requireMainACPTurnBlockForTest(t *testing.T, model *Model) *MainACPTurnBlock {
	t.Helper()
	if id := strings.TrimSpace(model.activeMainACPTurnID); id != "" {
		if block, _ := model.doc.Find(id).(*MainACPTurnBlock); block != nil {
			return block
		}
	}
	for _, docBlock := range model.doc.Blocks() {
		if block, ok := docBlock.(*MainACPTurnBlock); ok {
			return block
		}
	}
	t.Fatal("main ACP turn block missing")
	return nil
}

func mainACPBlockContainsText(block *MainACPTurnBlock, text string) bool {
	text = strings.TrimSpace(text)
	if block == nil || text == "" {
		return false
	}
	for _, event := range block.Events {
		if strings.Contains(event.Text, text) {
			return true
		}
	}
	return false
}

type eventstreamIntegrationTurn struct {
	events <-chan eventstream.Envelope
}

func (t *eventstreamIntegrationTurn) HandleID() string { return "handle-1" }
func (t *eventstreamIntegrationTurn) RunID() string    { return "run-1" }
func (t *eventstreamIntegrationTurn) TurnID() string   { return "turn-1" }

func (t *eventstreamIntegrationTurn) Events() <-chan eventstream.Envelope {
	return t.events
}

func (t *eventstreamIntegrationTurn) SubmitApproval(context.Context, control.ApprovalDecision) error {
	return nil
}

func (t *eventstreamIntegrationTurn) Cancel() {}

func (t *eventstreamIntegrationTurn) Close() error { return nil }
