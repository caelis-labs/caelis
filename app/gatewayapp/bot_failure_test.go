package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type botFailReceiptStore struct {
	appserver.OperationStore
	failed atomic.Bool
}

func (s *botFailReceiptStore) Complete(ctx context.Context, intent appserver.OperationIntent, result appserver.CommandResult) (appserver.OperationRecord, error) {
	if !s.failed.Swap(true) {
		return appserver.OperationRecord{}, errors.New("injected receipt failure")
	}
	return s.OperationStore.Complete(ctx, intent, result)
}

func TestBotCreationUnknownReceiptRecoversWithoutDuplicateConversation(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: "local-user"}
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{Sessions: appserver.SessionAuthorizer{Sessions: stack.Sessions()}},
		Operations: &botFailReceiptStore{OperationStore: appserver.NewMemoryOperationStore()}, Backend: stack.commandBackend,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "uncertain-create"}, Config: bot.Config{Name: "Persistent"}}
	unknown, err := commands.CreateBot(ctx, principal, request)
	if err == nil || unknown.Outcome != appserver.OutcomeUnknown || unknown.Resource == nil || unknown.SessionID == "" {
		t.Fatalf("missing unknown effect identity: %+v, %v", unknown, err)
	}
	recovered, err := commands.CreateBot(ctx, principal, request)
	if err != nil || recovered.Outcome != appserver.OutcomeCommitted || recovered.SessionID != unknown.SessionID || recovered.Resource.Ref != unknown.Resource.Ref {
		t.Fatalf("recovery duplicated/lost creation: %+v, %v", recovered, err)
	}
	list, err := stack.Bots().ListBots(ctx, principal)
	if err != nil || len(list) != 1 {
		t.Fatalf("unknown creation duplicated Bot: %+v, %v", list, err)
	}
	loaded, err := stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: recovered.SessionID}})
	if err != nil || len(loaded.Events) != 1 || session.EventText(loaded.Events[0]) != bot.ConfigurationMessage(request.Config) {
		t.Fatalf("recovery repeated configuration event: %+v, %v", loaded.Events, err)
	}
	// A fresh operation ledger models expiry of terminal receipts. The durable
	// domain identity still deduplicates the same creation payload.
	commands, err = appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{Sessions: appserver.SessionAuthorizer{Sessions: stack.Sessions()}},
		Operations: appserver.NewMemoryOperationStore(), Backend: stack.commandBackend,
	})
	if err != nil {
		t.Fatal(err)
	}
	afterRetention, err := commands.CreateBot(ctx, principal, request)
	if err != nil || afterRetention.SessionID != recovered.SessionID || afterRetention.Resource.Ref != recovered.Resource.Ref {
		t.Fatalf("receipt expiry reallocated identity: %+v, %v", afterRetention, err)
	}
}

type botFailSaveSessions struct{ session.Service }

func (s botFailSaveSessions) AppendEventsAndUpdateState(context.Context, session.AppendEventsAndUpdateStateRequest) ([]*session.Event, error) {
	return nil, errors.New("injected configuration save failure")
}

func TestBotFailedSaveDoesNotChangeConfigurationOrCanonicalHistory(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: "local-user"}
	value := createTestBot(t, stack, "create-save-test", "Original")
	ref := session.SessionRef{SessionID: value.SessionID}
	before, err := stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	original := stack.composition.sessions
	stack.composition.sessions = botFailSaveSessions{Service: original}
	defer func() { stack.composition.sessions = original }()
	changed := value.Config
	changed.Name, changed.Description = "Not saved", "must not become context"
	request := appserver.UpdateBotRequest{WriteBase: appserver.WriteBase{OperationID: "failed-save", SessionID: value.SessionID, ExpectedRevision: &value.Revision}, BotID: value.ID, Config: changed}
	result, err := stack.Bots().UpdateBot(ctx, principal, request)
	if err == nil || result.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("save falsely succeeded: %+v, %v", result, err)
	}
	stack.composition.sessions = original
	after, err := original.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed transaction changed durable facts: %v", err)
	}
	result, err = stack.Bots().UpdateBot(ctx, principal, request)
	if err != nil || result.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("uncertain save was blindly repeated: %+v, %v", result, err)
	}
	current, err := stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil || current.Config != value.Config {
		t.Fatalf("failed update lost identity/config: %+v, %v", current, err)
	}
}

func TestBotOrdinarySessionCommandsCannotGrantProductRole(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: "local-user"}
	for index, metadata := range []map[string]any{
		{sessionvisibility.MetadataSystemManagedAgent: "bot"},
		{sessionvisibility.MetadataSystemManagedAgent: " BOT "},
		{bot.MetadataID: "forged"},
	} {
		result, err := stack.ControlClient().CreateSession(context.Background(), principal, appserver.CreateSessionRequest{
			WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("forge-%d", index)}, Metadata: metadata,
		})
		if !errors.Is(err, appserver.ErrUnauthorized) || result.Outcome != appserver.OutcomeRejected {
			t.Fatalf("untrusted metadata granted Bot role: %+v, %v", result, err)
		}
	}
}

// The store has committed, but its caller loses the response boundary. The
// resulting configuration must keep its newly accepted Runtime model pin.
type botPostCommitSessions struct {
	session.Service
	cancel    context.CancelFunc
	failRead  bool
	committed bool
}

func (s *botPostCommitSessions) AppendEventsAndUpdateState(ctx context.Context, req session.AppendEventsAndUpdateStateRequest) ([]*session.Event, error) {
	events, err := s.Service.(session.EventBatchStateService).AppendEventsAndUpdateState(ctx, req)
	if err == nil {
		s.committed = true
		s.cancel()
	}
	return events, err
}

func (s *botPostCommitSessions) Session(ctx context.Context, ref session.SessionRef) (session.Session, error) {
	if s.committed && s.failRead {
		return session.Session{}, errors.New("post-commit read unavailable")
	}
	return s.Service.Session(ctx, ref)
}

func TestBotCommittedSaveKeepsNewModelPinWhenClientCancels(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("read_failure_%t", failRead), func(t *testing.T) {
			ctx := context.Background()
			stack, err := newGatewayAppTestStack(t, Config{
				StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(),
				Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "old-model", Token: "test-token"},
			})
			if err != nil {
				t.Fatal(err)
			}
			value := createTestBot(t, stack, "pin-create", "Pin")
			active := activateSessionRuntime(t, stack, value.SessionID)
			release, err := stack.sessionRuntimes.retainObservation(session.SessionRef{SessionID: value.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			profile, err := stack.connectTestModel(ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "new-model", Token: "test-token"})
			if err != nil {
				t.Fatal(err)
			}
			modelID := profile.Backend.Provider.ModelConfigID
			if _, err := active.instance.lookup.ResolveConfig(modelID); err == nil {
				t.Fatal("fixture: new model unexpectedly already admitted")
			}
			value, err = stack.Bots().GetBot(ctx, appserver.Principal{ID: "local-user"}, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			value.Config.Model = modelID
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			original := stack.composition.sessions
			stack.composition.sessions = &botPostCommitSessions{Service: original, cancel: cancel, failRead: failRead}
			result, err := stack.Bots().UpdateBot(requestCtx, appserver.Principal{ID: "local-user"}, appserver.UpdateBotRequest{
				WriteBase: appserver.WriteBase{OperationID: "pin-update", SessionID: value.SessionID, ExpectedRevision: &value.Revision}, BotID: value.ID, Config: value.Config,
			})
			stack.composition.sessions = original
			if err != nil || result.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("known commit became rejected/unknown: %+v, %v", result, err)
			}
			if failRead && (result.Revision != 0 || result.Detail == "") {
				t.Fatalf("unobserved revision fabricated: %+v", result)
			}
			tools, err := active.instance.exec.(*bot.Notebook).Tools()
			if err != nil {
				t.Fatal(err)
			}
			resolver := &botTurnResolver{composition: &active.instance.runtimeComposition, notebookTools: tools}
			if _, err := resolver.ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef{SessionID: value.SessionID}}); err != nil {
				t.Fatalf("committed model pin rolled back: %v", err)
			}
			persisted, err := stack.Bots().GetBot(ctx, appserver.Principal{ID: "local-user"}, value.ID)
			if err != nil || persisted.Config.Model != modelID {
				t.Fatalf("committed config lost: %+v, %v", persisted, err)
			}
		})
	}
}
