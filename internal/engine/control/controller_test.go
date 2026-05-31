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
	if len(snapshot.Events) != 4 {
		t.Fatalf("stored events = %d, want lifecycle start/remote, response, lifecycle completed", len(snapshot.Events))
	}
	started := snapshot.Events[0]
	if started.Type != session.EventLifecycle || started.Lifecycle == nil || started.Lifecycle.Status != session.LifecycleRunning {
		t.Fatalf("started lifecycle = %#v, want running lifecycle event", started)
	}
	if meta := session.RuntimeControllerMeta(started.Meta); meta["phase"] != string(ControllerInvocationStarted) || meta["run_id"] == "" {
		t.Fatalf("started lifecycle meta = %#v, want controller start metadata", meta)
	}
	remote := snapshot.Events[1]
	if remote.Type != session.EventLifecycle || remote.Lifecycle == nil || remote.Lifecycle.Status != session.LifecycleRunning {
		t.Fatalf("remote lifecycle = %#v, want running lifecycle event", remote)
	}
	if meta := session.RuntimeControllerMeta(remote.Meta); meta["phase"] != string(ControllerInvocationRemoteSession) || meta["remote_session_id"] != "remote-1" {
		t.Fatalf("remote lifecycle meta = %#v, want remote session metadata", meta)
	}
	event := snapshot.Events[2]
	if event.Scope == nil || event.Scope.Controller.Kind != session.ControllerACP || event.Scope.Controller.ID != "reviewer" || event.Scope.ACP.SessionID != "remote-1" {
		t.Fatalf("stored event scope = %#v, want controller and remote ACP session", event.Scope)
	}
	if event.Actor.Kind != session.ActorController || event.Actor.ID != "reviewer" {
		t.Fatalf("event actor = %#v, want controller actor", event.Actor)
	}
	if event.Time != clock {
		t.Fatalf("event time = %s, want %s", event.Time, clock)
	}
	completed := snapshot.Events[3]
	if completed.Type != session.EventLifecycle || completed.Lifecycle == nil || completed.Lifecycle.Status != session.LifecycleCompleted {
		t.Fatalf("completed lifecycle = %#v, want completed lifecycle event", completed)
	}
	if meta := session.RuntimeControllerMeta(completed.Meta); meta["phase"] != string(ControllerInvocationCompleted) || meta["remote_session_id"] != "remote-1" {
		t.Fatalf("completed lifecycle meta = %#v, want completed controller metadata", meta)
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

func TestControllerRunnerResumesAndAppliesRemoteConfigOptions(t *testing.T) {
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
	agent := &fakeConfigurableAgentSession{
		fakeAgentSession: fakeAgentSession{
			events: []session.Event{{
				Type: session.EventAssistant,
				Message: &model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("configured controller response")},
				},
			}},
		},
		options: []ConfigOption{{
			Type:         "select",
			ID:           "model",
			Category:     "model",
			CurrentValue: "gpt-old",
			Options:      []ConfigChoice{{Value: "gpt-old", Name: "Old"}, {Value: "gpt-next", Name: "Next"}},
		}, {
			Type:         "select",
			ID:           "reasoning_effort",
			Category:     "thought_level",
			CurrentValue: "low",
			Options:      []ConfigChoice{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}},
		}, {
			Type:         "select",
			ID:           "mode",
			Category:     "mode",
			CurrentValue: "ask",
			Options:      []ConfigChoice{{Value: "ask", Name: "Ask"}, {Value: "code", Name: "Code"}},
		}},
	}
	runner := ControllerRunner{Store: store}
	result, err := runner.Invoke(ctx, ControllerRequest{
		SessionRef: active.Ref,
		Controller: session.ControllerBinding{
			Kind:            session.ControllerACP,
			ID:              "reviewer",
			RemoteSessionID: "remote-existing",
		},
		ControllerModel:           "gpt-next",
		ControllerReasoningEffort: "high",
		ControllerMode:            "code",
		Input:                     "continue",
		Agent:                     agent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.resumedSessionID != "remote-existing" || agent.newSessions != 0 {
		t.Fatalf("resume/new = %q/%d, want resume existing without new session", agent.resumedSessionID, agent.newSessions)
	}
	wantSets := []string{"model=gpt-next", "reasoning_effort=high", "mode=code"}
	if len(agent.sets) != len(wantSets) {
		t.Fatalf("set config calls = %#v, want %#v", agent.sets, wantSets)
	}
	for i, want := range wantSets {
		if agent.sets[i] != want {
			t.Fatalf("set config call %d = %q, want %q", i, agent.sets[i], want)
		}
	}
	if len(result.ConfigOptions) != 3 || result.ConfigOptions[0].CurrentValue != "gpt-next" || result.ConfigOptions[1].CurrentValue != "high" || result.ConfigOptions[2].CurrentValue != "code" {
		t.Fatalf("result config options = %#v, want applied current values", result.ConfigOptions)
	}
	if len(result.Events) != 1 || result.Events[0].Meta["controller_config_options"] == nil {
		t.Fatalf("result events = %#v, want controller config options metadata", result.Events)
	}
}

type fakeConfigurableAgentSession struct {
	fakeAgentSession
	options          []ConfigOption
	resumedSessionID string
	sets             []string
}

func (a *fakeConfigurableAgentSession) NewSessionState(context.Context, session.Workspace) (AgentSessionState, error) {
	a.newSessions++
	return AgentSessionState{RemoteSessionID: "remote-1", ConfigOptions: cloneConfigOptions(a.options)}, nil
}

func (a *fakeConfigurableAgentSession) ResumeSessionState(_ context.Context, sessionID string, _ session.Workspace) (AgentSessionState, error) {
	a.resumedSessionID = sessionID
	return AgentSessionState{RemoteSessionID: sessionID, ConfigOptions: cloneConfigOptions(a.options)}, nil
}

func (a *fakeConfigurableAgentSession) SetConfigOption(_ context.Context, sessionID string, configID string, value any) (AgentSessionState, error) {
	text, _ := value.(string)
	a.sets = append(a.sets, configID+"="+text)
	for i := range a.options {
		if a.options[i].ID == configID {
			a.options[i].CurrentValue = text
		}
	}
	return AgentSessionState{RemoteSessionID: sessionID, ConfigOptions: cloneConfigOptions(a.options)}, nil
}
