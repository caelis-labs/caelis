package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type receiptPollTestClient struct {
	*paneTestClient
	queries [][]string
	state   string
	err     error
}

func (c *receiptPollTestClient) SubagentInputStatuses(_ context.Context, req appserver.SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error) {
	c.queries = append(c.queries, slices.Clone(req.IDs))
	var statuses []collaboration.UserInputStatus
	for _, id := range req.IDs {
		statuses = append(statuses, collaboration.UserInputStatus{ID: id, State: c.state})
	}
	return statuses, c.err
}

func TestPaneReceiptsShareOneTimerAndInFlightQuery(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client
	state := m.subagentOutputOverlay
	submit := func(i int) {
		t.Helper()
		state.editor.SetValue(fmt.Sprintf("input %d", i))
		send := m.submitPanePrompt()
		if send == nil {
			t.Fatal("input was not submitted")
		}
		_, next := m.updateSubagentWorkspace(send())
		if (next != nil) != (i == 0) {
			t.Fatalf("input %d started a duplicate poll or failed to start the first", i)
		}
	}
	for i := range 16 {
		submit(i)
	}
	tick := paneInputTickMsg{sessionID: m.currentSessionID, callID: state.callID, poll: state.receiptPoll}
	_, query := m.updateSubagentWorkspace(tick)
	if query == nil {
		t.Fatal("timer did not start a query")
	}
	_, duplicate := m.updateSubagentWorkspace(tick)
	if duplicate != nil {
		t.Fatal("duplicate tick started another query")
	}
	submit(16)
	result := query()
	if len(client.queries) != 1 || len(client.queries[0]) != 16 {
		t.Fatalf("first cycle queried %v", client.queries)
	}
	_, timer := m.updateSubagentWorkspace(result)
	if timer == nil {
		t.Fatal("queued receipts did not schedule one successor")
	}
	if _, next := m.updateSubagentWorkspace(result); next != nil {
		t.Fatal("duplicate result restarted the poll")
	}
	client.state = "sent"
	_, query = m.updateSubagentWorkspace(paneInputTickMsg{sessionID: m.currentSessionID, callID: state.callID, poll: state.receiptPoll})
	_, next := m.updateSubagentWorkspace(query())
	if next != nil || state.receiptPoll != nil || len(state.receipts) != 0 {
		t.Fatal("completed receipts kept polling")
	}
	if len(client.queries) != 2 || len(client.queries[1]) != 17 {
		t.Fatalf("second cycle did not query the updated set once: %v", client.queries)
	}
}

func TestPaneReceiptPollFencesOldSessionAndRetainedPane(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "sent"}
	m.cfg.SubagentInputs = client
	old := m.subagentOutputOverlay
	old.receipts = []string{"old"}
	query := m.schedulePaneInputPoll(m.currentSessionID, old.callID, true)
	result := query()
	tick := paneInputTickMsg{sessionID: m.currentSessionID, callID: old.callID, poll: old.receiptPoll}
	m.currentSessionID = "another-session"
	if _, cmd := m.updateSubagentWorkspace(tick); cmd != nil {
		t.Fatal("old Session tick queried the new Session")
	}
	if _, cmd := m.updateSubagentWorkspace(result); cmd != nil || len(old.receipts) != 1 {
		t.Fatal("old Session result changed receipts")
	}
	// Returning to the same Session can recreate the same call ID; the old
	// request must still be unable to update that replacement pane.
	m.currentSessionID = tick.sessionID
	fresh := &subagentOutputOverlayState{callID: old.callID, receipts: []string{"fresh"}}
	m.subagentOutputViews[old.callID].pane = fresh
	if _, cmd := m.updateSubagentWorkspace(tick); cmd != nil {
		t.Fatal("old pane tick was resurrected")
	}
	if _, cmd := m.updateSubagentWorkspace(result); cmd != nil || fresh.inputStatus != "" || len(fresh.receipts) != 1 {
		t.Fatal("old pane result affected the replacement")
	}
	client.err = errors.New("unavailable")
	query = m.schedulePaneInputPoll(m.currentSessionID, fresh.callID, true)
	if _, cmd := m.updateSubagentWorkspace(query()); cmd != nil || fresh.receiptPoll != nil || len(fresh.receipts) != 1 {
		t.Fatal("failed receipt query retried or discarded an unconfirmed input")
	}
}

func TestSlashExecutionResultToPanePollAndRenderEvidence(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client

	var sentMsgs []tea.Msg
	sender := &ProgramSender{Send: func(msg tea.Msg) {
		sentMsgs = append(sentMsgs, msg)
	}}
	_, generation := sender.replaceSessionView(context.Background(), m.currentSessionID)
	m.viewGeneration = generation
	m.cfg.ProgramSender = sender

	result := controlprompt.Result{
		Handled: true,
		ParticipantTask: &controlprompt.AgentRunResult{
			SessionID: m.currentSessionID,
			TaskID:    "task-omar",
			InputReceipt: &collaboration.UserInputStatus{
				ID:    "rcpt-1",
				State: "queued",
			},
		},
	}

	res := executeControlPromptResult(context.Background(), nil, sender, result)
	if res.completion.Err != nil {
		t.Fatalf("unexpected completion error: %v", res.completion.Err)
	}
	if len(sentMsgs) == 0 {
		t.Fatal("executeControlPromptResult did not send any messages")
	}

	for _, msg := range sentMsgs {
		m.Update(msg)
	}

	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != "spawn-omar" {
		t.Fatalf("pane for task-omar was not opened, got %v", m.subagentOutputOverlay)
	}
	state := m.subagentOutputOverlay
	if !slices.Contains(state.receipts, "rcpt-1") {
		t.Fatalf("state.receipts does not contain rcpt-1: %v", state.receipts)
	}
	if state.inputStatus != "Queued · waits for input admission" {
		t.Fatalf("inputStatus = %q, want queued label", state.inputStatus)
	}
	if state.receiptPoll == nil {
		t.Fatal("receipt poll was not scheduled")
	}

	overlayText := subagentOutputOverlayPlain(m)
	if !strings.Contains(overlayText, "Queued · waits for input admission") {
		t.Fatalf("queued status not rendered in overlay:\n%s", overlayText)
	}

	tick := paneInputTickMsg{sessionID: m.currentSessionID, callID: state.callID, poll: state.receiptPoll}
	_, query := m.updateSubagentWorkspace(tick)
	if query == nil {
		t.Fatal("tick did not start query")
	}
	pollMsg := query()
	if len(client.queries) != 1 || !slices.Contains(client.queries[0], "rcpt-1") {
		t.Fatalf("queried %v, want rcpt-1", client.queries)
	}
	_, timer := m.updateSubagentWorkspace(pollMsg)
	if timer == nil {
		t.Fatal("queued status did not schedule next timer")
	}

	client.state = "sent"
	tick = paneInputTickMsg{sessionID: m.currentSessionID, callID: state.callID, poll: state.receiptPoll}
	_, query = m.updateSubagentWorkspace(tick)
	if query == nil {
		t.Fatal("second tick did not start query")
	}
	_, nextCmd := m.updateSubagentWorkspace(query())
	if nextCmd != nil {
		t.Fatal("completed receipt scheduled another poll")
	}
	if len(state.receipts) != 0 {
		t.Fatalf("receipts not cleared after sent: %v", state.receipts)
	}
	if state.inputStatus != "Sent" {
		t.Fatalf("inputStatus = %q, want Sent", state.inputStatus)
	}
	overlayText = subagentOutputOverlayPlain(m)
	if !strings.Contains(overlayText, "Sent") {
		t.Fatalf("sent status not rendered in overlay:\n%s", overlayText)
	}
}

func TestSlashExecutionLateDirectoryReceiptPreservedAndPolled(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client

	var sentMsgs []tea.Msg
	sender := &ProgramSender{Send: func(msg tea.Msg) {
		sentMsgs = append(sentMsgs, msg)
	}}
	_, generation := sender.replaceSessionView(context.Background(), m.currentSessionID)
	m.viewGeneration = generation
	m.cfg.ProgramSender = sender

	result := controlprompt.Result{
		Handled: true,
		ParticipantTask: &controlprompt.AgentRunResult{
			SessionID: m.currentSessionID,
			TaskID:    "task-late",
			InputReceipt: &collaboration.UserInputStatus{
				ID:    "rcpt-late-1",
				State: "queued",
			},
		},
	}
	executeControlPromptResult(context.Background(), nil, sender, result)

	m.subagentOutputOverlay = nil

	for _, msg := range sentMsgs {
		m.Update(msg)
	}

	if m.subagentOutputOverlay != nil {
		t.Fatal("focus opened a speculative child pane before directory arrived")
	}
	if m.subagentFocusTaskID != "task-late" {
		t.Fatalf("subagentFocusTaskID = %q, want task-late", m.subagentFocusTaskID)
	}
	pending := m.subagentPendingReceipts["task-late"]
	if len(pending) != 1 || pending[0].ID != "rcpt-late-1" {
		t.Fatalf("subagentPendingReceipts = %v, want rcpt-late-1 preserved", pending)
	}

	descriptor := taskstream.TaskDescriptor{
		SessionID:     m.currentSessionID,
		TaskID:        "task-late",
		ParticipantID: "participant-late",
		Handle:        "late-worker",
		Kind:          task.KindSubagent,
		Running:       true,
		Model:         "test-model",
	}
	applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{descriptor})

	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != "task:task-late" {
		t.Fatalf("child pane was not opened upon late directory arrival: %v", m.subagentOutputOverlay)
	}
	state := m.subagentOutputOverlay
	if !slices.Contains(state.receipts, "rcpt-late-1") {
		t.Fatalf("state.receipts does not contain rcpt-late-1: %v", state.receipts)
	}
	if state.inputStatus != "Queued · waits for input admission" {
		t.Fatalf("inputStatus = %q, want Queued", state.inputStatus)
	}
	if state.receiptPoll == nil {
		t.Fatal("receipt poll was not started upon late directory arrival")
	}

	if len(m.subagentPendingReceipts["task-late"]) != 0 {
		t.Fatalf("pending receipts not drained: %v", m.subagentPendingReceipts["task-late"])
	}
}

func TestPaneReceiptRenderFailureEvidenceAndUnknownNotRetried(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client

	state := m.subagentOutputOverlay
	state.receipts = []string{"rcpt-fail"}
	state.inputStatus = "Queued · waits for input admission"
	m.schedulePaneInputPoll(m.currentSessionID, state.callID, true)

	pollMsg := paneInputPollMsg{
		sessionID: m.currentSessionID,
		callID:    state.callID,
		poll:      state.receiptPoll,
		statuses: []collaboration.UserInputStatus{{
			ID:     "rcpt-fail",
			State:  "failed",
			Detail: "agent process died unexpectedly",
		}},
	}
	_, cmd := m.updateSubagentWorkspace(pollMsg)
	if cmd != nil {
		t.Fatal("failed receipt scheduled a retry")
	}
	if len(state.receipts) != 0 {
		t.Fatalf("failed receipt was not removed: %v", state.receipts)
	}
	wantFailNotice := "Not sent · agent process died unexpectedly"
	if state.inputStatus != wantFailNotice {
		t.Fatalf("inputStatus = %q, want %q", state.inputStatus, wantFailNotice)
	}
	overlay := subagentOutputOverlayPlain(m)
	if !strings.Contains(overlay, wantFailNotice) {
		t.Fatalf("failure evidence missing from overlay render:\n%s", overlay)
	}

	state.receipts = []string{"rcpt-unknown"}
	m.schedulePaneInputPoll(m.currentSessionID, state.callID, true)
	unknownPollMsg := paneInputPollMsg{
		sessionID: m.currentSessionID,
		callID:    state.callID,
		poll:      state.receiptPoll,
		statuses: []collaboration.UserInputStatus{{
			ID:     "rcpt-unknown",
			State:  "unknown",
			Detail: "host network split",
		}},
	}
	_, next := m.updateSubagentWorkspace(unknownPollMsg)
	if next != nil {
		t.Fatal("unknown receipt scheduled an automatic retry")
	}
	if len(state.receipts) != 0 {
		t.Fatalf("unknown receipt was not removed: %v", state.receipts)
	}
	wantUnknownNotice := "Delivery unconfirmed · not retried · host network split"
	if state.inputStatus != wantUnknownNotice {
		t.Fatalf("inputStatus = %q, want %q", state.inputStatus, wantUnknownNotice)
	}
	overlay = subagentOutputOverlayPlain(m)
	if !strings.Contains(overlay, wantUnknownNotice) {
		t.Fatalf("unknown evidence missing from overlay render:\n%s", overlay)
	}
}

func TestMultipleConcurrentReceiptsFollowUp(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client

	state := m.subagentOutputOverlay
	m.focusParticipantTask(participantTaskFocusMsg{
		sessionID: m.currentSessionID,
		taskID:    "task-breeze",
		receipt:   &collaboration.UserInputStatus{ID: "rcpt-c1", State: "queued"},
	})
	m.focusParticipantTask(participantTaskFocusMsg{
		sessionID: m.currentSessionID,
		taskID:    "task-breeze",
		receipt:   &collaboration.UserInputStatus{ID: "rcpt-c2", State: "queued"},
	})

	if len(state.receipts) != 2 || state.receipts[0] != "rcpt-c1" || state.receipts[1] != "rcpt-c2" {
		t.Fatalf("state.receipts = %v, want [rcpt-c1, rcpt-c2]", state.receipts)
	}
	if state.receiptPoll == nil {
		t.Fatal("poll timer was not set")
	}

	state.receiptPoll.querying = true
	pollMsg := paneInputPollMsg{
		sessionID: m.currentSessionID,
		callID:    state.callID,
		poll:      state.receiptPoll,
		statuses: []collaboration.UserInputStatus{
			{ID: "rcpt-c1", State: "sent"},
			{ID: "rcpt-c2", State: "queued"},
		},
	}
	_, nextCmd := m.updateSubagentWorkspace(pollMsg)
	if nextCmd == nil {
		t.Fatal("queued rcpt-c2 should schedule successor query")
	}
	if len(state.receipts) != 1 || state.receipts[0] != "rcpt-c2" {
		t.Fatalf("state.receipts = %v, want only rcpt-c2", state.receipts)
	}

	state.receiptPoll.querying = true
	pollMsg2 := paneInputPollMsg{
		sessionID: m.currentSessionID,
		callID:    state.callID,
		poll:      state.receiptPoll,
		statuses: []collaboration.UserInputStatus{
			{ID: "rcpt-c2", State: "sent"},
		},
	}
	_, finalCmd := m.updateSubagentWorkspace(pollMsg2)
	if finalCmd != nil {
		t.Fatal("completed receipts should not schedule further poll")
	}
	if len(state.receipts) != 0 {
		t.Fatalf("state.receipts not empty: %v", state.receipts)
	}
	if state.inputStatus != "Sent" {
		t.Fatalf("inputStatus = %q, want Sent", state.inputStatus)
	}
}

func TestParticipantFocusStaleSessionFenced(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.currentSessionID = "session-active"

	cmd := m.focusParticipantTask(participantTaskFocusMsg{
		sessionID: "session-old",
		taskID:    "task-breeze",
		receipt:   &collaboration.UserInputStatus{ID: "rcpt-old", State: "queued"},
	})
	if cmd != nil {
		t.Fatal("focusParticipantTask returned command for stale session")
	}
	if len(m.subagentPendingReceipts) != 0 {
		t.Fatalf("stale receipt was enqueued: %v", m.subagentPendingReceipts)
	}

	m.subagentPendingReceipts = map[string][]collaboration.UserInputStatus{
		"task-x": {{ID: "rcpt-x", State: "queued"}},
	}
	m.resetSubagentDirectoryWatch()
	if m.subagentPendingReceipts != nil {
		t.Fatalf("resetSubagentDirectoryWatch did not clear pending receipts: %v", m.subagentPendingReceipts)
	}
}

func TestLateDirectoryMultipleTasksPendingReceiptsDrained(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client

	// Start without any active pane
	m.subagentOutputOverlay = nil

	// Two tasks receive receipts before directory snapshot arrives
	m.focusParticipantTask(participantTaskFocusMsg{
		sessionID: m.currentSessionID,
		taskID:    "task-early-1",
		receipt:   &collaboration.UserInputStatus{ID: "rcpt-e1", State: "queued"},
	})
	m.focusParticipantTask(participantTaskFocusMsg{
		sessionID: m.currentSessionID,
		taskID:    "task-early-2",
		receipt:   &collaboration.UserInputStatus{ID: "rcpt-e2", State: "queued"},
	})

	if len(m.subagentPendingReceipts) != 2 {
		t.Fatalf("expected 2 pending tasks, got %d", len(m.subagentPendingReceipts))
	}
	if m.subagentOutputOverlay != nil {
		t.Fatal("child pane opened before directory snapshot")
	}

	// Now directory snapshot arrives discovering BOTH tasks
	d1 := taskstream.TaskDescriptor{
		SessionID:     m.currentSessionID,
		TaskID:        "task-early-1",
		ParticipantID: "part-1",
		Handle:        "worker-1",
		Kind:          task.KindSubagent,
		Running:       true,
	}
	d2 := taskstream.TaskDescriptor{
		SessionID:     m.currentSessionID,
		TaskID:        "task-early-2",
		ParticipantID: "part-2",
		Handle:        "worker-2",
		Kind:          task.KindSubagent,
		Running:       true,
	}
	m.sessionDrafts = map[string]sessionDraft{m.currentSessionID: {children: map[string]sessionComposerDraft{
		"task:task-early-1": {text: "retained child draft"},
	}}}
	applySubagentDirectorySnapshotForTest(m, 1, []taskstream.TaskDescriptor{d1, d2})

	// The last-focused task (task-early-2) has the active overlay
	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != "task:task-early-2" {
		t.Fatalf("overlay not open on task-early-2: %v", m.subagentOutputOverlay)
	}
	state2 := m.subagentOutputOverlay
	if !slices.Contains(state2.receipts, "rcpt-e2") {
		t.Fatalf("task-early-2 missing rcpt-e2: %v", state2.receipts)
	}
	if state2.receiptPoll == nil {
		t.Fatal("task-early-2 poll not scheduled")
	}

	// Earlier task (task-early-1) has its pane state constructed and receipts attached for background polling
	view1 := m.subagentOutputViews["task:task-early-1"]
	if view1 == nil || view1.pane == nil {
		t.Fatal("task-early-1 pane not created for pending receipt drain")
	}
	state1 := view1.pane
	if !state1.editorReady || state1.editor.Value() != "retained child draft" {
		t.Fatal("background receipt pane lost the retained draft")
	}
	if !slices.Contains(state1.receipts, "rcpt-e1") {
		t.Fatalf("task-early-1 missing rcpt-e1: %v", state1.receipts)
	}
	if state1.receiptPoll == nil {
		t.Fatal("task-early-1 poll not scheduled")
	}

	// Pending receipts map must be completely drained
	if len(m.subagentPendingReceipts) != 0 {
		t.Fatalf("pending receipts not empty: %v", m.subagentPendingReceipts)
	}
}

func TestReceiptsExceed64BatchedWithoutDropping(t *testing.T) {
	m, base := newPaneTestModel(t)
	client := &receiptPollTestClient{paneTestClient: base, state: "queued"}
	m.cfg.SubagentInputs = client

	state := m.subagentOutputOverlay
	for i := 0; i < 70; i++ {
		state.receipts = append(state.receipts, fmt.Sprintf("rcpt-%02d", i))
	}
	if len(state.receipts) < 70 {
		t.Fatalf("setup failed, receipts = %d", len(state.receipts))
	}

	query := m.schedulePaneInputPoll(m.currentSessionID, state.callID, true)
	if query == nil {
		t.Fatal("immediate poll did not return query")
	}
	_ = query()

	// Every receipt is checked even when the first batch remains queued.
	if len(client.queries) != 2 || len(client.queries[0]) != 64 || len(client.queries[1]) != 6 {
		t.Fatalf("expected 64+6 IDs in one poll cycle, got %v", client.queries)
	}

	// None of the 70 receipts were dropped from state.receipts
	if len(state.receipts) != 70 {
		t.Fatalf("receipts were dropped: len = %d, want 70", len(state.receipts))
	}
}
