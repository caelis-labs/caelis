package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func TestProjectACPEventToTranscriptEventsUsesEnvelopeScope(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Actor:   "copilot",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "subagent output"},
		},
		Final: true,
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].Scope != ACPProjectionSubagent || events[0].ScopeID != "task-1" || events[0].Actor != "copilot" {
		t.Fatalf("event scope = %#v, want subagent/task-1/copilot", events[0])
	}
}

func TestProjectACPEventToTranscriptEventsProjectsNotice(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:   eventstream.KindNotice,
		Notice: "gateway notice",
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].Kind != TranscriptEventNotice || events[0].Text != "gateway notice" {
		t.Fatalf("event = %#v, want notice text", events[0])
	}
}

func TestProjectACPEventToTranscriptEventsProjectsAttemptResetNotice(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindLifecycle,
		Lifecycle: &eventstream.Lifecycle{
			State: "attempt_reset",
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{
						"attempt":        2,
						"cause":          "model: http status 400",
						"max_retries":    5,
						"retry_delay_ms": 5000,
						"retrying":       true,
					},
				},
			},
		},
	})
	if len(events) != 2 {
		t.Fatalf("events = %#v, want lifecycle plus retry notice", events)
	}
	if events[0].Kind != TranscriptEventLifecycle || events[0].State != "attempt_reset" {
		t.Fatalf("first event = %#v, want attempt_reset lifecycle", events[0])
	}
	if events[1].Kind != TranscriptEventNotice || events[1].Text != "Retrying model request (2/5, retry in 5s)" {
		t.Fatalf("second event = %#v, want visible retry notice", events[1])
	}
	if events[1].NoticeKind != transcript.NoticeKindModelRetry {
		t.Fatalf("second event notice kind = %q, want model retry", events[1].NoticeKind)
	}
	if strings.Contains(events[1].Text, "http status 400") {
		t.Fatalf("retry notice leaked provider error: %q", events[1].Text)
	}
	if cause := transcript.MetaString(events[0].Meta, "caelis", "runtime", "attempt_reset", "cause"); cause != "" {
		t.Fatalf("lifecycle meta leaked retry cause: %q", cause)
	}
}

func TestProjectACPEventToTranscriptEventsProjectsCompactNotice(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one compact notice", events)
	}
	if events[0].Kind != TranscriptEventNotice || events[0].Text != transcript.CompactNoticeLabel {
		t.Fatalf("event = %#v, want compact notice", events[0])
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysSkillContentReadAsSkill(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "skill-review-1",
			Title:         `Read <skill_content name="review">`,
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusInProgress,
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	event := events[0]
	if event.Kind != TranscriptEventTool || event.ToolName != "SKILL" || event.ToolArgs != "review" || event.ToolKind != schema.ToolKindRead {
		t.Fatalf("event = %#v, want Skill review tool event", event)
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysStandardRawTerminalOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "side acp output\n"},
			Meta:          metautil.WithTerminalInfo(acpToolNameMeta("RUN_COMMAND"), "call-1"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "side acp output\n" {
		t.Fatalf("ToolOutput = %q, want standard raw terminal output", events[0].ToolOutput)
	}
	if !events[0].ToolOutputTerminal {
		t.Fatal("ToolOutputTerminal = false, want terminal raw output marked as terminal")
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysStandardRawOutputWithoutToolKind(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "side acp output\n"},
			Meta:          metautil.WithTerminalInfo(acpToolNameMeta("RUN_COMMAND"), "call-1"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "side acp output\n" {
		t.Fatalf("ToolOutput = %q, want standard raw terminal output", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysStandardTerminalContentWithoutToolKind(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			Kind:          stringPtr(schema.ToolKindExecute),
			Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "call-1", "terminal content\n"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "terminal content\n" {
		t.Fatalf("ToolOutput = %q, want standard terminal content", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysTerminalContentWithoutToolKind(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			Kind:          stringPtr(schema.ToolKindExecute),
			Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "call-1", "terminal content output\n"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "terminal content output\n" {
		t.Fatalf("ToolOutput = %q, want terminal content output", events[0].ToolOutput)
	}
	if !events[0].ToolOutputTerminal {
		t.Fatal("ToolOutputTerminal = false, want terminal content marked as terminal")
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysStringRawOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			Kind:          stringPtr(schema.ToolKindExecute),
			RawOutput:     "string raw output\n",
			Meta:          metautil.WithTerminalInfo(acpToolNameMeta("RUN_COMMAND"), "call-1"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "string raw output\n" {
		t.Fatalf("ToolOutput = %q, want string raw output", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsDoesNotDisplayGatewayProjectedRawTerminalOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Meta: map[string]any{
			"caelis": map[string]any{
				"bridge": map[string]any{"source": "gateway_projection"},
			},
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "hidden raw output\n"},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "" {
		t.Fatalf("ToolOutput = %q, want gateway-projected raw terminal output hidden without content", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsSuppressesRunningSnapshotTerminalOutputWhenStreamable(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusInProgress
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
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
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "" {
		t.Fatalf("ToolOutput = %q, want running snapshot terminal output suppressed while live stream owns display", events[0].ToolOutput)
	}
	if events[0].Final {
		t.Fatal("Final = true, want running snapshot to remain open")
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysTerminalStreamFrameOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusInProgress
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RUN_COMMAND echo ok"),
			Kind:          &kind,
			Status:        &status,
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "Step 3/5\n", "append"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "Step 3/5\n" {
		t.Fatalf("ToolOutput = %q, want live terminal stream frame output", events[0].ToolOutput)
	}
	if !events[0].ToolOutputTerminal {
		t.Fatal("ToolOutputTerminal = false, want live terminal stream output marked as terminal")
	}
}

func TestProjectACPEventToTranscriptEventsPreservesTerminalNewlineFrameOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusInProgress
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RUN_COMMAND echo ok"),
			Kind:          &kind,
			Status:        &status,
			Meta:          runningSnapshotTerminalMeta("RUN_COMMAND", "task-1", "terminal-1", "\n", "append"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "\n" {
		t.Fatalf("ToolOutput = %q, want newline terminal stream frame output", events[0].ToolOutput)
	}
}

func runningSnapshotTerminalMeta(toolName string, taskID string, terminalID string, output string, streamMode string) map[string]any {
	runtime := map[string]any{
		"tool": map[string]any{"name": toolName},
		"task": map[string]any{
			"task_id":       taskID,
			"terminal_id":   terminalID,
			"output_cursor": int64(len([]byte(output))),
			"running":       true,
			"state":         "running",
		},
	}
	if streamMode != "" {
		runtime["stream"] = map[string]any{"mode": streamMode}
	}
	meta := map[string]any{
		"caelis": map[string]any{
			"runtime": runtime,
		},
	}
	meta = metautil.WithTerminalInfo(meta, terminalID)
	if streamMode != "" {
		meta = metautil.WithTerminalOutput(meta, terminalID, output)
	}
	return meta
}
