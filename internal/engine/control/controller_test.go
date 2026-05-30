package control

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/engine/internal/teststore"
)

func TestControllerRunnerInvokesAgentAndStoresCanonicalEvents(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	active, err := store.Create(ctx, session.StartRequest{
		AppName:   "caelis",
		UserID:    "tester",
		Workspace: session.Workspace{Key: "repo", CWD: "/repo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	runner := ControllerRunner{
		Store: store,
		Now:   func() time.Time { return clock },
	}
	result, err := runner.Invoke(ctx, ControllerRequest{
		SessionRef: active.Ref,
		Controller: session.ControllerBinding{
			Kind:      session.ControllerACP,
			ID:        "reviewer",
			AgentName: "reviewer",
			Label:     "Reviewer",
		},
		Input: "inspect",
		Agent: &fakeAgentSession{
			events: []session.Event{{
				Type: session.EventAssistant,
				Message: &model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("controller response")},
				},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteSessionID != "remote-1" || len(result.Events) != 1 {
		t.Fatalf("result = %#v, want one remote controller event", result)
	}
	snapshot, err := store.Load(ctx, active.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(snapshot.Events))
	}
	event := snapshot.Events[0]
	if event.Scope == nil || event.Scope.Controller.Kind != session.ControllerACP || event.Scope.Controller.ID != "reviewer" || event.Scope.ACP.SessionID != "remote-1" {
		t.Fatalf("stored event scope = %#v, want controller and remote ACP session", event.Scope)
	}
	if event.Actor.Kind != session.ActorController || event.Actor.ID != "reviewer" {
		t.Fatalf("event actor = %#v, want controller actor", event.Actor)
	}
	if event.Time != clock {
		t.Fatalf("event time = %s, want %s", event.Time, clock)
	}
}

func TestControllerRunnerReusesRemoteSessionID(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	active, err := store.Create(ctx, session.StartRequest{
		AppName:   "caelis",
		UserID:    "tester",
		Workspace: session.Workspace{Key: "repo", CWD: "/repo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgentSession{
		events: []session.Event{{
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("continued controller response")},
			},
		}},
	}
	runner := ControllerRunner{Store: store}
	result, err := runner.Invoke(ctx, ControllerRequest{
		SessionRef: active.Ref,
		Controller: session.ControllerBinding{
			Kind:            session.ControllerACP,
			ID:              "reviewer",
			AgentName:       "reviewer",
			Label:           "Reviewer",
			RemoteSessionID: "remote-existing",
		},
		Input: "continue",
		Agent: agent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.newSessions != 0 {
		t.Fatalf("new sessions = %d, want 0 for existing remote session", agent.newSessions)
	}
	if len(agent.promptSessionIDs) != 1 || agent.promptSessionIDs[0] != "remote-existing" {
		t.Fatalf("prompt session ids = %#v, want remote-existing", agent.promptSessionIDs)
	}
	if result.RemoteSessionID != "remote-existing" {
		t.Fatalf("result remote session id = %q, want remote-existing", result.RemoteSessionID)
	}
	if len(result.Events) != 1 || result.Events[0].Scope == nil || result.Events[0].Scope.Controller.RemoteSessionID != "remote-existing" {
		t.Fatalf("result events = %#v, want reused remote controller scope", result.Events)
	}
}
