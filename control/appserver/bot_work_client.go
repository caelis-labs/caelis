package appserver

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/control/bot"
)

// BotWorkService binds owned work reads and shared-ledger commands. Native
// Session bootstrap, SSE, Task output and approvals remain their existing owners.
type BotWorkService struct {
	Bots     BotService
	Store    *bot.WorkStore
	Commands BotWorkCommands
	// Wake examines finite persisted activity after client reactivation.
	Wake func(context.Context, string, string)
}

// BotWorkClient is the principal-bound managed-work surface capability.
type BotWorkClient interface {
	GetBotWorkOperation(context.Context, string, string) (bot.WorkOperation, error)
	ListBotCompletions(context.Context, string) ([]bot.Completion, error)
	AcknowledgeBotCompletion(context.Context, BotWorkRequest) (CommandResult, error)
	ListBotWork(context.Context, string) ([]bot.Work, error)
	GetBotWork(context.Context, string, string) (bot.Work, error)
	GetBotRequest(context.Context, string, string) (bot.RequestSource, error)
	CreateBotWork(context.Context, BotWorkRequest) (CommandResult, error)
	ContinueBotWork(context.Context, BotWorkRequest) (CommandResult, error)
	SteerBotWork(context.Context, BotWorkRequest) (CommandResult, error)
	CancelBotWork(context.Context, BotWorkRequest) (CommandResult, error)
}

func (s *BotWorkService) authorize(ctx context.Context, p Principal, botID string) error {
	if s == nil || s.Bots == nil || s.Store == nil || s.Commands == nil {
		return errors.New("controlclient: managed Bot work unavailable")
	}
	_, err := s.Bots.GetBot(ctx, p, botID)
	return err
}
func (s *BotWorkService) ListBotWork(ctx context.Context, p Principal, id string) ([]bot.Work, error) {
	if err := s.authorize(ctx, p, id); err != nil {
		return nil, err
	}
	return s.Store.ListWork(ctx, p.ID, id)
}
func (s *BotWorkService) GetBotWork(ctx context.Context, p Principal, id, workID string) (bot.Work, error) {
	if err := s.authorize(ctx, p, id); err != nil {
		return bot.Work{}, err
	}
	return s.Store.GetWork(ctx, p.ID, id, workID)
}
func (s *BotWorkService) GetBotRequest(ctx context.Context, p Principal, id, operationID string) (bot.RequestSource, error) {
	if err := s.authorize(ctx, p, id); err != nil {
		return bot.RequestSource{}, err
	}
	return s.Store.RequestForOperation(ctx, p.ID, id, operationID)
}

type boundBotWorkClient struct {
	s *BotWorkService
	p Principal
}

func (c *boundBotWorkClient) ListBotWork(ctx context.Context, id string) ([]bot.Work, error) {
	return c.s.ListBotWork(ctx, c.p, id)
}
func (c *boundBotWorkClient) GetBotWork(ctx context.Context, id, work string) (bot.Work, error) {
	return c.s.GetBotWork(ctx, c.p, id, work)
}
func (c *boundBotWorkClient) GetBotRequest(ctx context.Context, id, operation string) (bot.RequestSource, error) {
	return c.s.GetBotRequest(ctx, c.p, id, operation)
}
func (c *boundBotWorkClient) CreateBotWork(ctx context.Context, r BotWorkRequest) (CommandResult, error) {
	return c.s.Commands.CreateBotWork(ctx, c.p, r)
}
func (c *boundBotWorkClient) ContinueBotWork(ctx context.Context, r BotWorkRequest) (CommandResult, error) {
	return c.s.Commands.ContinueBotWork(ctx, c.p, r)
}
func (c *boundBotWorkClient) SteerBotWork(ctx context.Context, r BotWorkRequest) (CommandResult, error) {
	return c.s.Commands.SteerBotWork(ctx, c.p, r)
}
func (c *boundBotWorkClient) CancelBotWork(ctx context.Context, r BotWorkRequest) (CommandResult, error) {
	return c.s.Commands.CancelBotWork(ctx, c.p, r)
}

func (s *BotWorkService) GetBotWorkOperation(ctx context.Context, p Principal, id, operationID string) (bot.WorkOperation, error) {
	if err := s.authorize(ctx, p, id); err != nil {
		return bot.WorkOperation{}, err
	}
	return s.Store.Operation(ctx, p.ID, id, operationID)
}
func (s *BotWorkService) ListBotCompletions(ctx context.Context, p Principal, id string) ([]bot.Completion, error) {
	if err := s.authorize(ctx, p, id); err != nil {
		return nil, err
	}
	return s.Store.Completions(ctx, p.ID, id)
}
func (c *boundBotWorkClient) GetBotWorkOperation(ctx context.Context, id, operation string) (bot.WorkOperation, error) {
	return c.s.GetBotWorkOperation(ctx, c.p, id, operation)
}
func (c *boundBotWorkClient) ListBotCompletions(ctx context.Context, id string) ([]bot.Completion, error) {
	return c.s.ListBotCompletions(ctx, c.p, id)
}
func (c *boundBotWorkClient) AcknowledgeBotCompletion(ctx context.Context, r BotWorkRequest) (CommandResult, error) {
	return c.s.Commands.AcknowledgeBotCompletion(ctx, c.p, r)
}
