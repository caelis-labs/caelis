package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/agentcommunication"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestRuntimeRunPersistsOrderedAgentCommunicationBatchInOneTurn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "batch-user",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	orbit := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-orbit", Role: "delegated", Name: "orbit"}
	zenith := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-zenith", Role: "delegated", Name: "zenith"}
	probe := &durableIdentityModel{text: "ack both"}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		InputKind:  agent.SubmissionKindAgentCommunication,
		Inputs: []agent.AgentCommunicationInput{
			{Source: orbit, Input: "status from orbit"},
			{Source: zenith, Input: "status from zenith"},
		},
		AgentSpec: agent.AgentSpec{Name: "chat", Model: probe},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("drain error = %v", err)
	}

	wantUser := []model.Message{
		mustPrefixedAgentMessage(t, orbit, "status from orbit"),
		mustPrefixedAgentMessage(t, zenith, "status from zenith"),
	}
	if got := userMessages(probe.messages); !reflect.DeepEqual(got, wantUser) {
		t.Fatalf("runtime-produced model context = %#v, want %#v", got, wantUser)
	}

	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	persisted := contextEvents(loaded.Events)
	if len(persisted) != 2 {
		t.Fatalf("persisted Agent communication events = %d, want 2 in one Turn", len(persisted))
	}
	if persisted[0].Actor.ID != orbit.ID || persisted[1].Actor.ID != zenith.ID {
		t.Fatalf("durable sources = (%#v, %#v), want orbit then zenith", persisted[0].Actor, persisted[1].Actor)
	}
	if persisted[0].Scope == nil || persisted[1].Scope == nil || persisted[0].Scope.TurnID == "" ||
		persisted[0].Scope.TurnID != persisted[1].Scope.TurnID {
		t.Fatalf("batch events used different Turns: (%#v, %#v)", persisted[0].Scope, persisted[1].Scope)
	}
	if persisted[0].IdempotencyKey == persisted[1].IdempotencyKey {
		t.Fatalf("batch events reused idempotency key %q", persisted[0].IdempotencyKey)
	}

	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []session.Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	replayed := make([]*session.Event, 0, len(decoded))
	for i := range decoded {
		migrated, err := session.MigrateEvent(decoded[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := session.ValidateDurableCoreEvent(&migrated); err != nil {
			t.Fatal(err)
		}
		replayed = append(replayed, &migrated)
	}
	got := make([]model.Message, 0, len(replayed))
	for _, event := range replayed {
		message, ok := session.ModelMessageOf(event)
		if !ok {
			t.Fatalf("replayed event has no model message: %#v", event)
		}
		got = append(got, message)
	}
	if !reflect.DeepEqual(got, wantUser) {
		t.Fatalf("rebuilt model context = %#v, want runtime-produced %#v", got, wantUser)
	}
}

func mustPrefixedAgentMessage(t *testing.T, actor session.ActorRef, text string) model.Message {
	t.Helper()
	prefixed, err := agentcommunication.PrefixMessage(model.NewTextMessage(model.RoleUser, text), actor)
	if err != nil {
		t.Fatal(err)
	}
	return prefixed
}

func userMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == model.RoleUser {
			out = append(out, message)
		}
	}
	return out
}

func contextEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event != nil && session.EventTypeOf(event) == session.EventTypeContext {
			out = append(out, event)
		}
	}
	return out
}

func TestRuntimeRunRejectsMixedSingularAndBatchAgentCommunication(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "mixed-user",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	actor := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "orbit"}
	_, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		InputKind:  agent.SubmissionKindAgentCommunication,
		Input:      "singular",
		InputActor: actor,
		Inputs:     []agent.AgentCommunicationInput{{Source: actor, Input: "batch"}},
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: staticModel{text: "no"}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("Run(mixed) error = %v, want combined-field rejection", err)
	}
}
