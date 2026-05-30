package control

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/engine/internal/teststore"
)

func TestParticipantRunnerInvokesAgentAndStoresCanonicalEvents(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	active, err := store.Create(ctx, session.StartRequest{
		AppName: "caelis",
		UserID:  "tester",
		Workspace: session.Workspace{
			Key: "repo",
			CWD: "/tmp/repo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgentSession{
		events: []session.Event{{
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("external answer")},
			},
		}},
	}
	runner := ParticipantRunner{
		Store: store,
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	}
	result, err := runner.Invoke(ctx, ParticipantRequest{
		SessionRef: active.Ref,
		Input:      "inspect",
		Participant: session.ParticipantBinding{
			ID:        "reviewer",
			AgentName: "reviewer",
			Label:     "Reviewer",
		},
		Agent: agent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteSessionID != "remote-1" {
		t.Fatalf("remote session id = %q, want remote-1", result.RemoteSessionID)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	event := result.Events[0]
	if event.SessionID != active.SessionID {
		t.Fatalf("event session id = %q, want local session", event.SessionID)
	}
	if event.Scope == nil || event.Scope.Participant.ID != "reviewer" || event.Scope.ACP.SessionID != "remote-1" {
		t.Fatalf("event scope = %#v, want participant and remote ACP session", event.Scope)
	}
	if got := session.EventText(event); got != "external answer" {
		t.Fatalf("event text = %q, want external answer", got)
	}
	if len(agent.prompts) != 1 || agent.prompts[0][0].Text != "inspect" {
		t.Fatalf("agent prompts = %#v, want inspect prompt", agent.prompts)
	}

	snapshot, err := store.Load(ctx, active.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || session.EventText(snapshot.Events[0]) != "external answer" {
		t.Fatalf("stored events = %#v, want external answer", snapshot.Events)
	}
}

type fakeAgentSession struct {
	initialized bool
	newSessions int
	prompts     [][]model.ContentPart
	events      []session.Event
}

func (a *fakeAgentSession) Initialize(context.Context) error {
	a.initialized = true
	return nil
}

func (a *fakeAgentSession) NewSession(context.Context, session.Workspace) (string, error) {
	a.newSessions++
	return "remote-1", nil
}

func (a *fakeAgentSession) Prompt(_ context.Context, _ string, parts []model.ContentPart) ([]session.Event, error) {
	a.prompts = append(a.prompts, model.CloneContentParts(parts))
	out := make([]session.Event, 0, len(a.events))
	for _, event := range a.events {
		out = append(out, session.CloneEvent(event))
	}
	return out, nil
}

func (a *fakeAgentSession) Close() error {
	return nil
}
