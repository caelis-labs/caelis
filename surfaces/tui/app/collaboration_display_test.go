package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestCollaborationObservationPresentation(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"ListThreads", "timeout"}, {"ReadThread", "timeout"},
		{"WaitThread", "timeout"}, {"WaitThread", "input"}, {"ReceiveMessages", "timeout"},
	} {
		name := tc.name
		t.Run(name+"/"+tc.reason, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.width, m.height = 100, 40
			m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
			env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "turn", Scope: eventstream.ScopeMain,
				Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "observe", Title: name, Kind: eventstream.ToolKindOther, Status: eventstream.ToolStatusInProgress, Meta: acpToolNameMeta(name)}}
			m = applyACPEnvelopeForTest(t, m, env)
			if name == "WaitThread" && m.runningActivity.Phase != runningPhaseToolWait {
				t.Fatalf("missing wait hint: %#v", m.runningActivity)
			}
			status := eventstream.ToolStatusCompleted
			env.Update = eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "observe", Status: &status, Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: `{"reason":"` + tc.reason + `"}`}}}, Meta: acpToolNameMeta(name)}
			m = applyACPEnvelopeForTest(t, m, env)
			for _, block := range m.doc.Blocks() {
				for _, row := range block.Render(m.blockRenderContext(100)) {
					if strings.Contains(row.Plain, name) || strings.Contains(row.Plain, tc.reason) {
						t.Fatalf("control leaked: %s", row.Plain)
					}
				}
			}
			failure := TranscriptEvent{Kind: TranscriptEventTool, ToolName: name, ToolError: true, Final: true, ToolStatus: "failed"}
			if _, hidden := hiddenTaskControlAction(failure); hidden {
				t.Fatal("control failure hidden")
			}
		})
	}
}

func TestSendMessageReturnedMailRendersInMainAndChildHistory(t *testing.T) {
	for _, mode := range []string{"main", "child live", "child history"} {
		t.Run(mode, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.currentSessionID = "session-1"
			m.width, m.height = 120, 40
			m.taskStreamWanted["task-1"] = true
			m.taskStreamTokens["task-1"] = 7
			m.taskStreamCallIDsByID["task-1"] = "spawn-1"
			m.taskStreamIDsByCallID["spawn-1"] = "task-1"
			view := m.ensureSubagentOutputView("spawn-1")
			view.taskHandle, view.actor = "zuri", "zuri[breeze]"
			stage := &subagentOutputHistoryStage{view: newSubagentOutputHistoryView(view)}
			completed := eventstream.ToolStatusCompleted
			payload := `{"id":"out-1","status":"queued","messages":[{"id":"in-1","from":"reviewer","message":"Review complete."},{"id":"in-2","from":"tester","message":"Tests passed."}]}`
			updates := []eventstream.Update{
				eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "send-1", Title: "SendMessage", Kind: eventstream.ToolKindOther, Status: eventstream.ToolStatusInProgress, RawInput: map[string]any{"to": "parent", "message": "progress update"}, Meta: acpToolNameMeta("SendMessage")},
				eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "send-1", Status: &completed, Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: payload}}}, Meta: acpToolNameMeta("SendMessage")},
			}
			updates = append(updates, updates[1]) // A repeated final must not duplicate returned mail.
			for _, update := range updates {
				env := subagentMailboxEnvelope(t, "activity-1", time.Unix(120, 0), update)
				switch mode {
				case "main":
					env.Scope, env.ScopeID, env.ParentTool = eventstream.ScopeMain, "", nil
					m = applyACPEnvelopeForTest(t, m, env)
				case "child live":
					next, _ := m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session-1", taskID: "task-1", token: 7, events: []eventstream.Envelope{env}})
					m = next.(*Model)
				case "child history":
					m.observeSubagentOutputHistoryEnvelope(stage, env)
				}
			}
			var rows []string
			if mode == "main" {
				m.syncViewportContent()
				rows = m.viewportPlainLines
			} else {
				if mode == "child history" {
					view = stage.view
				}
				view.prepareVisibleRender()
				rows = renderedPlainRows(m.subagentOutputRows(view, 120, 40))
			}
			plain := strings.Join(rows, "\n")
			t.Log(plain)
			for _, want := range []string{"progress update", "reviewer: Review complete.", "tester: Tests passed."} {
				if strings.Count(plain, want) != 1 {
					t.Fatalf("missing or repeated %q:\n%s", want, plain)
				}
			}
			if strings.Index(plain, "Review complete.") > strings.Index(plain, "Tests passed.") {
				t.Fatalf("mail order changed:\n%s", plain)
			}
			for _, hidden := range []string{"queued", "out-1", "in-1", "in-2", `"messages"`} {
				if strings.Contains(plain, hidden) {
					t.Fatalf("mail metadata leaked %q:\n%s", hidden, plain)
				}
			}
		})
	}
}

func TestUntypedMailboxJSONStaysLiteralUserInput(t *testing.T) {
	payload := `{"id":"mail-1","from":"parent","to":"zuri","message":"continue from parent"}`
	events := expandCollaborationMessages([]TranscriptEvent{{
		Kind: TranscriptEventNarrative, NarrativeKind: TranscriptNarrativeUser,
		Scope: ACPProjectionSubagent, Text: payload,
	}})
	if len(events) != 1 || events[0].Kind != TranscriptEventNarrative ||
		events[0].NarrativeKind != TranscriptNarrativeUser || events[0].Text != payload ||
		events[0].AgentSourceName != "" {
		t.Fatalf("untyped JSON reclassified: %#v", events)
	}
}

func TestMailboxMessagesReuseIncomingPresentation(t *testing.T) {
	for _, name := range []string{"SendMessage", "ReceiveMessages", "WaitThread"} {
		payload := `[{"id":"mail-1","from":"review-runtime","message":"Review complete."}]`
		if name != "ReceiveMessages" {
			payload = `{"messages":` + payload + `,"threads":[]}`
		}
		events := expandCollaborationMessages([]TranscriptEvent{{Kind: TranscriptEventTool, ToolName: name, Final: true, ToolOutput: payload}})
		if len(events) != 2 || events[1].Kind != TranscriptEventAgentCommunication || events[1].AgentSourceName != "review-runtime" || events[1].Text != "Review complete." || events[1].MessageID != "mail-1" {
			t.Fatalf("mail projection: %#v", events)
		}
	}
}

func TestToolReviewRendersOnOriginalHeader(t *testing.T) {
	for _, status := range []string{"denied", "approved"} {
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		block := &MainACPTurnBlock{}
		block.UpdateToolWithMeta("edit-1", "Edit", "file.go", "normal result", true, status == "denied", ToolUpdateMeta{ToolKind: "edit"})
		block.AddApprovalReviewEvent("edit-1", "Edit", "file.go", status, "", "", "denied: outside the requested scope")
		plain := strings.Join(renderedPlainRows(block.Render(m.blockRenderContext(100))), "\n")
		t.Log(plain)
		if strings.Contains(plain, "Auto approval") || !strings.Contains(plain, "file.go "+status) {
			t.Fatalf("review header: %s", plain)
		}
		if status == "denied" && (strings.Contains(plain, "normal result") || strings.Count(plain, "outside the requested scope") != 1) {
			t.Fatalf("denial result: %s", plain)
		}
		if status == "approved" && !strings.Contains(plain, "normal result") {
			t.Fatalf("approved result missing: %s", plain)
		}
	}
}

func TestMailboxResultRendersMessagesWithoutControlJSON(t *testing.T) {
	for _, name := range []string{"SendMessage", "ReceiveMessages", "WaitThread"} {
		t.Run(name, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			payload := `[{"id":"mail-1","from":"review-runtime","message":"Review complete."}]`
			if name != "ReceiveMessages" {
				payload = `{"messages":` + payload + `,"threads":[]}`
			}
			status := eventstream.ToolStatusCompleted
			env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "receive", Status: &status, Meta: acpToolNameMeta(name), Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: payload}}}}}
			m = applyACPEnvelopeForTest(t, m, env)
			var lines []string
			for _, b := range m.doc.Blocks() {
				lines = append(lines, renderedPlainRows(b.Render(m.blockRenderContext(120)))...)
			}
			plain := strings.Join(lines, "\n")
			t.Log(plain)
			if !strings.Contains(plain, "review-runtime: Review complete.") || strings.Contains(plain, name) || strings.Contains(plain, `"id"`) {
				t.Fatalf("mailbox display: %s", plain)
			}
		})
	}
}
