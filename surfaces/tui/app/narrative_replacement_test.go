package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func TestNarrativeReplacementLiveAndReplayRenderSameAcrossOwners(t *testing.T) {
	for _, lane := range []string{"main", "participant", "child"} {
		for _, kind := range []string{eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought} {
			t.Run(lane+"/"+kind, func(t *testing.T) {
				newModel := func() *Model {
					m := NewModel(Config{NoColor: true, NoAnimation: true})
					next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
					m = next.(*Model)
					m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(500, 0))
					if lane == "child" {
						m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn", Name: stringPtr("StartThread"), Kind: "execute", Status: "in_progress"}})
					}
					return m
				}
				apply := func(m *Model, text string, replace bool) *Model {
					env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ContentChunk{SessionUpdate: kind, MessageID: "same-message", Content: eventstream.TextContent{Type: "text", Text: text}, Replacement: replace}}
					if lane != "main" {
						env.Scope, env.ScopeID, env.Actor = eventstream.ScopeSubagent, "child", "child"
					}
					if lane == "participant" {
						env.Scope, env.ParticipantID = eventstream.ScopeParticipant, "participant"
					}
					if lane == "child" {
						env.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn", ToolName: "StartThread"}
					}
					return applyACPEnvelopeForTest(t, m, env)
				}
				render := func(m *Model) string {
					if lane == "main" {
						return joinRenderedPlain(requireMainACPTurnBlockForTest(t, m).Render(m.blockRenderContext(100)))
					}
					if lane == "participant" {
						b := m.findParticipantTurnBlock("turn")
						if b == nil {
							t.Fatal("participant missing")
						}
						return joinRenderedPlain(b.Render(m.blockRenderContext(100)))
					}
					return joinRenderedPlain(requireSubagentOutputViewForTest(t, m, "spawn").block.Render(m.blockRenderContext(100)))
				}
				live := newModel()
				live = apply(live, "stale bytes", false)
				live = apply(live, "snapshot bytes", true)
				if got := render(live); strings.Contains(got, "stale") || !strings.Contains(got, "snapshot bytes") {
					t.Fatalf("replacement render = %q", got)
				}
				live = apply(live, "", true)
				if got := render(live); strings.Contains(got, "snapshot bytes") {
					t.Fatalf("clear render retained stale bytes = %q", got)
				}
				live = apply(live, "current bytes", false)
				replay := apply(newModel(), "current bytes", true)
				if got, want := render(live), render(replay); got != want {
					t.Fatalf("live/replay differ\nlive: %q\nreplay: %q", got, want)
				}
			})
		}
	}
}

func TestObservedUserReplacementIsScopedAndCanClear(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	apply := func(scopeID, id, text string, replace bool) {
		t.Helper()
		m.applyGatewayUserEcho(gatewayUserEchoOptions{displayLine: text, event: TranscriptEvent{Scope: ACPProjectionMain, ScopeID: scopeID, TurnID: "turn", MessageID: id, Replacement: replace}})
	}
	apply("one", "user", "stale", false)
	apply("two", "user", "other session", false)
	apply("one", "user", "current", true)
	apply("one", "user", " suffix", false)
	var one, two *UserNarrativeBlock
	for _, item := range m.doc.Blocks() {
		if b, ok := item.(*UserNarrativeBlock); ok {
			if strings.Contains(b.observedMessage, "one") {
				one = b
			}
			if strings.Contains(b.observedMessage, "two") {
				two = b
			}
		}
	}
	if one == nil || two == nil || one.Raw != "current suffix" || two.Raw != "other session" {
		t.Fatalf("scoped users: %#v / %#v", one, two)
	}
	apply("one", "user", "", true)
	if one.Raw != "" || two.Raw != "other session" || len(one.Render(m.blockRenderContext(100))) != 0 {
		t.Fatal("clear changed another user or remained visible")
	}
}

func TestIdentifiedMessageReplacementRepairsAcrossToolBoundary(t *testing.T) {
	for _, lane := range []string{"main", "participant", "child"} {
		t.Run(lane, func(t *testing.T) {
			newModel := func() *Model {
				m := NewModel(Config{NoColor: true, NoAnimation: true})
				next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
				m = next.(*Model)
				m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(500, 0))
				if lane == "child" {
					m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn", Name: stringPtr("StartThread"), Kind: "execute", Status: "in_progress"}})
				}
				return m
			}
			apply := func(m *Model, update eventstream.Update) *Model {
				env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: update}
				if lane != "main" {
					env.Scope, env.ScopeID, env.Actor = eventstream.ScopeSubagent, "helper", "helper"
				}
				if lane == "participant" {
					env.Scope, env.ParticipantID = eventstream.ScopeParticipant, "participant"
				}
				if lane == "child" {
					env.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn", ToolName: "StartThread"}
				}
				return applyACPEnvelopeForTest(t, m, env)
			}
			message := func(id, text string, replacement bool) eventstream.Update {
				return eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: id, Content: eventstream.TextContent{Type: "text", Text: text}, Replacement: replacement}
			}
			tool := eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "tool", Name: stringPtr("ReadFile"), Kind: "read", Status: "completed", RawInput: map[string]any{"path": "example.txt"}}
			live := newModel()
			for _, update := range []eventstream.Update{
				message("old", "old text", true),
				message("latest", "progress", true),
				tool,
				// A codec materializes an explicitly identified continuation
				// across a boundary as the complete current message snapshot.
				message("latest", "progress continued", true),
				message("latest", " suffix", false),
				message("old", "old text late delta", true),
				message("old", "", true),
			} {
				live = apply(live, update)
			}
			replay := newModel()
			for _, update := range []eventstream.Update{message("old", "", true), message("latest", "progress continued suffix", true), tool} {
				replay = apply(replay, update)
			}
			render := func(m *Model) string {
				var events []SubagentEvent
				var rows []RenderedRow
				switch lane {
				case "main":
					b := requireMainACPTurnBlockForTest(t, m)
					events, rows = b.Events, b.Render(m.blockRenderContext(100))
				case "participant":
					b := m.findParticipantTurnBlock("turn")
					if b == nil {
						t.Fatal("participant missing")
					}
					events, rows = b.Events, b.Render(m.blockRenderContext(100))
				case "child":
					b := requireSubagentOutputViewForTest(t, m, "spawn").block
					events, rows = b.Events, b.Render(m.blockRenderContext(100))
				}
				var narratives []string
				for _, e := range events {
					if e.Kind == SEAssistant {
						narratives = append(narratives, e.Text)
					}
				}
				if len(narratives) != 2 || narratives[0] != "" || narratives[1] != "progress continued suffix" {
					t.Fatalf("identified repair appended another presentation run: %#v", narratives)
				}
				return joinRenderedPlain(rows)
			}
			if got, want := render(live), render(replay); got != want || strings.Contains(got, "old text") || strings.Count(got, "progress continued suffix") != 1 {
				t.Fatalf("cross-boundary live/replay differs or retains old content:\nlive=%q\nreplay=%q", got, want)
			}
		})
	}
}

func TestDirectedUserReplacementDoesNotRepeatAddressForContentBlocks(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	e := TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionParticipant, ScopeID: "participant", TurnID: "turn", Actor: "helper", NarrativeKind: TranscriptNarrativeUser, MessageID: "user", Text: "first", Replacement: true}
	m.handleDirectedParticipantUserMessage(e)
	e.Text, e.Replacement = " suffix", false
	m.handleDirectedParticipantUserMessage(e)
	var block *UserNarrativeBlock
	for _, item := range m.doc.Blocks() {
		if b, ok := item.(*UserNarrativeBlock); ok {
			block = b
		}
	}
	if block == nil || block.Raw != "/helper first suffix" {
		t.Fatalf("directed content repeated address: %#v", block)
	}
	e.Text, e.Replacement = "", true
	m.handleDirectedParticipantUserMessage(e)
	if block.Raw != "" {
		t.Fatalf("directed clear left address: %q", block.Raw)
	}
}

func TestToolDisplayPresenceAndTerminalSnapshotCanClear(t *testing.T) {
	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{CallID: "tool", Name: "RunCommand", Args: "executed command", Output: "old bytes", Meta: ToolUpdateMeta{ToolKind: "execute", ToolTitle: "old title", FullArgs: "execution evidence", Terminal: true, OutputTerminal: true}}, index)
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{CallID: "tool", Output: "snapshot", Meta: ToolUpdateMeta{OutputReplacement: true, OutputTerminal: true}}, index)
	if events[0].Output != "snapshot" || events[0].Args != "executed command" {
		t.Fatalf("snapshot appended or missing input cleared: %#v", events[0])
	}
	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{CallID: "tool", Meta: ToolUpdateMeta{OutputReplacement: true, ReplacedFields: []string{"name", "title", "kind", "rawInput"}}}, index)
	if e := events[0]; e.Output != "" || e.Name != "" || e.Title != "" || e.ToolKind != "" || e.Args != "" || e.FullArgs != "" || e.StartArgs != "" {
		t.Fatalf("explicit clear retained fields: %#v", e)
	}
}

func TestTerminalReplacementNativeProjectionPreservesExactBytes(t *testing.T) {
	newModel := func() *Model {
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
		m = next.(*Model)
		return applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "terminal", Name: stringPtr("RunCommand"), Kind: "execute", Status: "in_progress", RawInput: map[string]any{"command": "echo example"}}})
	}
	apply := func(m *Model, text string, replace bool) *Model {
		meta := map[string]any{"terminal_info": map[string]any{"terminal_id": "terminal"}, "terminal_output": map[string]any{"terminal_id": "terminal", "data": text}}
		if replace {
			meta["terminal_output"].(map[string]any)["replacement"] = true
		}
		return applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "terminal", Status: stringPtr("in_progress"), Meta: meta}})
	}
	m := apply(newModel(), "old bytes\n", false)
	m = apply(m, "snapshot bytes\n", true)
	block := requireMainACPTurnBlockForTest(t, m)
	if block.Events[0].Output != "snapshot bytes\n" {
		t.Fatalf("snapshot appended: %#v", block.Events)
	}
	m = apply(m, "", true)
	if block.Events[0].Output != "" || strings.Contains(joinRenderedPlain(block.Render(m.blockRenderContext(100))), "snapshot bytes") {
		t.Fatal("empty native snapshot was lost")
	}
	m = apply(m, "current\ncurrent\n", false)
	replay := apply(newModel(), "current\ncurrent\n", true)
	if got, want := joinRenderedPlain(block.Render(m.blockRenderContext(100))), joinRenderedPlain(requireMainACPTurnBlockForTest(t, replay).Render(replay.blockRenderContext(100))); got != want {
		t.Fatalf("terminal live/replay differ: %q / %q", got, want)
	}
}

func TestTerminalExitClearRemovesOnlySyntheticDisplay(t *testing.T) {
	for _, exact := range []bool{false, true} {
		events := []SubagentEvent{{Kind: SEToolCall, CallID: "command", Name: "RunCommand", Output: "exit display", OutputSynthetic: !exact, Done: true}}
		if exact {
			events[0].Output = "exact process bytes\n"
		}
		input := transcript.ToolProjectionInput{CallID: "command", ToolName: "RunCommand", Meta: map[string]any{"terminal_exit": map[string]any{"terminal_id": "command", "signal": nil}}, ReplacedFields: []string{"_meta"}}
		event, ok := projectTranscriptToolResult(input, "")
		if !ok || !event.ToolClearSyntheticOutput {
			t.Fatalf("exit null lost presence: %#v", event)
		}
		events, _, _ = applyToolEventUpdate(events, toolEventUpdate{CallID: "command", Name: event.ToolName, Output: event.ToolOutput, Meta: transcriptToolUpdateMeta(event)}, map[string]int{})
		if exact && events[0].Output != "exact process bytes\n" {
			t.Fatalf("exit clear removed process bytes: %#v", events[0])
		}
		if !exact && events[0].Output != "" {
			t.Fatalf("exit clear retained placeholder: %#v", events[0])
		}
		if !events[0].Done {
			t.Fatal("display clear reopened tool")
		}
	}
}

func TestAgentCommunicationObservedReplacementClearAndReplay(t *testing.T) {
	for _, lane := range []string{"main", "participant", "child"} {
		t.Run(lane, func(t *testing.T) {
			newModel := func() *Model {
				m := NewModel(Config{NoColor: true, NoAnimation: true})
				next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
				m = next.(*Model)
				m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(500, 0))
				if lane == "child" {
					m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn", Name: stringPtr("StartThread"), Kind: "execute", Status: "in_progress"}})
				}
				return m
			}
			apply := func(m *Model, text string, replacement bool) *Model {
				env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "controller", ID: "parent", Name: "parent"}, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, MessageID: "mail", Replacement: replacement, Content: eventstream.TextContent{Type: "text", Text: text}}}
				if lane != "main" {
					env.Scope, env.ScopeID, env.Actor = eventstream.ScopeSubagent, "helper", "helper"
				}
				if lane == "participant" {
					env.Scope, env.ParticipantID = eventstream.ScopeParticipant, "participant"
				}
				if lane == "child" {
					env.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn", ToolName: "StartThread"}
				}
				return applyACPEnvelopeForTest(t, m, env)
			}
			render := func(m *Model, want string) string {
				var events []SubagentEvent
				var rows []RenderedRow
				switch lane {
				case "main":
					b := requireMainACPTurnBlockForTest(t, m)
					events, rows = b.Events, b.Render(m.blockRenderContext(100))
				case "participant":
					b := m.findParticipantTurnBlock("turn")
					if b == nil {
						t.Fatal("participant missing")
					}
					events, rows = b.Events, b.Render(m.blockRenderContext(100))
				case "child":
					b := requireSubagentOutputViewForTest(t, m, "spawn").block
					events, rows = b.Events, b.Render(m.blockRenderContext(100))
				}
				if len(events) != 1 || events[0].Kind != SEAgentCommunication || events[0].Text != want || events[0].SourceID != "parent" {
					t.Fatalf("mail identity/source or exact body changed: %#v", events)
				}
				return joinRenderedPlain(rows)
			}
			live := apply(newModel(), "original", true)
			live = apply(live, " suffix", false)
			if got := render(live, "original suffix"); strings.Count(got, "parent:") != 1 || !strings.Contains(got, "original suffix") {
				t.Fatalf("mail continuation repeated address or lost spacing: %q", got)
			}
			live = apply(live, "updated", true)
			if got := render(live, "updated"); strings.Contains(got, "original") {
				t.Fatalf("mail replacement retained stale text: %q", got)
			}
			live = apply(live, "", true)
			if got := render(live, ""); strings.Contains(got, "parent:") || strings.Contains(got, "updated") {
				t.Fatalf("mail clear retained source-only row: %q", got)
			}
			live = apply(live, "current", false)
			replay := apply(newModel(), "current", true)
			if got, want := render(live, "current"), render(replay, "current"); got != want {
				t.Fatalf("mail live/replay differs: %q / %q", got, want)
			}
		})
	}
}

func TestAgentCommunicationDisplayPatchPreservesSourceAndScope(t *testing.T) {
	original := TranscriptEvent{Kind: TranscriptEventAgentCommunication, Scope: ACPProjectionMain, ScopeID: "one", TurnID: "turn", MessageID: "mail", Text: "body", AgentSourceKind: "controller", AgentSourceID: "parent", AgentSourceName: "parent", Replacement: true}
	events := []SubagentEvent{agentCommunicationSubagentEvent(original)}
	for _, modify := range []func(*TranscriptEvent){
		func(e *TranscriptEvent) { e.ScopeID = "two" },
		func(e *TranscriptEvent) { e.AgentSourceID = "sibling" },
		func(e *TranscriptEvent) { e.AgentSourceKind = "system" },
	} {
		incoming := original
		incoming.Text = ""
		modify(&incoming)
		if patchAgentCommunicationEvents(events, agentCommunicationSubagentEvent(incoming)) || events[0].Text != "body" {
			t.Fatal("observed repair crossed its display scope or typed source")
		}
	}
	original.Text = ""
	if !patchAgentCommunicationEvents(events, agentCommunicationSubagentEvent(original)) || events[0].Text != "" || events[0].SourceID != "parent" {
		t.Fatal("same scoped source clear lost identity")
	}
}
