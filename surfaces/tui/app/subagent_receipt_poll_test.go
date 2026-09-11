package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
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
