package appserver

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/bot"
)

func TestBotCommandIntentsUseCanonicalTargets(t *testing.T) {
	revision := uint64(4)
	tests := []struct {
		name        string
		wantAction  Action
		wantTarget  string
		wantSession string
		invoke      func(*CommandService) (CommandResult, error)
	}{
		{
			name:       "create",
			wantAction: ActionBotCreate,
			wantTarget: BotCreateTarget,
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
					WriteBase: WriteBase{OperationID: "bot-create-1"}, Config: bot.Config{Name: " Ada "},
				})
			},
		},
		{
			name:        "update",
			wantAction:  ActionBotUpdate,
			wantTarget:  "bot/bot-1",
			wantSession: "bot-chat-1",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
					WriteBase: WriteBase{OperationID: "bot-update-1", SessionID: "bot-chat-1", ExpectedRevision: &revision},
					BotID:     " bot-1 ", Config: bot.Config{Name: "Ada Lovelace"},
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			service := newTestCommandService(t, allowAuthorizer{}, operations, &recordingCommandBackend{})
			if _, err := test.invoke(service); err != nil {
				t.Fatal(err)
			}
			if len(operations.intents) != 1 || operations.intents[0].Action != test.wantAction ||
				operations.intents[0].Target != test.wantTarget || operations.intents[0].SessionID != test.wantSession {
				t.Fatalf("operation intents = %#v, want %s %q %q", operations.intents, test.wantAction, test.wantTarget, test.wantSession)
			}
		})
	}
}

func TestBotCommandsRejectInvalidRequestsBeforeLedger(t *testing.T) {
	revision := uint64(4)
	tests := []struct {
		name   string
		invoke func(*CommandService) (CommandResult, error)
	}{
		{
			name: "create addresses a session",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-create-session", SessionID: "bot-chat-1"}, Config: bot.Config{Name: "Ada"},
				})
			},
		},
		{
			name: "create carries a revision",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-create-revision", ExpectedRevision: &revision}, Config: bot.Config{Name: "Ada"},
				})
			},
		},
		{
			name: "create without a name",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-create-name"},
				})
			},
		},
		{
			name: "update without a session",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-update-session", ExpectedRevision: &revision},
					BotID:     "bot-1", Config: bot.Config{Name: "Ada"},
				})
			},
		},
		{
			name: "update without a revision",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-update-revision", SessionID: "bot-chat-1"},
					BotID:     "bot-1", Config: bot.Config{Name: "Ada"},
				})
			},
		},
		{
			name: "update without a bot id",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-update-bot", SessionID: "bot-chat-1", ExpectedRevision: &revision},
					Config:    bot.Config{Name: "Ada"},
				})
			},
		},
		{
			name: "update without a name",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
					WriteBase: WriteBase{OperationID: "bad-update-name", SessionID: "bot-chat-1", ExpectedRevision: &revision},
					BotID:     "bot-1",
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			result, err := test.invoke(service)
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
				t.Fatalf("result = %#v, %v", result, err)
			}
			if operations.beginCalls != 0 || backend.calls != 0 {
				t.Fatalf("invalid request reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
			}
		})
	}
}

func TestBotCommandsAuthorizeCanonicalScope(t *testing.T) {
	revision := uint64(4)
	authorizer := &recordingAuthorizer{}
	operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
	service := newTestCommandService(t, authorizer, operations, &recordingCommandBackend{})

	if _, err := service.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
		WriteBase: WriteBase{OperationID: "bot-create-scope"}, Config: bot.Config{Name: "Ada"},
	}); err != nil {
		t.Fatal(err)
	}
	if authorizer.action != ActionBotCreate || authorizer.sessionID != "" {
		t.Fatalf("create authorization = %s %q, want bot.create/host", authorizer.action, authorizer.sessionID)
	}

	if _, err := service.UpdateBot(context.Background(), Principal{ID: "owner"}, UpdateBotRequest{
		WriteBase: WriteBase{OperationID: "bot-update-scope", SessionID: "bot-chat-1", ExpectedRevision: &revision},
		BotID:     "bot-1", Config: bot.Config{Name: "Ada"},
	}); err != nil {
		t.Fatal(err)
	}
	if authorizer.action != ActionBotUpdate || authorizer.sessionID != "bot-chat-1" {
		t.Fatalf("update authorization = %s %q, want bot.update/bot-chat-1", authorizer.action, authorizer.sessionID)
	}
	if authorizer.calls != 2 {
		t.Fatalf("authorize calls = %d, want 2", authorizer.calls)
	}

	denied := newTestCommandService(t, denyAuthorizer{}, NewMemoryOperationStore(), &recordingCommandBackend{})
	if _, err := denied.CreateBot(context.Background(), Principal{ID: "owner"}, CreateBotRequest{
		WriteBase: WriteBase{OperationID: "bot-create-denied"}, Config: bot.Config{Name: "Ada"},
	}); err == nil {
		t.Fatal("CreateBot bypassed a denying authorizer")
	}
}
