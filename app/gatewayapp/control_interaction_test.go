package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

func TestControlInteractionPersistsRealConversationIdentityWithoutModelActivation(t *testing.T) {
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "store")
	stack, err := newGatewayAppTestStack(t, Config{AppName: "test", UserID: "owner", StoreDir: root, WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	owner := appserver.Principal{ID: "owner"}
	created, err := stack.ControlClient().CreateSession(ctx, owner, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := session.SessionRef{SessionID: created.SessionID}
	active, err := stack.composition.sessions.Session(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	req := appserver.RecordInteractionRequest{WriteBase: appserver.WriteBase{OperationID: "status", SessionID: active.SessionID, ExpectedRevision: &active.Revision, ExpectedControllerEpoch: active.Controller.EpochID}, Text: "/status"}
	result, err := stack.ControlClient().(appserver.InteractionService).RecordInteraction(ctx, owner, req)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Resource == nil || result.Resource.Ref == "" || result.Revision <= active.Revision {
		t.Fatalf("durable interaction: %+v, %v", result, err)
	}
	if replayed, err := stack.ControlClient().(appserver.InteractionService).RecordInteraction(ctx, owner, req); err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("same operation lost exact receipt: %+v, %v", replayed, err)
	}
	changed := req
	changed.Text = "/compact"
	if _, err := stack.ControlClient().(appserver.InteractionService).RecordInteraction(ctx, owner, changed); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("operation identity reused for another input: %v", err)
	}
	changed = req
	changed.OperationID = "stale-status"
	if _, err := stack.ControlClient().(appserver.InteractionService).RecordInteraction(ctx, owner, changed); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("stale CAS admitted another message: %v", err)
	}
	before, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var interaction *session.Event
	for _, event := range before {
		if event.Type == session.EventTypeInteraction {
			if interaction != nil {
				t.Fatal("idempotent retry appended another interaction")
			}
			interaction = event
		}
	}
	if interaction == nil || interaction.ID != result.Resource.Ref || interaction.MessageID != interaction.ID || interaction.Actor.ID != owner.ID || !session.IsMirror(interaction) || !session.IsClientReplayEvent(interaction) {
		t.Fatalf("receipt was not the real native conversation event: %+v", interaction)
	}
	live, err := projection.ProjectEvent(interaction)
	if err != nil || len(live) != 1 {
		t.Fatalf("interaction projection: %+v, %v", live, err)
	}
	chunk, ok := live[0].(eventstream.ContentChunk)
	contentJSON, contentErr := json.Marshal(chunk.Content)
	var content struct{ Text string }
	decodeErr := json.Unmarshal(contentJSON, &content)
	if !ok || contentErr != nil || decodeErr != nil || chunk.MessageID != interaction.ID || chunk.SessionUpdate != eventstream.UpdateUserMessage || content.Text != req.Text {
		t.Fatalf("wire receipt and native user echo diverged: %+v", live)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	after, err := reopened.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var replay []eventstream.Update
	for _, event := range after {
		if event.ID == interaction.ID {
			replay, err = projection.ProjectEvent(event)
		}
	}
	if err != nil || !reflect.DeepEqual(live, replay) {
		t.Fatalf("live/recovery conversation identity changed: %+v / %+v, %v", live, replay, err)
	}
	if visible := compact.PromptEventsFromLatestCompact(after); len(visible) != 0 {
		t.Fatalf("display-only command polluted rebuilt model context: %+v", visible)
	}
}

type interactionReceiptFailureStore struct {
	appserver.OperationStore
	failed bool
}

func (s *interactionReceiptFailureStore) Complete(ctx context.Context, intent appserver.OperationIntent, result appserver.CommandResult) (appserver.OperationRecord, error) {
	if !s.failed {
		s.failed = true
		return appserver.OperationRecord{}, errors.New("injected ledger receipt loss")
	}
	return s.OperationStore.Complete(ctx, intent, result)
}

func TestControlInteractionRecoversInsertedIdentityAfterLedgerReceiptLoss(t *testing.T) {
	ctx := t.Context()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	composition := &runtimeComposition{sessions: store}
	operations := &interactionReceiptFailureStore{OperationStore: appserver.NewMemoryOperationStore()}
	service, err := appserver.NewCommandService(appserver.CommandServiceConfig{Authorizer: controlClientAllowAuthorizer{}, Operations: operations, Backend: &controlCommandBackend{composition: composition}})
	if err != nil {
		t.Fatal(err)
	}
	req := appserver.RecordInteractionRequest{WriteBase: appserver.WriteBase{OperationID: "command", SessionID: active.SessionID, ExpectedRevision: &active.Revision}, Text: "/help"}
	first, err := service.RecordInteraction(ctx, appserver.Principal{ID: "owner"}, req)
	if err == nil || first.Outcome != appserver.OutcomeUnknown || first.Resource == nil || first.Resource.Ref == "" {
		t.Fatalf("post-append ledger failure erased known receipt: %+v, %v", first, err)
	}
	recovered, err := service.RecordInteraction(ctx, appserver.Principal{ID: "owner"}, req)
	if err != nil || recovered.Outcome != appserver.OutcomeCommitted || recovered.Resource == nil || recovered.Resource.Ref != first.Resource.Ref {
		t.Fatalf("original durable identity was not recovered: %+v, %v", recovered, err)
	}
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil || len(events) != 1 || events[0].ID != first.Resource.Ref {
		t.Fatalf("recovery redispatched an append: %+v, %v", events, err)
	}
}

type idleInteractionFenceRaceStore struct {
	session.Service
	store *sessionmemory.Store
}

func (s *idleInteractionFenceRaceStore) AppendEvent(ctx context.Context, request session.AppendEventRequest) (*session.Event, error) {
	if request.MutationGuard.Purpose == session.ControlMutationPurposeIdleInteraction {
		// Another producer acquires the real native fence after the Session CAS
		// read but before the append's transaction. No revision change is needed.
		if _, err := s.store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: request.SessionRef, OwnerID: "raced-native-producer"}); err != nil {
			return nil, err
		}
	}
	return s.Service.AppendEvent(ctx, request)
}

func TestControlInteractionIdleConditionRejectsRacedNativeFenceWithoutAppend(t *testing.T) {
	ctx := t.Context()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	composition := &runtimeComposition{sessions: &idleInteractionFenceRaceStore{Service: store, store: store}}
	service, err := appserver.NewCommandService(appserver.CommandServiceConfig{Authorizer: controlClientAllowAuthorizer{}, Operations: appserver.NewMemoryOperationStore(), Backend: &controlCommandBackend{composition: composition}})
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.RecordInteractionRequest{WriteBase: appserver.WriteBase{OperationID: "idle-command", SessionID: active.SessionID, ExpectedRevision: &active.Revision}, Text: "/help", RequireIdleRun: true}
	result, err := service.RecordInteraction(ctx, appserver.Principal{ID: "owner"}, request)
	if errorcode.CodeOf(err) != errorcode.Conflict || result.Outcome != appserver.OutcomeConflicted || result.Resource != nil {
		t.Fatalf("raced execution admitted an input or lost known no-effect: %+v, %v", result, err)
	}
	if events, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true}); err != nil || len(events) != 0 {
		t.Fatalf("conditional rejection appended an input: %+v, %v", events, err)
	}
	// Ordinary existing command display admission still overlaps a running Turn.
	request.OperationID, request.RequireIdleRun = "ordinary-command", false
	result, err = service.RecordInteraction(ctx, appserver.Principal{ID: "owner"}, request)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Resource == nil {
		t.Fatalf("ordinary native command admission changed: %+v, %v", result, err)
	}
}
