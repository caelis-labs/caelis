package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestCollaborationObservationPresentation(t *testing.T) {
	for _, name := range []string{"ListThreads", "ReadThread", "WaitThread", "ReceiveMessages"} {
		t.Run(name, func(t *testing.T) {
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
			env.Update = eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "observe", Status: &status, Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: `{"reason":"timeout","messages":[],"threads":[]}`}}}, Meta: acpToolNameMeta(name)}
			m = applyACPEnvelopeForTest(t, m, env)
			for _, block := range m.doc.Blocks() {
				for _, row := range block.Render(m.blockRenderContext(100)) {
					if strings.Contains(row.Plain, name) || strings.Contains(row.Plain, "timeout") {
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
	for _, name := range []string{"ReceiveMessages", "WaitThread"} {
		payload := `[{"id":"mail-1","from":"review-runtime","to":"parent","message":"Review complete."}]`
		if name == "WaitThread" {
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
	for _, name := range []string{"ReceiveMessages", "WaitThread"} {
		t.Run(name, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			payload := `[{"id":"mail-1","from":"review-runtime","to":"parent","message":"Review complete."}]`
			if name == "WaitThread" {
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
