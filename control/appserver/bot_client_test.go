package appserver

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/control/bot"
)

type fakeBotReader struct {
	listed  string
	getID   string
	loadErr error
}

func (r *fakeBotReader) ListBots(_ context.Context, ownerID string) ([]bot.Bot, error) {
	r.listed = ownerID
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	return []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada"}}}, nil
}

func (r *fakeBotReader) GetBot(_ context.Context, botID string) (bot.Bot, error) {
	r.getID = botID
	if r.loadErr != nil {
		return bot.Bot{}, r.loadErr
	}
	return bot.Bot{ID: botID, SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada"}}, nil
}

type fakeBotCommands struct {
	created CreateBotRequest
	updated UpdateBotRequest
	calls   int
}

func (c *fakeBotCommands) CreateBot(_ context.Context, _ Principal, req CreateBotRequest) (CommandResult, error) {
	c.calls++
	c.created = req
	return CommandResult{
		OperationID: req.OperationID, Outcome: OutcomeCommitted, SessionID: "bot-chat-1",
		Resource: &CommandResource{Kind: CommandResourceBot, Ref: "bot-1"},
	}, nil
}

func (c *fakeBotCommands) UpdateBot(_ context.Context, _ Principal, req UpdateBotRequest) (CommandResult, error) {
	c.calls++
	c.updated = req
	return CommandResult{OperationID: req.OperationID, Outcome: OutcomeCommitted, SessionID: req.SessionID}, nil
}

func TestBotServiceRequiresDependencies(t *testing.T) {
	if _, err := NewBotService(BotServiceConfig{}); err == nil {
		t.Fatal("NewBotService accepted missing dependencies")
	}
	if _, err := NewBotService(BotServiceConfig{Reader: &fakeBotReader{}, Commands: &fakeBotCommands{}}); err == nil {
		t.Fatal("NewBotService accepted a missing authorizer")
	}
}

func TestBotServiceListBotsIsOwnerScoped(t *testing.T) {
	reader := &fakeBotReader{}
	service, err := NewBotService(BotServiceConfig{Reader: reader, Commands: &fakeBotCommands{}, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	bots, err := service.ListBots(context.Background(), Principal{ID: " owner "})
	if err != nil || len(bots) != 1 || bots[0].ID != "bot-1" {
		t.Fatalf("ListBots() = %#v, %v", bots, err)
	}
	if reader.listed != "owner" {
		t.Fatalf("ListBots owner = %q, want trimmed principal", reader.listed)
	}
	if _, err := service.ListBots(context.Background(), Principal{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ListBots(empty principal) error = %v, want ErrUnauthorized", err)
	}
}

func TestBotServiceGetBotAuthorizesThroughBoundSession(t *testing.T) {
	reader := &fakeBotReader{}
	authorizer := &recordingAuthorizer{}
	service, err := NewBotService(BotServiceConfig{Reader: reader, Commands: &fakeBotCommands{}, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.GetBot(context.Background(), Principal{ID: "owner"}, " bot-1 ")
	if err != nil || value.ID != "bot-1" || value.Revision != 4 {
		t.Fatalf("GetBot() = %#v, %v", value, err)
	}
	if reader.getID != "bot-1" {
		t.Fatalf("GetBot id = %q, want trimmed", reader.getID)
	}
	if authorizer.calls != 1 || authorizer.action != ActionBotGet || authorizer.sessionID != "bot-chat-1" {
		t.Fatalf("GetBot authorization = %d %s %q, want bot.get/bot-chat-1", authorizer.calls, authorizer.action, authorizer.sessionID)
	}

	if _, err := service.GetBot(context.Background(), Principal{ID: "owner"}, "  "); err == nil {
		t.Fatal("GetBot accepted an empty bot id")
	}
	if _, err := service.GetBot(context.Background(), Principal{}, "bot-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("GetBot(empty principal) error = %v, want ErrUnauthorized", err)
	}
}

func TestBotServiceGetBotRejectsUnauthorizedOwner(t *testing.T) {
	reader := &fakeBotReader{}
	service, err := NewBotService(BotServiceConfig{Reader: reader, Commands: &fakeBotCommands{}, Authorizer: denyAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetBot(context.Background(), Principal{ID: "intruder"}, "bot-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("GetBot() error = %v, want ErrUnauthorized", err)
	}
}

func TestBotServiceMutationsForwardToCommands(t *testing.T) {
	commands := &fakeBotCommands{}
	service, err := NewBotService(BotServiceConfig{Reader: &fakeBotReader{}, Commands: commands, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	revision := uint64(4)
	created, err := service.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
		WriteBase: WriteBase{OperationID: "bot-create-1"}, Config: bot.Config{Name: "Ada"},
	})
	if err != nil || created.Resource == nil || created.Resource.Kind != CommandResourceBot || created.Resource.Ref != "bot-1" || created.SessionID != "bot-chat-1" {
		t.Fatalf("CreateBot() = %#v, %v", created, err)
	}
	if _, err := service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
		WriteBase: WriteBase{OperationID: "bot-update-1", SessionID: "bot-chat-1", ExpectedRevision: &revision},
		BotID:     "bot-1", Config: bot.Config{Name: "Ada Lovelace"},
	}); err != nil {
		t.Fatal(err)
	}
	if commands.calls != 2 || commands.created.Config.Name != "Ada" || commands.updated.BotID != "bot-1" {
		t.Fatalf("Bot command forwarding = %#v %#v", commands.created, commands.updated)
	}
}

func TestBindBotClientBindsPrincipal(t *testing.T) {
	if _, err := BindBotClient(nil, Principal{ID: "owner"}); err == nil {
		t.Fatal("BindBotClient accepted a nil service")
	}
	service, err := NewBotService(BotServiceConfig{Reader: &fakeBotReader{}, Commands: &fakeBotCommands{}, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindBotClient(service, Principal{}); err == nil {
		t.Fatal("BindBotClient accepted an empty principal")
	}
	client, err := BindBotClient(service, Principal{ID: " owner ", Roles: []string{"operator"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListBots(context.Background()); err != nil {
		t.Fatal(err)
	}
}
