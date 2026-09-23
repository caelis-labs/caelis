package appserver

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/control/bot"
)

// RegisterBotClientRequest enrolls a native client with a fixed action allowlist.
// No executable paths, arguments, environment or global plugin settings exist.
type RegisterBotClientRequest struct {
	WriteBase
	BotID   string   `json:"bot_id"`
	Actions []string `json:"actions"`
}

// RegisterBotClient requires an existing authenticated Host principal. The new
// credential is never stored in the shared operation receipt or model history.
func (s *BotWorkService) RegisterBotClient(ctx context.Context, p Principal, r RegisterBotClientRequest) (bot.ClientRegistration, error) {
	if p.ClientID != "" {
		return bot.ClientRegistration{}, ErrUnauthorized
	}
	value, err := s.Bots.GetBot(ctx, p, r.BotID)
	if err != nil {
		return bot.ClientRegistration{}, err
	}
	if len(r.Actions) > 0 && !value.Config.DesktopActions {
		return bot.ClientRegistration{}, errors.New("controlclient: desktop actions are not enabled for this Bot")
	}
	return s.Store.RegisterClient(ctx, p.ID, r.BotID, r.OperationID, r.Actions)
}

// AuthorizeBotSession preserves a Bot client's scope when entering existing
// Session/SSE/Task services whose public principal type predates Bot clients.
func (s *BotWorkService) AuthorizeBotSession(ctx context.Context, p Principal, sessionID string) error {
	value, err := s.Bots.GetBot(ctx, p, p.BotID)
	if err != nil {
		return err
	}
	if value.SessionID == sessionID {
		return nil
	}
	work, err := s.Store.GetWork(ctx, p.ID, p.BotID, sessionID)
	if err != nil {
		return err
	}
	if work.SessionID != sessionID {
		return ErrUnauthorized
	}
	return nil
}
