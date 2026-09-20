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

func TestNotebookSavePersistsExplicitAdmissionAndHistory(t *testing.T) {
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
	for index, next := range []Config{
		{Name: "Renamed", Description: "Use short replies.", Model: "model-a"},
		{Name: "Renamed", Description: "Use short replies.", Model: "model-b"},
	} {
		operation := []string{"rename", "model"}[index]
		active, err = service.Save(t.Context(), active, id, next, &config, false, operation, operation)
		if err != nil {
			t.Fatal(err)
		}
		config = next
		value, err := service.GetBot(t.Context(), id)
		if err != nil || value.NotebookEnabled {
			t.Fatalf("ordinary Save enabled legacy Bot: %+v, %v", value, err)
		}
	}
	active, err = service.Save(t.Context(), active, id, config, &config, true, "enable", "enable")
	if err != nil {
		t.Fatal(err)
	}
	// A second explicit enable and an ordinary model edit preserve admission,
	// without appending a second enable event.
	active, err = service.Save(t.Context(), active, id, config, &config, true, "enabled-again", "enabled-again")
	if err != nil {
		t.Fatal(err)
	}
	next := config
	next.Model = "model-c"
	if _, err := service.Save(t.Context(), active, id, next, &config, false, "enabled-model", "enabled-model"); err != nil {
		t.Fatal(err)
	}
	reopened := file.NewStore(file.Config{RootDir: root})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	value, err := (&Service{Sessions: reopened}).GetBot(t.Context(), id)
	if err != nil || !value.NotebookEnabled || value.ID != id || value.SessionID != active.SessionID || value.Config != next {
		t.Fatalf("reopened notebook Bot = %+v, %v", value, err)
	}
	if !reflect.DeepEqual(loaded.Events[:len(before.Events)], before.Events) || loaded.State["unrelated"] != "preserved" {
		t.Fatal("notebook Save rewrote history or unrelated state")
	}
	if len(loaded.Events) != 3 {
		t.Fatalf("events = %d, want original input, rename, and one enable", len(loaded.Events))
	}
	enabled := loaded.Events[2]
	if enabled.Type != session.EventTypeUser || enabled.Actor.Kind != session.ActorKindUser || enabled.Actor.ID != active.UserID || session.EventText(enabled) != NotebookEnableMessage() {
		t.Fatalf("enable event lost canonical user provenance: %+v", enabled)
	}
	for _, event := range loaded.Events {
		if event.Type == session.EventTypeCompact {
			t.Fatal("notebook admission compacted history")
		}
	}
}

func TestNewBotNotebookAdmissionIsDefaultAndTransactional(t *testing.T) {
	for _, mode := range []string{"success", "transaction_failure", "committed_read_failure"} {
		t.Run(mode, func(t *testing.T) {
			root, store, active, id := notebookSaveFixture(t)
			fault := &notebookSaveFault{Service: store, failState: mode == "transaction_failure", failRead: mode == "committed_read_failure"}
			service := &Service{Sessions: fault}
			_, saveErr := service.Save(t.Context(), active, id, Config{Name: "New"}, nil, false, "create", "create")
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
			enabled, err := NotebookEnabled(loaded.State)
			if err != nil || !enabled || len(loaded.Events) != 2 || session.EventText(loaded.Events[1]) != NotebookEnableMessage() {
				t.Fatalf("new Bot admission did not recover together: %+v, %v", loaded, err)
			}
		})
	}
}

func TestNotebookSaveRejectsStaleRevisionAndUnknownCapability(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale", true: "unknown_version"}[unknown], func(t *testing.T) {
			_, store, active, id := notebookSaveFixture(t)
			config := Config{Name: "Legacy"}
			stored := record{Version: 1, ID: id, Config: config}
			if unknown {
				stored.NotebookVersion = 99
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
			if unknown {
				active = before.Session
			}
			_, err = (&Service{Sessions: store}).Save(t.Context(), active, id, config, &config, true, "enable", "enable")
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
