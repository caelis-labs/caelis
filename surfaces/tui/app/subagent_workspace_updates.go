package tuiapp

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/google/uuid"
)

type panePreferencesMsg struct {
	value    uipreferences.Preferences
	revision uint64
	save     bool
	err      error
}
type paneInputResultMsg struct {
	sessionID, callID, id string
	status                collaboration.UserInputStatus
	err                   error
}
type paneInputPollMsg struct {
	sessionID, callID string
	statuses          []collaboration.UserInputStatus
	err               error
}
type paneInputTickMsg struct{ sessionID, callID string }

func (m *Model) loadPanePreferences() tea.Cmd {
	client := m.cfg.UIPreferences
	if client == nil {
		return nil
	}
	revision := m.workspace.preferenceRevision
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, err := client.LoadUIPreferences(ctx)
		return panePreferencesMsg{value: p, revision: revision, err: err}
	}
}
func (m *Model) savePanePreferences() tea.Cmd {
	if m.workspace.saving || m.cfg.UIPreferences == nil {
		return nil
	}
	m.workspace.saving = true
	p, revision, client := m.workspace.preferences.WithDefaults(), m.workspace.preferenceRevision, m.cfg.UIPreferences
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := client.SaveUIPreferences(ctx, p)
		return panePreferencesMsg{value: p, revision: revision, save: true, err: err}
	}
}

func (m *Model) updateSubagentWorkspace(msg tea.Msg) (bool, tea.Cmd) {
	switch value := msg.(type) {
	case panePreferencesMsg:
		if value.save {
			m.workspace.saving = false
			if value.revision != m.workspace.preferenceRevision {
				return true, m.savePanePreferences()
			}
		} else if value.err == nil && value.revision == m.workspace.preferenceRevision && !m.workspace.resizing && !m.workspace.dragging {
			if err := value.value.Validate(); err != nil {
				value.err = err
			} else {
				m.workspace.preferences = value.value.WithDefaults()
				m.resizeWorkspace()
			}
		}
		if value.err != nil {
			if state := m.subagentOutputOverlay; state != nil {
				state.inputStatus = "Layout not saved: " + value.err.Error()
			}
			return true, m.showHint("Layout preferences: "+value.err.Error(), hintOptions{priority: HintPriorityHigh, clearOnMessage: true})
		}
		return true, nil
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
			return true, m.pollPaneInputs(value.sessionID, value.callID)
		}
		state.inputStatus = paneReceiptLabel(value.status)
		if value.status.State == "queued" || value.status.State == "sending" {
			return true, paneInputTick(value.sessionID, value.callID)
		}
		state.receipts = removePaneReceipt(state.receipts, value.id)
		return true, nil
	case paneInputTickMsg:
		return true, m.pollPaneInputs(value.sessionID, value.callID)
	case paneInputPollMsg:
		if value.sessionID != m.currentSessionID {
			return true, nil
		}
		view := m.subagentOutputViews[value.callID]
		if view == nil || view.pane == nil {
			return true, nil
		}
		state := view.pane
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
		if len(state.receipts) > 0 {
			return true, paneInputTick(value.sessionID, value.callID)
		}
		return true, nil
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
func paneInputTick(sessionID, callID string) tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return paneInputTickMsg{sessionID, callID} })
}
func (m *Model) pollPaneInputs(sessionID, callID string) tea.Cmd {
	if sessionID != m.currentSessionID || m.cfg.SubagentInputs == nil {
		return nil
	}
	view := m.subagentOutputViews[callID]
	if view == nil || view.pane == nil || len(view.pane.receipts) == 0 {
		return nil
	}
	ids := append([]string(nil), view.pane.receipts...)
	client := m.cfg.SubagentInputs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		statuses, err := client.SubagentInputStatuses(ctx, appserver.SubagentInputStatusRequest{SessionID: sessionID, IDs: ids})
		return paneInputPollMsg{sessionID, callID, statuses, err}
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
		state.inputStatus = "Input unavailable · waiting for child connection"
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
