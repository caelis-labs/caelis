package acpagentbridge

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type participantInputReceipt struct {
	taskID   string
	status   collaboration.UserInputStatus
	reported bool
}

// The Session observer retains receipt identity beyond the slash prompt's
// lifetime. Only status reads are repeated; input is never submitted here.
func (o *acpParticipantTasks) trackInputReceiptLocked(child controlprompt.AgentRunResult) {
	if child.InputReceipt == nil || child.InputReceipt.ID == "" {
		return
	}
	if o.inputReceipts == nil {
		o.inputReceipts = make(map[string]participantInputReceipt)
	}
	if _, exists := o.inputReceipts[child.InputReceipt.ID]; !exists {
		o.inputReceipts[child.InputReceipt.ID] = participantInputReceipt{taskID: child.TaskID, status: *child.InputReceipt}
	}
}

func (o *acpParticipantTasks) pollInputReceipts(ctx context.Context) error {
	o.mu.Lock()
	pending := make(map[string]participantInputReceipt, len(o.inputReceipts))
	for id, receipt := range o.inputReceipts {
		pending[id] = receipt
	}
	o.mu.Unlock()
	var ids []string
	for id, receipt := range pending {
		if !receipt.reported {
			if err := o.reportInputReceipt(ctx, receipt); err != nil {
				return err
			}
		}
		if receipt.status.State == "queued" || receipt.status.State == "sending" {
			ids = append(ids, id)
		}
	}
	for len(ids) > 0 {
		batch := ids[:min(len(ids), 64)]
		ids = ids[len(batch):]
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var statuses []collaboration.UserInputStatus
		var err error
		if o.agent.subagentInputClient == nil {
			err = fmt.Errorf("input receipt client is unavailable")
		} else {
			statuses, err = o.agent.subagentInputClient.SubagentInputStatuses(queryCtx, appserver.SubagentInputStatusRequest{SessionID: o.sessionID, IDs: batch})
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		byID := make(map[string]collaboration.UserInputStatus, len(statuses))
		for _, status := range statuses {
			byID[status.ID] = status
		}
		for _, id := range batch {
			receipt := pending[id]
			status, found := byID[id]
			if err != nil || !found {
				status = collaboration.UserInputStatus{ID: id, State: "unknown", Detail: "Input receipt is unavailable"}
				if err != nil {
					status.Detail += ": " + err.Error()
				}
			}
			if status != receipt.status {
				receipt.status = status
				if err := o.reportInputReceipt(ctx, receipt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (o *acpParticipantTasks) reportInputReceipt(ctx context.Context, receipt participantInputReceipt) error {
	label := receipt.status.State
	switch receipt.status.State {
	case "queued":
		label = "Queued · waits for input admission"
	case "sending":
		label = "Sending…"
	case "sent":
		label = "Sent"
	case "failed":
		label = "Not sent"
	case "unknown":
		label = "Delivery unconfirmed · not retried"
	}
	if receipt.status.Detail != "" {
		label += " · " + receipt.status.Detail
	}
	if err := emitACPNotice(ctx, o.callbacks, o.sessionID, eventstream.Envelope{
		Kind:   eventstream.KindNotice,
		Notice: fmt.Sprintf("Participant %s input %s: %s", receipt.taskID, receipt.status.ID, label),
	}, "", nil); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if receipt.status.State == "queued" || receipt.status.State == "sending" {
		receipt.reported = true
		o.inputReceipts[receipt.status.ID] = receipt
	} else {
		delete(o.inputReceipts, receipt.status.ID)
	}
	return nil
}
