package tuiapp

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/google/uuid"
)

type paneInputResultMsg struct {
	sessionID, callID, id string
	status                collaboration.UserInputStatus
	err                   error
}
type paneInputPollMsg struct {
	sessionID, callID string
	poll              *paneReceiptPoll
	statuses          []collaboration.UserInputStatus
	err               error
}
type paneInputTickMsg struct {
	sessionID, callID string
	poll              *paneReceiptPoll
}

// One retained pane owns one timer or request. Its identity also fences late
// results from a prior Session, even if the same call ID is opened again.
type paneReceiptPoll struct{ querying bool }

func (m *Model) updateSubagentWorkspace(msg tea.Msg) (bool, tea.Cmd) {
	switch value := msg.(type) {
	case paneInputResultMsg:
		if value.sessionID != m.currentSessionID {
			return true, nil
		}
		view := m.subagentOutputViews[value.callID]
		if view == nil || view.pane == nil {
			return true, nil
		}
		state := view.pane
		if value.err != nil {
			switch errorcode.CodeOf(value.err) {
			case errorcode.InvalidArgument, errorcode.PermissionDenied, errorcode.NotFound, errorcode.Conflict, errorcode.Unsupported, errorcode.FailedPrecondition:
				state.inputStatus = "Not sent · " + value.err.Error() + " · ↑ Recall"
				state.receipts = removePaneReceipt(state.receipts, value.id)
				return true, nil
			}
			state.inputStatus = "Delivery unconfirmed · check receipt"
			return true, m.schedulePaneInputPoll(value.sessionID, value.callID, true)
		}
		state.inputStatus = paneReceiptLabel(value.status)
		if value.status.State == "queued" || value.status.State == "sending" {
			return true, m.schedulePaneInputPoll(value.sessionID, value.callID, false)
		}
		state.receipts = removePaneReceipt(state.receipts, value.id)
		return true, nil
	case paneInputTickMsg:
		return true, m.pollPaneInputs(value)
	case paneInputPollMsg:
		if value.sessionID != m.currentSessionID {
			return true, nil
		}
		view := m.subagentOutputViews[value.callID]
		if view == nil || view.pane == nil {
			return true, nil
		}
		state := view.pane
		if value.poll == nil || state.receiptPoll != value.poll || !value.poll.querying {
			return true, nil
		}
		state.receiptPoll = nil
		if value.err != nil {
			state.inputStatus = "Receipt unavailable · not retried"
			return true, nil
		}
		for _, receipt := range value.statuses {
			state.inputStatus = paneReceiptLabel(receipt)
			if receipt.State != "queued" && receipt.State != "sending" {
				state.receipts = removePaneReceipt(state.receipts, receipt.ID)
			}
		}
		return true, m.schedulePaneInputPoll(value.sessionID, value.callID, false)
	}
	return false, nil
}
func removePaneReceipt(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}
func paneReceiptLabel(status collaboration.UserInputStatus) string {
	labels := map[string]string{"queued": "Queued · waits for input admission", "sending": "Sending…", "sent": "Sent", "failed": "Not sent", "unknown": "Delivery unconfirmed · not retried"}
	label := labels[status.State]
	if label == "" {
		label = status.State
	}
	if status.Detail != "" {
		label += " · " + status.Detail
	}
	return label
}
func (m *Model) schedulePaneInputPoll(sessionID, callID string, immediate bool) tea.Cmd {
	if sessionID != m.currentSessionID || m.cfg.SubagentInputs == nil {
		return nil
	}
	view := m.subagentOutputViews[callID]
	if view == nil || view.pane == nil || len(view.pane.receipts) == 0 || view.pane.receiptPoll != nil {
		return nil
	}
	poll := &paneReceiptPoll{}
	view.pane.receiptPoll = poll
	tick := paneInputTickMsg{sessionID: sessionID, callID: callID, poll: poll}
	if immediate {
		return m.pollPaneInputs(tick)
	}
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tick })
}

func (m *Model) pollPaneInputs(tick paneInputTickMsg) tea.Cmd {
	if tick.sessionID != m.currentSessionID || m.cfg.SubagentInputs == nil {
		return nil
	}
	view := m.subagentOutputViews[tick.callID]
	if view == nil || view.pane == nil || tick.poll == nil || view.pane.receiptPoll != tick.poll || tick.poll.querying {
		return nil
	}
	if len(view.pane.receipts) == 0 {
		view.pane.receiptPoll = nil
		return nil
	}
	tick.poll.querying = true
	ids := append([]string(nil), view.pane.receipts...)
	client := m.cfg.SubagentInputs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		statuses, err := client.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: tick.sessionID, IDs: ids})
		return paneInputPollMsg{sessionID: tick.sessionID, callID: tick.callID, poll: tick.poll, statuses: statuses, err: err}
	}
}
func (m *Model) submitPanePrompt() tea.Cmd {
	state := m.subagentOutputOverlay
	if state == nil {
		return nil
	}
	line, tokens := submissionInput(state.editor.Value(), state.attachments)
	text, images := expandPastesRemapImages(line, tokens)
	if text == "" && len(images) == 0 {
		return nil
	}
	if len(text) > 65536 {
		state.inputStatus = "Prompt exceeds 64 KiB"
		return nil
	}
	descriptor, ok := m.subagentRosterTasks[state.callID]
	if !ok || descriptor.TaskID == "" || descriptor.ParticipantID == "" || m.cfg.SubagentInputs == nil {
		state.inputStatus = "Input unavailable · waiting for participant connection"
		return nil
	}
	if len(state.receipts) >= 64 {
		state.inputStatus = "Wait for pending inputs"
		return nil
	}
	id := uuid.NewString()
	req := appserver.SubagentInputRequest{SessionID: m.currentSessionID, OperationID: id, ParticipantID: descriptor.ParticipantID, TaskID: descriptor.TaskID, Input: text}
	attachments := make([]controlprompt.Attachment, 0, len(images))
	for _, item := range images {
		attachments = append(attachments, controlprompt.Attachment{Name: item.Name, Offset: item.Offset})
	}
	workspace := m.cfg.Workspace
	callID, client := state.callID, m.cfg.SubagentInputs
	state.history = append(state.history, state.editor.Value())
	state.historyAttachments = append(state.historyAttachments, cloneInputAttachments(state.attachments))
	state.historyIndex = -1
	state.editor.Reset()
	state.attachments = nil
	state.editorOffset = 0
	state.inputStatus = "Sending…"
	state.receipts = append(state.receipts, id)
	return func() tea.Msg {
		parts, err := appserveradapter.ContentPartsFromSubmission(text, attachments, workspace)
		if err != nil {
			return paneInputResultMsg{sessionID: req.SessionID, callID: callID, id: id, err: errorcode.Wrap(errorcode.InvalidArgument, "Read image", err)}
		}
		req.ContentParts = parts
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		status, err := client.SubmitSubagentInput(ctx, req)
		return paneInputResultMsg{req.SessionID, callID, id, status, err}
	}
}
