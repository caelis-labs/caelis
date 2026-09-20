package tuiapp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

// A collaboration ReadMessages result is a shared-log page. The pane shows only
// the mail addressed to its own Host-resolved Handle, keeps one row per
// MessageID across reads, mailbox results, and direct delivery, and defaults
// every received row to the shared compact preview.
const readMessagesMCPTitle = "caelis-collaboration / ReadMessages"

type readMessagesMail struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Message string `json:"message"`
}

func readMessagesPayload(mails ...readMessagesMail) string {
	body, err := json.Marshal(struct {
		Messages []readMessagesMail `json:"messages"`
	}{Messages: mails})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func readMessagesStringPtr(value string) *string { return &value }

// The five production entries exercised by the existing mailbox display test:
// the main timeline plus each child-pane delivery shape.
const (
	readMessagesMainMode         = "main"
	readMessagesChildLiveMode    = "child live"
	readMessagesChildHistoryMode = "child history"
	readMessagesChildPagedMode   = "child paged history"
	readMessagesChildEarlierMode = "child earlier history"
)

var readMessagesModes = []string{
	readMessagesMainMode,
	readMessagesChildLiveMode,
	readMessagesChildHistoryMode,
	readMessagesChildPagedMode,
	readMessagesChildEarlierMode,
}

func readMessagesTestModel() *Model {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.currentSessionID = "session-1"
	m.width, m.height = 120, 40
	m.taskStreamWanted["task-1"] = true
	m.taskStreamTokens["task-1"] = 7
	m.taskStreamCallIDsByID["task-1"] = "spawn-1"
	m.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := m.ensureSubagentOutputView("spawn-1")
	view.taskHandle, view.actor = "zuri", "zuri[breeze]"
	return m
}

// The normalized Meta carries the tool identity; the ACP Title is the same
// provider label an external MCP downstream leaves in place. Surfaces must use
// the former and never guess a pane from the latter.
func readMessagesUpdates(callID, payload string) []eventstream.Update {
	completed := eventstream.ToolStatusCompleted
	return []eventstream.Update{
		eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: callID,
			Title: readMessagesMCPTitle, Kind: eventstream.ToolKindOther,
			Status: eventstream.ToolStatusInProgress, Meta: acpToolNameMeta("ReadMessages"),
		},
		eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: callID, Status: &completed,
			Title:   readMessagesStringPtr(readMessagesMCPTitle),
			Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: payload}}},
			Meta:    acpToolNameMeta("ReadMessages"),
		},
	}
}

func newReadMessagesModeModel(t *testing.T, mode string) (*Model, *earlierHistoryBuild) {
	t.Helper()
	m := readMessagesTestModel()
	switch mode {
	case readMessagesChildPagedMode:
		m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session-1", taskID: "task-1", token: 7, phase: taskstream.DeliveryReplaceBegin})
	case readMessagesChildEarlierMode:
		view := m.subagentOutputViews["spawn-1"]
		child := *view
		child.resetForReplacement()
		build := &earlierHistoryBuild{generation: m.viewGeneration, before: "older", child: &child, cancel: func() {}}
		m.earlierHistory = map[string]*earlierHistoryBuild{"spawn-1": build}
		view.historyBefore = build.before
		return m, build
	}
	return m, nil
}

func applyReadMessagesMode(t *testing.T, m *Model, build *earlierHistoryBuild, mode, callID, turnID, payload string) *Model {
	t.Helper()
	at := time.Unix(120, 0)
	for index, update := range readMessagesUpdates(callID, payload) {
		switch mode {
		case readMessagesMainMode:
			m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
				Scope: eventstream.ScopeMain, Update: update,
			})
		case readMessagesChildLiveMode, readMessagesChildHistoryMode:
			env := subagentMailboxEnvelope(t, turnID, at, update)
			next, _ := m.handleTaskStreamBatch(taskStreamBatchMsg{
				sessionID: "session-1", taskID: "task-1", token: 7,
				replacement: mode == readMessagesChildHistoryMode && index == 0,
				events:      []eventstream.Envelope{env},
			})
			m = next.(*Model)
		case readMessagesChildPagedMode:
			m.handleTaskStreamBatch(taskStreamBatchMsg{
				sessionID: "session-1", taskID: "task-1", token: 7,
				phase:  taskstream.DeliveryReplacePage,
				events: []eventstream.Envelope{subagentMailboxEnvelope(t, turnID, at, update)},
			})
		case readMessagesChildEarlierMode:
			m.handleEarlierHistory(earlierHistoryMsg{
				callID: "spawn-1", build: build,
				message: taskStreamBatchMsg{events: []eventstream.Envelope{subagentMailboxEnvelope(t, turnID, at, update)}},
			})
		}
	}
	switch mode {
	case readMessagesChildPagedMode:
		m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session-1", taskID: "task-1", token: 7, phase: taskstream.DeliveryReplaceEnd})
	case readMessagesChildEarlierMode:
		m.handleEarlierHistory(earlierHistoryMsg{callID: "spawn-1", build: build, done: true})
	}
	return m
}

func readMessagesModePlain(t *testing.T, m *Model, mode string) string {
	t.Helper()
	if mode == readMessagesMainMode {
		m.syncViewportContent()
		return strings.Join(m.viewportPlainLines, "\n")
	}
	view := m.subagentOutputViews["spawn-1"]
	view.prepareVisibleRender()
	return strings.Join(renderedPlainRows(m.subagentOutputRows(view, 120, 40)), "\n")
}

func assertReadMessagesShown(t *testing.T, plain string, want string) {
	t.Helper()
	if strings.Count(plain, want) != 1 {
		t.Fatalf("want exactly one %q:\n%s", want, plain)
	}
}

func assertReadMessagesHidden(t *testing.T, plain string, hidden ...string) {
	t.Helper()
	for _, needle := range hidden {
		if strings.Contains(plain, needle) {
			t.Fatalf("hidden %q leaked into the pane:\n%s", needle, plain)
		}
	}
}

func assertNoReadMessagesMetadata(t *testing.T, plain string) {
	t.Helper()
	for _, leak := range []string{"ReadMessages", "caelis-collaboration", `"messages"`, `"from"`, `"to"`, `"id"`} {
		if strings.Contains(plain, leak) {
			t.Fatalf("ReadMessages metadata leaked %q:\n%s", leak, plain)
		}
	}
}

// A shared-log page addressed to several handles must expose only the mail for
// the observing pane: main reads as parent, a child reads as its Spawn Handle.
func TestReadMessagesMixedPageShowsOnlyPaneAddress(t *testing.T) {
	for _, mode := range readMessagesModes {
		t.Run(mode, func(t *testing.T) {
			m, build := newReadMessagesModeModel(t, mode)
			payload := readMessagesPayload(
				readMessagesMail{ID: "mail-parent", From: "reviewer", To: "parent", Message: "body-for-parent"},
				readMessagesMail{ID: "mail-zuri", From: "parent", To: "zuri", Message: "body-for-zuri"},
				readMessagesMail{ID: "mail-other", From: "tester", To: "other", Message: "body-for-other"},
			)
			m = applyReadMessagesMode(t, m, build, mode, "read-1", "turn-1", payload)
			plain := readMessagesModePlain(t, m, mode)
			t.Logf("rendered pane:\n%s", plain)
			if mode == readMessagesMainMode {
				assertReadMessagesShown(t, plain, "reviewer: body-for-parent")
				assertReadMessagesHidden(t, plain, "body-for-zuri", "body-for-other")
			} else {
				assertReadMessagesShown(t, plain, "parent: body-for-zuri")
				assertReadMessagesHidden(t, plain, "body-for-parent", "body-for-other")
			}
			assertNoReadMessagesMetadata(t, plain)
		})
	}
}

// A page for someone else, or an empty page, renders nothing. The success tool
// row itself stays hidden, so no tool name or result JSON reaches the pane.
func TestReadMessagesForeignOrEmptyPageShowsNothing(t *testing.T) {
	for _, mode := range []string{readMessagesMainMode, readMessagesChildLiveMode} {
		for _, tc := range []struct {
			name    string
			payload string
		}{
			{"foreign", readMessagesPayload(readMessagesMail{ID: "mail-other", From: "tester", To: "other", Message: "for-other"})},
			{"empty", readMessagesPayload()},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				m, build := newReadMessagesModeModel(t, mode)
				before := readMessagesModePlain(t, m, mode)
				m = applyReadMessagesMode(t, m, build, mode, "read-empty", "turn-1", tc.payload)
				plain := readMessagesModePlain(t, m, mode)
				t.Logf("empty observation rendered: %q", plain)
				if plain != before {
					t.Fatalf("empty observation changed the pane: before=%q after=%q", before, plain)
				}
				assertReadMessagesHidden(t, plain, "for-other")
				assertNoReadMessagesMetadata(t, plain)
			})
		}
	}
}

// A genuinely failed read is a control failure, not a hidden observation: its
// row stays visible even though the successful observation row is consumed.
func TestReadMessagesFailureStaysVisible(t *testing.T) {
	for _, mode := range []string{readMessagesMainMode, readMessagesChildLiveMode} {
		t.Run(mode, func(t *testing.T) {
			m, _ := newReadMessagesModeModel(t, mode)
			failed := eventstream.ToolStatusFailed
			update := eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "read-fail", Status: &failed,
				Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: "mailbox unavailable"}}},
				Meta:    acpToolNameMeta("ReadMessages"),
			}
			env := eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
				Scope: eventstream.ScopeMain, Update: update,
			}
			if mode != readMessagesMainMode {
				env = subagentMailboxEnvelope(t, "turn-1", time.Unix(120, 0), update)
			}
			m = applyACPEnvelopeForTest(t, m, env)
			plain := readMessagesModePlain(t, m, mode)
			t.Logf("failed read:\n%s", plain)
			if !strings.Contains(plain, "ReadMessages") || !strings.Contains(plain, "failed") || !strings.Contains(plain, "mailbox unavailable") {
				t.Fatalf("failed ReadMessages row is not visible:\n%s", plain)
			}
		})
	}
}

func readMessagesLongBody(marker string) string {
	filler := strings.Repeat("overflow payload line\n", 120)
	return "start\n" + filler + "deep " + marker + "\n" + filler + "end"
}

// Every received row defaults to the compact preview, including the ~5KB pages
// a real mailbox read returns. The row's own fold token expands and collapses
// it in place without navigating away.
func TestReadMessagesLongBodyFoldsInPlace(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		m, build := newReadMessagesModeModel(t, readMessagesMainMode)
		payload := readMessagesPayload(readMessagesMail{ID: "mail-long", From: "reviewer", To: "parent", Message: readMessagesLongBody("deep-fold-marker")})
		m = applyReadMessagesMode(t, m, build, readMessagesMainMode, "read-long", "turn-1", payload)
		block := requireMainACPTurnBlockForTest(t, m)
		row := requireRenderedRowContaining(t, block.Render(m.blockRenderContext(120)), "• reviewer:")
		t.Logf("collapsed main mail: %s", row.Plain)
		if !strings.HasPrefix(row.ClickToken, agentMessageFoldTokenPrefix) {
			t.Fatalf("folded mail row token = %q, want in-place fold", row.ClickToken)
		}
		if strings.Contains(row.Plain, "deep-fold-marker") {
			t.Fatalf("folded mail row leaked its body: %q", row.Plain)
		}

		// The viewport click path resolves and toggles the row's own token.
		m.syncViewportContent()
		headerLine := -1
		for index, line := range m.viewportPlainLines {
			if strings.Contains(line, "• reviewer:") {
				headerLine = index
				break
			}
		}
		if headerLine < 0 {
			t.Fatalf("mail row missing from the viewport:\n%s", strings.Join(m.viewportPlainLines, "\n"))
		}
		foldToken := m.viewportClickTokens[headerLine]
		if !strings.HasPrefix(foldToken, agentMessageFoldTokenPrefix) {
			t.Fatalf("viewport fold token = %q, want in-place fold", foldToken)
		}
		clickViewportColumn(t, m, headerLine, 4)
		if plain := strings.Join(m.viewportPlainLines, "\n"); !strings.Contains(plain, "deep-fold-marker") {
			t.Fatalf("viewport click did not expand the mail:\n%s", plain)
		}
		if !m.tryToggleFoldToken(block.BlockID(), foldToken) {
			t.Fatal("collapsing the expanded mail failed")
		}
		m.syncViewportContent()
		if plain := strings.Join(m.viewportPlainLines, "\n"); strings.Contains(plain, "deep-fold-marker") {
			t.Fatalf("collapse kept the expanded mail:\n%s", plain)
		}
	})

	t.Run("child", func(t *testing.T) {
		m, _ := newReadMessagesModeModel(t, readMessagesChildLiveMode)
		oldPayload := readMessagesPayload(readMessagesMail{ID: "mail-old", From: "parent", To: "zuri", Message: readMessagesLongBody("old-fold-marker")})
		m = applyACPEnvelopeForTest(t, m, subagentMailboxEnvelope(t, "turn-1", time.Unix(120, 0), readMessagesUpdates("read-old", oldPayload)[1]))
		newPayload := readMessagesPayload(readMessagesMail{ID: "mail-new", From: "parent", To: "zuri", Message: readMessagesLongBody("new-fold-marker")})
		m = applyACPEnvelopeForTest(t, m, subagentMailboxEnvelope(t, "turn-2", time.Unix(240, 0), readMessagesUpdates("read-new", newPayload)[1]))

		view := m.subagentOutputViews["spawn-1"]
		if blocks := view.document.Blocks(); len(blocks) != 2 {
			t.Fatalf("child blocks = %d, want one earlier and one current Turn", len(blocks))
		}
		foldToken := agentMessageFoldClickToken("message:mail-new")
		view.prepareVisibleRender()
		t.Logf("collapsed child mail:\n%s", strings.Join(renderedPlainRows(m.subagentOutputRows(view, 120, 40)), "\n"))
		if plain := strings.Join(renderedPlainRows(m.subagentOutputRows(view, 120, 40)), "\n"); strings.Contains(plain, "new-fold-marker") || strings.Contains(plain, "old-fold-marker") {
			t.Fatalf("child panes did not default to the compact preview:\n%s", plain)
		}

		m.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
		_ = m.renderSubagentOutputOverlay()
		m = clickSubagentOutputFoldRowForTest(t, m, foldToken)
		plain := strings.Join(renderedPlainRows(m.subagentOutputRows(view, 120, 40)), "\n")
		if !strings.Contains(plain, "new-fold-marker") {
			t.Fatalf("child fold click did not expand the current block:\n%s", plain)
		}
		if strings.Contains(plain, "old-fold-marker") {
			t.Fatalf("child fold click expanded the earlier block:\n%s", plain)
		}
		if !m.toggleSubagentOutputRow(foldToken) {
			t.Fatal("collapsing the expanded child mail failed")
		}
		if plain := strings.Join(renderedPlainRows(m.subagentOutputRows(view, 120, 40)), "\n"); strings.Contains(plain, "new-fold-marker") {
			t.Fatalf("child collapse kept the expanded mail:\n%s", plain)
		}
	})
}

// One mail can arrive through a shared-log read, a SendMessage result, and a
// direct typed delivery. The pane keeps the first row per MessageID, including
// when the repeat lands in a later Turn.
func TestReadMessagesDedupesAcrossReadReturnedAndDirectDelivery(t *testing.T) {
	for _, mode := range []string{readMessagesMainMode, readMessagesChildLiveMode} {
		t.Run(mode, func(t *testing.T) {
			m, build := newReadMessagesModeModel(t, mode)
			pane := "parent"
			if mode != readMessagesMainMode {
				pane = "zuri"
			}
			m = applyReadMessagesDirectDelivery(t, m, mode, "turn-1", "participant-1", "reviewer", "mail-1", "first body")
			m = applyReadMessagesMode(t, m, build, mode, "read-1", "turn-2", readMessagesPayload(
				readMessagesMail{ID: "mail-1", From: "reviewer", To: pane, Message: "first body"},
				readMessagesMail{ID: "mail-2", From: "reviewer", To: pane, Message: "second body"},
			))
			m = applyReadMessagesSendReturned(t, m, mode, "turn-2", "send-1", readMessagesPayload(
				readMessagesMail{ID: "mail-1", From: "reviewer", To: pane, Message: "first body"},
				readMessagesMail{ID: "mail-3", From: "reviewer", To: pane, Message: "third body"},
			))
			m = applyReadMessagesMode(t, m, build, mode, "read-2", "turn-3", readMessagesPayload(
				readMessagesMail{ID: "mail-2", From: "reviewer", To: pane, Message: "second body"},
			))

			plain := readMessagesModePlain(t, m, mode)
			for _, body := range []string{"first body", "second body", "third body"} {
				if strings.Count(plain, body) != 1 {
					t.Fatalf("%q rendered %d times, want one:\n%s", body, strings.Count(plain, body), plain)
				}
			}
		})
	}
}

func applyReadMessagesDirectDelivery(t *testing.T, m *Model, mode, turnID, sourceID, sourceName, messageID, text string) *Model {
	t.Helper()
	source := &eventstream.ActorIdentity{Kind: "participant", ID: sourceID, Role: "delegated", Name: sourceName}
	update := eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateUserMessage,
		Content:       eventstream.TextContent{Type: "text", Text: text},
		MessageID:     messageID,
	}
	env := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
		Scope: eventstream.ScopeMain, AgentCommunicationSource: source, Update: update,
	}
	if mode != readMessagesMainMode {
		env = subagentMailboxEnvelope(t, turnID, time.Unix(120, 0), update)
		env.AgentCommunicationSource = source
	}
	return applyACPEnvelopeForTest(t, m, env)
}

func applyReadMessagesSendReturned(t *testing.T, m *Model, mode, turnID, callID, payload string) *Model {
	t.Helper()
	completed := eventstream.ToolStatusCompleted
	updates := []eventstream.Update{
		eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: callID, Title: "SendMessage",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"to": "reviewer", "message": "outgoing"}, Meta: acpToolNameMeta("SendMessage"),
		},
		eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: callID, Status: &completed,
			Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: payload}}},
			Meta:    acpToolNameMeta("SendMessage"),
		},
	}
	for _, update := range updates {
		env := eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
			Scope: eventstream.ScopeMain, Update: update,
		}
		if mode != readMessagesMainMode {
			env = subagentMailboxEnvelope(t, turnID, time.Unix(120, 0), update)
		}
		m = applyACPEnvelopeForTest(t, m, env)
	}
	return m
}

// An unresolvable pane address is not repaired from the tool Title. A
// child-scope read whose Spawn owner is not mounted shows nothing, even though
// the Title names the collaboration read tool.
func TestReadMessagesUnresolvedPaneDoesNotGuessFromTitle(t *testing.T) {
	m := readMessagesTestModel()
	payload := readMessagesPayload(readMessagesMail{ID: "mail-x", From: "reviewer", To: "zuri", Message: "unresolved-body"})
	env := subagentMailboxEnvelope(t, "turn-1", time.Unix(120, 0), readMessagesUpdates("read-x", payload)[1])
	env.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-missing", ToolName: surfaceToolSpawn}
	m = applyACPEnvelopeForTest(t, m, env)

	m.syncViewportContent()
	if plain := strings.Join(m.viewportPlainLines, "\n"); strings.Contains(plain, "unresolved-body") {
		t.Fatalf("unresolved pane rendered a titled read:\n%s", plain)
	}
	if view := m.subagentOutputViews["spawn-missing"]; view != nil {
		for _, block := range view.document.Blocks() {
			if participant, ok := block.(*ParticipantTurnBlock); ok {
				for _, event := range participant.Events {
					if event.Kind == SEAgentCommunication {
						t.Fatalf("unresolved pane mounted %#v", event)
					}
				}
			}
		}
	}
}

// Earlier history is built independently. A mail already mounted in the live
// document must not be re-added from an older page, while a new older mail is.
func TestPrependHistoryDocumentOmitsAlreadyMountedMail(t *testing.T) {
	m := readMessagesTestModel()
	ctx := m.blockRenderContext(120)

	current := NewDocument()
	currentBlock := NewParticipantTurnBlock("turn-2", "zuri")
	currentBlock.Events = append(currentBlock.Events, SubagentEvent{Kind: SEAgentCommunication, Text: "current body", MessageID: "shared-mail"})
	current.Append(currentBlock)

	older := NewDocument()
	olderBlock := NewParticipantTurnBlock("turn-1", "zuri")
	olderBlock.Events = append(olderBlock.Events,
		SubagentEvent{Kind: SEAgentCommunication, Text: "older duplicate", MessageID: "shared-mail"},
		SubagentEvent{Kind: SEAgentCommunication, Text: "older unique", MessageID: "older-mail"},
	)
	older.Append(olderBlock)

	merged := prependHistoryDocument(older, current)
	var plain strings.Builder
	for _, block := range merged.Blocks() {
		plain.WriteString(strings.Join(renderedPlainRows(block.Render(ctx)), "\n"))
		plain.WriteString("\n")
	}
	rendered := plain.String()
	assertReadMessagesHidden(t, rendered, "older duplicate")
	assertReadMessagesShown(t, rendered, "current body")
	assertReadMessagesShown(t, rendered, "older unique")
}

func clickSubagentOutputFoldRowForTest(t *testing.T, m *Model, token string) *Model {
	t.Helper()
	if m.subagentOutputOverlay == nil {
		t.Fatal("subagent output overlay is not open")
	}
	geometry := m.subagentOutputOverlay.geometry
	rowIndex := -1
	for index, rowToken := range geometry.rowTokens {
		if rowToken == token {
			rowIndex = index
			break
		}
	}
	if rowIndex < 0 {
		t.Fatalf("visible overlay rows omitted fold token %q: %#v", token, geometry.rowTokens)
	}
	mouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.x + maxInt(1, geometry.contentWidth/2),
		Y:      geometry.contentY + rowIndex,
	}
	next, _ := m.handleMouse(tea.MouseClickMsg(mouse))
	m = next.(*Model)
	next, _ = m.handleMouse(tea.MouseReleaseMsg(mouse))
	return next.(*Model)
}
