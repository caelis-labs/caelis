package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func TestSavePersistsConfigurationAndHistory(t *testing.T) {
	root, store, active, id := notebookSaveFixture(t)
	service := &Service{Sessions: store}
	config := Config{Name: "Legacy", Model: "model-a"}
	message := model.NewTextMessage(model.RoleUser, "Keep this exact conversation input.")
	_, err := store.AppendEventsAndUpdateState(t.Context(), session.AppendEventsAndUpdateStateRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		TransactionID: "legacy", MutationDigest: "legacy",
		Events: []*session.Event{{ID: "original-input", Type: session.EventTypeUser, Message: &message}},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			return map[string]any{StateKey: record{Version: 1, ID: id, Config: config}, "unrelated": "preserved"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	active = notebookSession(t, store, active.SessionRef)
	before, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var appendedExpectation string
	for index, next := range []Config{
		{Name: "Renamed", Description: "Use short replies.", Model: "model-a"},
		{Name: "Renamed", Description: "Use short replies.", Model: "model-b"},
	} {
		operation := []string{"rename", "model"}[index]
		if index == 0 {
			appendedExpectation = ConfigurationMessage(next)
		}
		active, err = service.Save(t.Context(), active, id, next, &config, operation, operation)
		if err != nil {
			t.Fatal(err)
		}
		config = next
		value, err := service.GetBot(t.Context(), id)
		if err != nil || value.Config != next || value.ID != id {
			t.Fatalf("saved Bot = %+v, %v", value, err)
		}
	}
	reopened := file.NewStore(file.Config{RootDir: root})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	value, err := (&Service{Sessions: reopened}).GetBot(t.Context(), id)
	if err != nil || value.ID != id || value.SessionID != active.SessionID || value.Config != config {
		t.Fatalf("reopened Bot = %+v, %v", value, err)
	}
	if !reflect.DeepEqual(loaded.Events[:len(before.Events)], before.Events) || loaded.State["unrelated"] != "preserved" {
		t.Fatal("Save rewrote history or unrelated state")
	}
	// One canonical user event per saved name/description change; a model-only
	// edit appends none and rewrites no prefix.
	if len(loaded.Events) != len(before.Events)+1 {
		t.Fatalf("events = %d, want one appended user settings event", len(loaded.Events))
	}
	appended := loaded.Events[len(before.Events)]
	if appended.Type != session.EventTypeUser || appended.Actor.Kind != session.ActorKindUser || appended.Actor.ID != active.UserID || session.EventText(appended) != appendedExpectation {
		t.Fatalf("settings event lost canonical user provenance: %+v", appended)
	}
	for _, event := range loaded.Events {
		if event.Type == session.EventTypeCompact {
			t.Fatal("Save compacted history")
		}
	}
}

func TestNewBotSaveIsTransactional(t *testing.T) {
	for _, mode := range []string{"success", "transaction_failure", "committed_read_failure"} {
		t.Run(mode, func(t *testing.T) {
			root, store, active, id := notebookSaveFixture(t)
			fault := &notebookSaveFault{Service: store, failState: mode == "transaction_failure", failRead: mode == "committed_read_failure"}
			service := &Service{Sessions: fault}
			config := Config{Name: "New"}
			_, saveErr := service.Save(t.Context(), active, id, config, nil, "create", "create")
			reopened := file.NewStore(file.Config{RootDir: root})
			loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "transaction_failure" {
				if saveErr == nil || session.IsCommitted(saveErr) || len(loaded.Events) != 0 || loaded.State[StateKey] != nil {
					t.Fatalf("failed transaction left partial admission: %+v, %v", loaded, saveErr)
				}
				return
			}
			if (mode == "success" && saveErr != nil) || (mode == "committed_read_failure" && !session.IsCommitted(saveErr)) {
				t.Fatalf("Save error = %v for %s", saveErr, mode)
			}
			stored, err := Decode(loaded.State)
			if err != nil || stored != config || len(loaded.Events) != 1 || session.EventText(loaded.Events[0]) != ConfigurationMessage(config) {
				t.Fatalf("new Bot admission did not recover together: %+v, %+v, %v", loaded, stored, err)
			}
		})
	}
}

func TestSaveRejectsStaleAndUnsupportedRecords(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale", true: "unsupported_version"}[unsupported], func(t *testing.T) {
			_, store, active, id := notebookSaveFixture(t)
			config := Config{Name: "Legacy"}
			stored := map[string]any{"version": 1, "id": id, "config": config}
			if unsupported {
				stored["version"] = 99
			}
			_, err := store.UpdateState(t.Context(), session.UpdateStateRequest{
				SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
				Update: func(map[string]any) (map[string]any, error) { return map[string]any{StateKey: stored}, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if unsupported {
				active = before.Session
			}
			_, err = (&Service{Sessions: store}).Save(t.Context(), active, id, config, &config, "enable", "enable")
			if err == nil || session.IsCommitted(err) {
				t.Fatalf("invalid admission Save = %v", err)
			}
			after, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected admission changed Session: %v", err)
			}
		})
	}
}

func notebookSaveFixture(t *testing.T) (string, *file.Store, session.Session, string) {
	t.Helper()
	root := t.TempDir()
	store := file.NewStore(file.Config{RootDir: root})
	id := Identity("owner", "create")
	sessionID, err := ConversationID(id)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: sessionID,
		Metadata: map[string]any{MetadataID: id, sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentBot},
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, store, active, id
}

func notebookSession(t *testing.T, sessions session.Service, ref session.SessionRef) session.Session {
	t.Helper()
	active, err := sessions.Session(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

type notebookSaveFault struct {
	session.Service
	failState, failRead, committed bool
}

func (s *notebookSaveFault) AppendEventsAndUpdateState(ctx context.Context, req session.AppendEventsAndUpdateStateRequest) ([]*session.Event, error) {
	if s.failState {
		req.UpdateState = func([]*session.Event, map[string]any) (map[string]any, error) {
			return nil, errors.New("injected transaction failure")
		}
	}
	events, err := s.Service.(session.EventBatchStateService).AppendEventsAndUpdateState(ctx, req)
	s.committed = err == nil || session.IsCommitted(err)
	return events, err
}

func (s *notebookSaveFault) Session(ctx context.Context, ref session.SessionRef) (session.Session, error) {
	if s.failRead && s.committed {
		return session.Session{}, errors.New("injected post-commit read failure")
	}
	return s.Service.Session(ctx, ref)
}
