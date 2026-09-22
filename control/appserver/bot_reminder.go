package appserver

import (
	"context"
	"errors"
	"time"
)

const ActionBotReminderFire Action = "bot.reminder.fire"

// BotReminderRequest reports one native schedule occurrence. It cannot provide
// prompt text; Control resolves the exact persisted grant and original request.
type BotReminderRequest struct {
	WriteBase
	BotID   string    `json:"bot_id"`
	GrantID string    `json:"grant_id"`
	Version string    `json:"version"`
	Due     time.Time `json:"due"`
}

func (s *CommandService) FireBotReminder(ctx context.Context, p Principal, r BotReminderRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotReminderFire, r.WriteBase, "bot-reminder/"+r.GrantID+"/"+r.Version+"/"+r.Due.UTC().Format(time.RFC3339Nano), r)
}
func validateBotReminder(action Action, r BotReminderRequest) error {
	if action != ActionBotReminderFire || r.BotID == "" || r.GrantID == "" || r.Version == "" || r.Due.IsZero() {
		return errors.New("controlclient: exact reminder occurrence is required")
	}
	return requireSession(r.SessionID)
}
