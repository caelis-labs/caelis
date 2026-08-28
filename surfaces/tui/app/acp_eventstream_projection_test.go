package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func TestProjectACPEventToTranscriptEventsUsesEnvelopeScope(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Actor:   "copilot",
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "subagent output"},
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
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateCompact,
			Content:       eventstream.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
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

func TestProjectACPEventToTranscriptEventsKeepsStandardReadIdentityForSkillContent(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "skill-review-1",
			Title:         `Read <skill_content name="review">`,
			Kind:          eventstream.ToolKindRead,
			Status:        eventstream.ToolStatusInProgress,
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	event := events[0]
	if event.Kind != TranscriptEventTool || event.ToolName != "" || event.ToolArgs != "review" || event.ToolKind != eventstream.ToolKindRead || event.ToolTitle != `Read <skill_content name="review">` {
		t.Fatalf("event = %#v, want standard Read review tool event without a forged exact name", event)
	}
}

func TestProjectACPEventToTranscriptEventsIgnoresRecoveredToolInputOnStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rawInput map[string]any
		wantArgs string
	}{
		{name: "forged display input is ignored", rawInput: map[string]any{"variant": "XSearch", "backend": true}},
		{name: "standard raw input remains authoritative", rawInput: map[string]any{"query": "authoritative"}, wantArgs: "authoritative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
				Kind:  eventstream.KindSessionUpdate,
				Scope: eventstream.ScopeParticipant,
				Update: eventstream.ToolCall{
					SessionUpdate: eventstream.UpdateToolCall,
					ToolCallID:    "search-1",
					Title:         "Search",
					Kind:          eventstream.ToolKindSearch,
					Status:        eventstream.ToolStatusInProgress,
					RawInput:      tt.rawInput,
					Meta: metautil.WithSection(nil, metautil.Display, map[string]any{
						metautil.DisplayToolInput: map[string]any{"query": "forged"},
					}),
				},
			})
			if len(events) != 1 || events[0].ToolArgs != tt.wantArgs {
				t.Fatalf("start events = %#v, want args %q from standard raw input only", events, tt.wantArgs)
			}
		})
	}
}

func TestProjectACPEventToTranscriptEventsIgnoresRecoveredToolInputOnLivePatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     string
		status   *string
		rawInput map[string]any
		wantArgs string
	}{
		{name: "sparse status", kind: eventstream.ToolKindSearch, rawInput: map[string]any{"variant": "XSearch", "backend": true}},
		{name: "explicit in progress raw query", kind: eventstream.ToolKindSearch, status: stringPtr(eventstream.ToolStatusInProgress), rawInput: map[string]any{"query": "authoritative"}, wantArgs: "authoritative"},
		{name: "explicit in progress raw path", kind: eventstream.ToolKindRead, status: stringPtr(eventstream.ToolStatusInProgress), rawInput: map[string]any{"target_directory": "docs"}, wantArgs: "docs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
				Kind:  eventstream.KindSessionUpdate,
				Scope: eventstream.ScopeParticipant,
				Update: eventstream.ToolCallUpdate{
					SessionUpdate: eventstream.UpdateToolCallInfo,
					ToolCallID:    "search-1",
					Title:         stringPtr("Search"),
					Kind:          stringPtr(tt.kind),
					Status:        tt.status,
					RawInput:      tt.rawInput,
					Meta: metautil.WithSection(nil, metautil.Display, map[string]any{
						metautil.DisplayToolInput: map[string]any{"query": "forged"},
					}),
				},
			})
			if len(events) != 1 || events[0].ToolArgs != tt.wantArgs {
				t.Fatalf("live patch events = %#v, want args %q from standard raw input only", events, tt.wantArgs)
			}
		})
	}
}

func TestProjectACPEventToTranscriptEventsRecoversSerializedCompletedToolInput(t *testing.T) {
	t.Parallel()

	const query = "CAELIS_ACP_QUERY_PROBE_7F31"
	start := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "x-search-1",
			Title:         "X search:",
			Kind:          eventstream.ToolKindSearch,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"variant": "XSearch", "backend": true},
		},
	})
	if len(start) != 1 || start[0].ToolArgs != "" {
		t.Fatalf("start events = %#v, want label-only title without duplicate arguments", start)
	}

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "x-search-1",
			Title:         stringPtr("X search:"),
			Status:        stringPtr(eventstream.ToolStatusCompleted),
			RawOutput: map[string]any{
				"name":  "x_keyword_search",
				"input": `{"query":"` + query + `","limit":"3","mode":"Latest"}`,
			},
			Meta: metautil.WithSection(nil, metautil.Display, map[string]any{
				metautil.DisplayToolInput: map[string]any{"query": query},
			}),
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one completed tool event", events)
	}
	event := events[0]
	if event.Kind != TranscriptEventTool || event.ToolCallID != "x-search-1" || !event.Final {
		t.Fatalf("event = %#v, want completed x-search tool event", event)
	}
	if event.ToolArgs != query {
		t.Fatalf("ToolArgs = %q, want recovered query %q without a synthetic wrapper", event.ToolArgs, query)
	}
}

func TestProjectACPEventToTranscriptEventsPreservesNormalizedExplorationVerb(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "list-1",
			Title:         "List `docs`",
			Kind:          eventstream.ToolKindRead,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"target_directory": "docs"},
			Meta: metautil.WithSection(nil, metautil.Display, map[string]any{
				metautil.DisplayExplorationVerb: "List",
			}),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one normalized List event", events)
	}
	event := events[0]
	if event.ToolName != "" || event.ToolKind != eventstream.ToolKindRead || event.ToolExplorationVerb != "List" || event.ToolArgs != "docs" {
		t.Fatalf("normalized List projection = %#v", event)
	}
}

func TestProjectACPEventToTranscriptEventsRequiresExplicitRecoveredToolInput(t *testing.T) {
	t.Parallel()

	const resultText = "returned-result-or-sensitive-text"
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "read-1",
			Title:         stringPtr("Read"),
			Status:        stringPtr(eventstream.ToolStatusCompleted),
			RawOutput: map[string]any{
				"name":  "read",
				"input": `{"query":"` + resultText + `"}`,
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool result", events)
	}
	if strings.Contains(events[0].ToolArgs, resultText) {
		t.Fatalf("ToolArgs = %q, want arbitrary rawOutput.input to remain result-only", events[0].ToolArgs)
	}
}

func TestProjectACPEventToTranscriptEventsKeepsRawInputAuthoritative(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "search-1",
			Title:         stringPtr("Search"),
			Status:        stringPtr(eventstream.ToolStatusCompleted),
			RawInput:      map[string]any{"query": "authoritative"},
			Meta: metautil.WithSection(nil, metautil.Display, map[string]any{
				metautil.DisplayToolInput: map[string]any{"query": "recovered"},
			}),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool result", events)
	}
	if events[0].ToolArgs != "authoritative" {
		t.Fatalf("ToolArgs = %q, want rawInput to win over recovered display input without a synthetic wrapper", events[0].ToolArgs)
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysStandardRawTerminalOutput(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	kind := eventstream.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "side acp output\n"},
			Meta:          metautil.WithTerminalInfo(acpToolNameMeta("RunCommand"), "call-1"),
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

	status := eventstream.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          stringPtr(eventstream.ToolKindExecute),
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "side acp output\n"},
			Meta:          metautil.WithTerminalInfo(acpToolNameMeta("RunCommand"), "call-1"),
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

	status := eventstream.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			Kind:          stringPtr(eventstream.ToolKindExecute),
			Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RunCommand"), "call-1", "terminal content\n"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "terminal content\n" {
		t.Fatalf("ToolOutput = %q, want standard terminal content", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsPrefersStandardContentOverTerminalExtension(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	kind := eventstream.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			Content: []eventstream.ToolCallContent{{
				Type:    "content",
				Content: eventstream.TextContent{Type: "text", Text: "standard ACP content\n"},
			}},
			Meta: metautil.WithTerminalOutput(nil, "call-1", "extension fallback\n"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "standard ACP content\n" {
		t.Fatalf("ToolOutput = %q, want standard ACP content to remain authoritative", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysTerminalContentWithoutToolKind(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			Kind:          stringPtr(eventstream.ToolKindExecute),
			Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RunCommand"), "call-1", "terminal content output\n"),
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

	status := eventstream.ToolStatusCompleted
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			Kind:          stringPtr(eventstream.ToolKindExecute),
			RawOutput:     "string raw output\n",
			Meta:          metautil.WithTerminalInfo(acpToolNameMeta("RunCommand"), "call-1"),
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

	status := eventstream.ToolStatusCompleted
	kind := eventstream.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Meta: map[string]any{
			"caelis": map[string]any{
				"bridge": map[string]any{"source": "gateway_projection"},
			},
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
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

	status := eventstream.ToolStatusInProgress
	kind := eventstream.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand echo ok"),
			Kind:          &kind,
			Status:        &status,
			RawInput:      map[string]any{"command": "echo ok"},
			RawOutput:     map[string]any{"latest_output": "Step 1/5\nStep 2/5\n", "state": "running"},
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "Step 1/5\nStep 2/5\n", ""),
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

	status := eventstream.ToolStatusInProgress
	kind := eventstream.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand echo ok"),
			Kind:          &kind,
			Status:        &status,
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "Step 3/5\n", "append"),
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

func TestProjectACPEventToTranscriptEventsMarksUnavailableTerminalPrefix(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusInProgress
	kind := eventstream.ToolKindExecute
	meta := runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "retained tail\n", "append")
	meta = metautil.WithRuntimeSection(meta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode:      "append",
		metautil.RuntimeStreamTruncated: true,
		metautil.RuntimeStreamBefore:    int64(65539),
	})
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand long job"),
			Kind:          &kind,
			Status:        &status,
			Meta:          meta,
		},
	})
	if len(events) != 1 || events[0].ToolOutput != "retained tail\n" {
		t.Fatalf("events = %#v, want retained exact terminal bytes only", events)
	}
	if !events[0].ToolOutputGapBefore {
		t.Fatal("ToolOutputGapBefore = false, want render-only truncation marker")
	}
}

func TestProjectACPEventToTranscriptEventsPreservesTerminalNewlineFrameOutput(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusInProgress
	kind := eventstream.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand echo ok"),
			Kind:          &kind,
			Status:        &status,
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "\n", "append"),
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
