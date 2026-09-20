package appserver

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/bot"
)

// CreateBotRequest creates one owner-scoped Bot. The identity is derived from
// the bound principal and operation ID, so a retried creation cannot allocate a
// second Bot. An empty Config.Model asks the Host to snapshot its default model;
// a Bot without a configured provider model can still be created but fails
// explicitly when prompted.
type CreateBotRequest struct {
	WriteBase
	Config bot.Config `json:"config"`
}

// UpdateBotRequest replaces one Bot's complete configuration. SessionID is
// required and must address the Session the Bot is bound to; the backend
// verifies that binding. ExpectedRevision is the Bot's Session CAS revision.
type UpdateBotRequest struct {
	WriteBase
	BotID  string     `json:"bot_id"`
	Config bot.Config `json:"config"`
	// EnableNotebook explicitly admits notebook access for a legacy Bot. False
	// preserves the current capability; ordinary configuration edits cannot revoke it.
	EnableNotebook bool `json:"enable_notebook,omitempty"`
}

// BotReader is the Host-provided Bot store. ListBots returns only the addressed
// owner's Bots; GetBot resolves one Bot by stable identity and lets AppServer
// authorize the caller against the Bot's bound Session.
type BotReader interface {
	ListBots(ctx context.Context, ownerID string) ([]bot.Bot, error)
	GetBot(ctx context.Context, botID string) (bot.Bot, error)
}

// BotCommandService is the principal-aware Host Bot mutation capability
// implemented by the shared Control command executor. It reuses the same
// durable operation ledger as Session and configuration commands.
type BotCommandService interface {
	CreateBot(context.Context, Principal, CreateBotRequest) (CommandResult, error)
	UpdateBot(context.Context, Principal, UpdateBotRequest) (CommandResult, error)
}

// BotService is the principal-aware AppServer Bot read and mutation capability.
type BotService interface {
	ListBots(context.Context, Principal) ([]bot.Bot, error)
	GetBot(context.Context, Principal, string) (bot.Bot, error)
	CreateBot(context.Context, Principal, CreateBotRequest) (CommandResult, error)
	UpdateBot(context.Context, Principal, UpdateBotRequest) (CommandResult, error)
}

// BotClient is the principal-bound Bot capability consumed by a presentation
// surface. Embedded and HTTP transports implement this same interface.
type BotClient interface {
	ListBots(context.Context) ([]bot.Bot, error)
	GetBot(context.Context, string) (bot.Bot, error)
	CreateBot(context.Context, CreateBotRequest) (CommandResult, error)
	UpdateBot(context.Context, UpdateBotRequest) (CommandResult, error)
}

// BotServiceConfig assembles the AppServer Bot capability from the owner-scoped
// Bot reader, the shared command executor, and the product authorizer. Reads are
// self-scoped by principal; GetBot additionally authorizes the caller against
// the addressed Bot's bound Session.
type BotServiceConfig struct {
	Reader     BotReader
	Commands   BotCommandService
	Authorizer Authorizer
}

type botService struct{ config BotServiceConfig }

// NewBotService validates and returns the AppServer Bot capability.
func NewBotService(config BotServiceConfig) (BotService, error) {
	if config.Reader == nil || config.Commands == nil || config.Authorizer == nil {
		return nil, errors.New("controlclient: Bot service dependencies are required")
	}
	return &botService{config: config}, nil
}

func (s *botService) ListBots(ctx context.Context, principal Principal) ([]bot.Bot, error) {
	ownerID := strings.TrimSpace(principal.ID)
	if ownerID == "" {
		return nil, ErrUnauthorized
	}
	return s.config.Reader.ListBots(ctx, ownerID)
}

func (s *botService) GetBot(ctx context.Context, principal Principal, botID string) (bot.Bot, error) {
	if strings.TrimSpace(principal.ID) == "" {
		return bot.Bot{}, ErrUnauthorized
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return bot.Bot{}, errors.New("controlclient: bot id is required")
	}
	existing, err := s.config.Reader.GetBot(ctx, botID)
	if err != nil {
		return bot.Bot{}, err
	}
	if err := s.config.Authorizer.Authorize(ctx, principal, ActionBotGet, existing.SessionID); err != nil {
		return bot.Bot{}, err
	}
	return existing, nil
}

func (s *botService) CreateBot(ctx context.Context, principal Principal, req CreateBotRequest) (CommandResult, error) {
	return s.config.Commands.CreateBot(ctx, principal, req)
}

func (s *botService) UpdateBot(ctx context.Context, principal Principal, req UpdateBotRequest) (CommandResult, error) {
	return s.config.Commands.UpdateBot(ctx, principal, req)
}

type boundBotClient struct {
	service   BotService
	principal Principal
}

// BindBotClient binds one trusted principal to the AppServer Bot capability.
func BindBotClient(service BotService, principal Principal) (BotClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: Bot service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundBotClient{service: service, principal: principal}, nil
}

func (c *boundBotClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

func (c *boundBotClient) ListBots(ctx context.Context) ([]bot.Bot, error) {
	return c.service.ListBots(ctx, c.boundPrincipal())
}

func (c *boundBotClient) GetBot(ctx context.Context, botID string) (bot.Bot, error) {
	return c.service.GetBot(ctx, c.boundPrincipal(), botID)
}

func (c *boundBotClient) CreateBot(ctx context.Context, req CreateBotRequest) (CommandResult, error) {
	return c.service.CreateBot(ctx, c.boundPrincipal(), req)
}

func (c *boundBotClient) UpdateBot(ctx context.Context, req UpdateBotRequest) (CommandResult, error) {
	return c.service.UpdateBot(ctx, c.boundPrincipal(), req)
}

var _ BotClient = (*boundBotClient)(nil)
