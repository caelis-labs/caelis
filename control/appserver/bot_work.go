package appserver

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/bot"
)

const (
	ActionBotWorkCreate    Action = "bot.work.create"
	ActionBotWorkContinue  Action = "bot.work.continue"
	ActionBotWorkSteer     Action = "bot.work.steer"
	ActionBotCompletionAck Action = "bot.completion.acknowledge"
	ActionBotWorkCancel    Action = "bot.work.cancel"
)

// BotWorkRequest addresses the Bot conversation in WriteBase.SessionID and a
// separate durable WorkID. SourceID references a persisted authenticated prompt.
// Target is required for steering/cancellation and includes the Host instance.
type BotWorkRequest struct {
	WriteBase
	BotID      string        `json:"bot_id"`
	WorkID     string        `json:"work_id,omitempty"`
	SourceID   string        `json:"source_id,omitempty"`
	Assignment string        `json:"assignment,omitempty"`
	Target     bot.Execution `json:"target,omitzero"`
}

// BotWorkCommands uses the shared durable Control command ledger.
type BotWorkCommands interface {
	FireBotReminder(context.Context, Principal, BotReminderRequest) (CommandResult, error)
	ExitBotClient(context.Context, Principal, BotClientExitRequest) (CommandResult, error)
	AcknowledgeBotCompletion(context.Context, Principal, BotWorkRequest) (CommandResult, error)
	CreateBotWork(context.Context, Principal, BotWorkRequest) (CommandResult, error)
	ContinueBotWork(context.Context, Principal, BotWorkRequest) (CommandResult, error)
	SteerBotWork(context.Context, Principal, BotWorkRequest) (CommandResult, error)
	CancelBotWork(context.Context, Principal, BotWorkRequest) (CommandResult, error)
}

func (s *CommandService) CreateBotWork(ctx context.Context, p Principal, r BotWorkRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotWorkCreate, r.WriteBase, "bot/"+r.BotID+"/work/create", r)
}
func (s *CommandService) ContinueBotWork(ctx context.Context, p Principal, r BotWorkRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotWorkContinue, r.WriteBase, "bot/"+r.BotID+"/work/"+r.WorkID, r)
}
func (s *CommandService) SteerBotWork(ctx context.Context, p Principal, r BotWorkRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotWorkSteer, r.WriteBase, "bot/"+r.BotID+"/work/"+r.WorkID, r)
}
func (s *CommandService) CancelBotWork(ctx context.Context, p Principal, r BotWorkRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotWorkCancel, r.WriteBase, "bot/"+r.BotID+"/work/"+r.WorkID, r)
}

func validateBotWorkRequest(action Action, req BotWorkRequest) error {
	if err := requireSession(req.SessionID); err != nil {
		return err
	}
	sessionID, err := bot.ConversationID(req.BotID)
	if err != nil || sessionID != req.SessionID {
		return errors.New("controlclient: Bot and conversation do not match")
	}
	switch action {
	case ActionBotCompletionAck:
		if req.WorkID == "" {
			return errors.New("controlclient: completion id is required")
		}
		return nil
	case ActionBotWorkCreate:
		if req.WorkID != "" || req.Target != (bot.Execution{}) {
			return errors.New("controlclient: work create must not address existing work")
		}
	case ActionBotWorkContinue:
		if req.WorkID == "" || req.Target != (bot.Execution{}) {
			return errors.New("controlclient: continue requires work handle without an active execution target")
		}
	case ActionBotWorkSteer, ActionBotWorkCancel:
		if req.WorkID == "" || req.Target.InstanceID == "" || req.Target.SessionID == "" || req.Target.HandleID == "" || req.Target.RunID == "" || req.Target.TurnID == "" {
			return errors.New("controlclient: exact work execution target is required")
		}
	default:
		return errors.New("controlclient: unsupported Bot work action")
	}
	if action != ActionBotWorkCancel && (strings.TrimSpace(req.SourceID) == "" || strings.TrimSpace(req.Assignment) == "") {
		return errors.New("controlclient: request source and assignment are required")
	}
	if len(req.Assignment) > bot.MaxDescriptionBytes {
		return errors.New("controlclient: assignment exceeds 64 KiB")
	}
	return nil
}

func (s *CommandService) AcknowledgeBotCompletion(ctx context.Context, p Principal, r BotWorkRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotCompletionAck, r.WriteBase, "bot/"+r.BotID+"/completion/"+r.WorkID, r)
}
