package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestActiveInputBatchPersistenceFailureAndRoundTrip(t *testing.T) {
	for _, path := range []string{"kernel", "controller"} {
		for _, fail := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/roundtrip", true: "/second-input-failure"}[fail], func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				root := t.TempDir()
				store := &invalidSecondInputStore{Store: sessionfile.NewStore(sessionfile.Config{RootDir: root}), fail: fail}
				active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "active-batch-test"})
				if err != nil {
					t.Fatal(err)
				}
				if path == "controller" {
					active, err = store.BindController(ctx, session.BindControllerRequest{SessionRef: active.SessionRef,
						Binding: session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "remote", EpochID: "epoch", RemoteSessionID: "remote-session"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "test-host"})
				if err != nil {
					t.Fatal(err)
				}
				ctx = session.ContextWithRuntimeFence(ctx, fence)
				defer func() {
					if err := store.ReleaseSessionFence(context.WithoutCancel(ctx), session.SessionFenceReleaseRequest(fence)); err != nil {
						t.Errorf("release test fence: %v", err)
					}
				}()
				inputs := []agent.AgentCommunicationInput{
					{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "one", Name: "one"}, Input: "first report", DisplayInput: "First report", MessageID: "mail-one"},
					{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "two", Name: "two"}, Input: "second report", DisplayInput: "Second report", MessageID: "mail-two"},
				}
				probe := &steerRuntimeModel{started: make(chan struct{}), releaseFirst: make(chan struct{})}
				remoteHandle := newTestControllerTurnHandle(nil)
				remoteStarted := make(chan struct{})
				var remoteParts []model.ContentPart
				var remoteTurn string
				remoteCalls := 0
				backend := steeringACPController{
					stubACPController: stubACPController{runTurn: func(_ context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
						remoteTurn = req.TurnID
						close(remoteStarted)
						return controller.TurnResult{Handle: remoteHandle}, nil
					}},
					steerController: func(_ context.Context, req controller.ControllerSteerRequest) error {
						remoteCalls++
						if req.TurnID != remoteTurn {
							t.Errorf("steering changed Turn")
						}
						remoteParts = req.ContentParts
						return req.Commit()
					},
				}
				rt, err := New(testConfigWithACPForwarder(Config{Sessions: store, AgentFactory: chat.Factory{}, Controllers: backend}))
				if err != nil {
					t.Fatal(err)
				}
				capture := &testSourceCapture{}
				result, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "initial", SourceObserver: capture, AgentSpec: agent.AgentSpec{Name: "chat", Model: probe}})
				if err != nil {
					t.Fatal(err)
				}
				started := probe.started
				if path == "controller" {
					started = remoteStarted
				}
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("initial Turn did not start")
				}
				err = result.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindAgentCommunication, Inputs: inputs})
				if path == "kernel" {
					if err != nil {
						t.Fatal(err)
					}
					close(probe.releaseFirst)
					err = result.Handle.WaitCompletion(ctx)
				} else {
					remoteHandle.finish()
					if waitErr := result.Handle.WaitCompletion(ctx); waitErr != nil {
						t.Fatal(waitErr)
					}
				}
				if (err != nil) != fail {
					t.Fatalf("error = %v, want failure %v", err, fail)
				}
				loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				events := contextEvents(loaded.Events)
				if fail {
					if len(events) != 0 || len(contextEvents(capture.Events())) != 0 {
						t.Fatal("partial batch was persisted or published")
					}
					if path == "kernel" && len(probe.Requests()) != 1 {
						t.Fatal("failed batch reached model")
					}
					return
				}
				if len(events) != 2 {
					t.Fatalf("input events = %d", len(events))
				}
				var rebuilt []model.Message
				var parts []model.ContentPart
				for i, event := range events {
					if !reflect.DeepEqual(event.Actor, inputs[i].Source) || event.Scope.TurnID != events[0].Scope.TurnID {
						t.Fatalf("source/Turn changed: %#v", event)
					}
					if session.EventMessageID(event) != inputs[i].MessageID {
						t.Fatalf("reopened correlation = %q, want %q", session.EventMessageID(event), inputs[i].MessageID)
					}
					message, _ := session.ModelMessageOf(event)
					rebuilt = append(rebuilt, message)
					parts = append(parts, model.ContentPartsFromParts(message.Parts)...)
				}
				if path == "kernel" {
					requests := probe.Requests()
					if len(requests) != 2 {
						t.Fatalf("model calls = %d", len(requests))
					}
					messages := userMessages(requests[1].Messages)
					if len(messages) != 3 || !reflect.DeepEqual(messages[1:], rebuilt) {
						t.Fatal("reopened context differs from live model input")
					}
				} else if remoteCalls != 1 || !reflect.DeepEqual(parts, remoteParts) {
					t.Fatal("batch did not use one canonical controller steering request")
				}
			})
		}
	}
}

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
					{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "first", Name: "first"}, Input: "first input", MessageID: "mail-first"},
					{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "second", Name: "second"}, Input: "second input", MessageID: "mail-second"},
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
					if session.EventMessageID(event) != inputs[i].MessageID {
						t.Fatalf("reopened correlation %d = %q, want %q", i, session.EventMessageID(event), inputs[i].MessageID)
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

// The embedding's optional correlation identity travels only with Agent
// communication. Ordinary conversation input keeps its existing identity shape,
// an admission without a supplied correlation stays empty, and the correlation
// never becomes the Turn or idempotency identity.
func TestBuildRunInputEventsCarriesSuppliedAgentCorrelationOnly(t *testing.T) {
	active := session.Session{SessionRef: session.SessionRef{SessionID: "correlation"}}
	conversation, err := buildRunInputEvents(active, "turn-conversation", agent.RunRequest{
		InputKind: agent.SubmissionKindConversation, Input: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation) != 1 || session.EventMessageID(conversation[0]) != "" {
		t.Fatalf("conversation identity = %#v, want no correlation", conversation)
	}
	actor := session.ActorRef{Kind: session.ActorKindParticipant, ID: "orbit", Name: "orbit"}
	batch, err := buildRunInputEvents(active, "turn-batch", agent.RunRequest{
		InputKind: agent.SubmissionKindAgentCommunication,
		Inputs: []agent.AgentCommunicationInput{
			{Source: actor, Input: "first", MessageID: "mail-1"},
			{Source: actor, Input: "second"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || session.EventMessageID(batch[0]) != "mail-1" || session.EventMessageID(batch[1]) != "" {
		t.Fatalf("agent correlations = %#v", batch)
	}
	for _, event := range batch {
		if strings.Contains(event.IdempotencyKey, "mail-1") {
			t.Fatalf("correlation leaked into Turn identity: %q", event.IdempotencyKey)
		}
	}
}
