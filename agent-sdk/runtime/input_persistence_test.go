package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

// Fail validation of the second input inside the real Store. Individual
// appends would have committed the first input before discovering this fault.
type invalidSecondInputStore struct {
	*sessionfile.Store
	fail   bool
	inputs int
}

func (s *invalidSecondInputStore) prepare(event *session.Event) *session.Event {
	event = session.CloneEvent(event)
	if session.EventTypeOf(event) == session.EventTypeContext {
		s.inputs++
		if s.fail && s.inputs == 2 {
			return nil
		}
	}
	return event
}

func (s *invalidSecondInputStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	req.Event = s.prepare(req.Event)
	return s.Store.AppendEvent(ctx, req)
}

func (s *invalidSecondInputStore) AppendEvents(ctx context.Context, req session.AppendEventsRequest) ([]*session.Event, error) {
	req.Events = session.CloneEvents(req.Events)
	for i := range req.Events {
		req.Events[i] = s.prepare(req.Events[i])
	}
	return s.Store.AppendEvents(ctx, req)
}

func TestInputBatchPersistenceFailureAndRoundTrip(t *testing.T) {
	for _, path := range []string{"kernel", "controller"} {
		for _, fail := range []bool{false, true} {
			name := "roundtrip"
			if fail {
				name = "second-input-failure"
			}
			t.Run(path+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				store := &invalidSecondInputStore{Store: sessionfile.NewStore(sessionfile.Config{RootDir: root}), fail: fail}
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "batch-test"})
				if err != nil {
					t.Fatal(err)
				}
				if path == "controller" {
					active, err = store.BindController(t.Context(), session.BindControllerRequest{
						SessionRef: active.SessionRef,
						Binding:    session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "remote", EpochID: "epoch"},
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				inputs := []agent.AgentCommunicationInput{
					{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "first", Name: "first"}, Input: "first input"},
					{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "second", Name: "second"}, Input: "second input"},
				}
				probe := &durableIdentityModel{text: "ack"}
				remoteCalls := 0
				var remoteParts []model.ContentPart
				rt, err := New(testConfigWithACPForwarder(Config{
					Sessions: store, AgentFactory: chat.Factory{},
					Controllers: stubACPController{runTurn: func(_ context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
						remoteCalls++
						remoteParts = req.ContentParts
						handle := newTestControllerTurnHandle(nil)
						handle.finish()
						return controller.TurnResult{Handle: handle}, nil
					}},
				}))
				if err != nil {
					t.Fatal(err)
				}
				var result agent.RunResult
				result, err = rt.Run(t.Context(), agent.RunRequest{
					SessionRef: active.SessionRef, InputKind: agent.SubmissionKindAgentCommunication, Inputs: inputs,
					AgentSpec: agent.AgentSpec{Name: "chat", Model: probe},
				})
				if err == nil {
					_, err = drainRunnerEvents(t, result.Handle)
				}
				if (err != nil) != fail {
					t.Fatalf("error = %v, want failure %v", err, fail)
				}
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				loaded, loadErr := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				events := contextEvents(loaded.Events)
				if fail {
					if len(events) != 0 {
						t.Fatalf("partial input survived reopen: %#v", events)
					}
					if remoteCalls != 0 || len(probe.messages) != 0 {
						t.Fatal("failed batch reached model/controller")
					}
					return
				}
				if len(events) != 2 {
					t.Fatalf("reopened input count = %d", len(events))
				}
				var rebuilt []model.Message
				var rebuiltParts []model.ContentPart
				for i, event := range events {
					message, ok := session.ModelMessageOf(event)
					if !ok {
						t.Fatal("missing canonical model message")
					}
					if !reflect.DeepEqual(event.Actor, inputs[i].Source) {
						t.Fatalf("source %d changed: %#v", i, event.Actor)
					}
					rebuilt = append(rebuilt, message)
					rebuiltParts = append(rebuiltParts, model.ContentPartsFromParts(message.Parts)...)
				}
				want := []model.Message{mustPrefixedAgentMessage(t, inputs[0].Source, inputs[0].Input), mustPrefixedAgentMessage(t, inputs[1].Source, inputs[1].Input)}
				if !reflect.DeepEqual(rebuilt, want) {
					t.Fatalf("rebuilt context = %#v, want %#v", rebuilt, want)
				}
				if path == "kernel" && !reflect.DeepEqual(rebuilt, userMessages(probe.messages)) {
					t.Fatal("reopened context differs from model input")
				}
				if path == "controller" && (remoteCalls != 1 || !reflect.DeepEqual(rebuiltParts, remoteParts)) {
					t.Fatal("reopened context differs from controller input")
				}
			})
		}
	}
}

type singularInputStore struct{ session.Service }

func TestInputBatchPersistenceRequiresAtomicStoreAndExactFence(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "batch-test"})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := buildRunInputEvents(active, "turn", agent.RunRequest{
		InputKind: agent.SubmissionKindAgentCommunication,
		Inputs: []agent.AgentCommunicationInput{
			{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "a"}, Input: "one"},
			{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "b"}, Input: "two"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{sessions: singularInputStore{store}}
	if _, err = rt.appendInputEvents(t.Context(), active.SessionRef, inputs); !errorcode.Is(err, errorcode.Unsupported) {
		t.Fatalf("singular store error = %v", err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	if err = store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	rt.sessions = store
	if _, err = rt.appendInputEvents(ctx, active.SessionRef, inputs); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale fence error = %v", err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(contextEvents(loaded.Events)) != 0 {
		t.Fatal("rejected input was persisted through fallback")
	}
}
