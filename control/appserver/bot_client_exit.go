package appserver

import (
	"context"
	"errors"
)

const ActionBotClientExit Action = "bot.client.exit"

// BotClientExitRequest revokes one exact activation and optionally interrupts
// its client-owned work. It never shuts down a shared Control Host.
type BotClientExitRequest struct {
	WriteBase
	BotID           string `json:"bot_id"`
	ActivationID    string `json:"activation_id"`
	CancelOwnedWork bool   `json:"cancel_owned_work"`
}

// ExitBotClient uses the normal durable operation ledger for explicit exit.
func (s *CommandService) ExitBotClient(ctx context.Context, p Principal, r BotClientExitRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionBotClientExit, r.WriteBase, "bot-client/"+p.ClientID+"/"+r.ActivationID, r)
}
func validateBotClientExit(action Action, r BotClientExitRequest) error {
	if action != ActionBotClientExit || r.BotID == "" || r.ActivationID == "" {
		return errors.New("controlclient: exact Bot activation is required")
	}
	return requireSession(r.SessionID)
}
