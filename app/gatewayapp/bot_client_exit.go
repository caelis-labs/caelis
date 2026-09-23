package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (b *controlCommandBackend) exitBotClient(ctx context.Context, p appserver.Principal, req appserver.BotClientExitRequest) (appserver.CommandResult, error) {
	b.botWorkAdmissionMu.Lock()
	defer b.botWorkAdmissionMu.Unlock()
	if p.ClientID == "" || p.BotID != req.BotID {
		return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeRejected, appserver.ErrUnauthorized)
	}
	store := b.composition.authorities.botWork
	client, err := store.ActiveClient(ctx, p.ID, p.BotID, p.ClientID)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	if client.ActivationID != req.ActivationID {
		return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("bot: client activation is stale"))
	}
	if err := store.ExitClient(ctx, client); err != nil {
		return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	stopped := []string{}
	uncertain := []string{}
	if req.CancelOwnedWork {
		work, err := store.ListWork(ctx, p.ID, p.BotID)
		if err != nil {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
		}
		for _, item := range work {
			if item.ClientID != p.ClientID {
				continue
			}
			if item.Execution.InstanceID != store.InstanceID {
				if item.Status == "unknown" {
					uncertain = append(uncertain, item.ID)
				}
				continue
			}
			runtime, release, err := b.runtimeRegistry().acquireLoadedRuntime(item.SessionID)
			if err != nil {
				uncertain = append(uncertain, item.ID)
				continue
			}
			if runtime == nil {
				continue
			}
			gw := runtime.instance.currentGateway()
			if active, ok := gw.ActiveTurn(item.SessionID); ok {
				if active.HandleID != item.Execution.HandleID || active.RunID != item.Execution.RunID || active.TurnID != item.Execution.TurnID {
					uncertain = append(uncertain, item.ID)
				} else {
					err := gw.Interrupt(ctx, kernel.InterruptRequest{SessionRef: active.SessionRef, HandleID: active.HandleID, RunID: active.RunID, TurnID: active.TurnID, Reason: "owning Bot client exited"})
					if err != nil {
						uncertain = append(uncertain, item.ID)
					} else {
						stopped = append(stopped, item.ID)
					}
				}
			}
			release()
		}
	}
	detail, _ := json.Marshal(struct {
		Stopped []string `json:"stopped_work"`
		Unknown []string `json:"unknown_work"`
	}{stopped, uncertain})
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, SessionID: req.SessionID, Resource: &appserver.CommandResource{Kind: "bot_client", Ref: client.ID}, Detail: string(detail)}, nil
}
