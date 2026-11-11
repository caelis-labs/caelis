package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type sessionCancelRunner struct {
	mu               sync.Mutex
	spawnState       delegation.State
	spawnContext     subagent.SpawnContext
	producerContext  context.Context
	entered          chan struct{}
	release          chan struct{}
	cancelCalls      int
	completeOnCancel bool
	waitState        delegation.State
}

func (r *sessionCancelRunner) Spawn(ctx context.Context, spawn subagent.SpawnContext, _ delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.mu.Lock()
	r.spawnContext, r.producerContext = spawn, ctx
	state := r.spawnState
	if state == "" {
		state = delegation.StateRunning
	}
	r.mu.Unlock()
	if r.entered != nil {
		close(r.entered)
		<-r.release // Exercise a producer that returns its identity after cancellation.
	}
	return delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child-" + spawn.TaskID, AgentID: "agent-" + spawn.TaskID}, delegation.Result{TaskID: spawn.TaskID, State: state, Running: state == delegation.StateRunning, Result: "retained child result"}, nil
}

func (r *sessionCancelRunner) Cancel(_ context.Context, _ delegation.Anchor) error {
	r.mu.Lock()
	r.cancelCalls++
	completion, complete := r.spawnContext.Completion, r.completeOnCancel
	r.mu.Unlock()
	if complete {
		completion.PublishSubagentCompletion(delegation.Result{State: delegation.StateCancelled})
	}
	return nil
}

func (r *sessionCancelRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.waitState
	if state == "" {
		state = delegation.StateRunning
	}
	return delegation.Result{State: state, Running: state == delegation.StateRunning}, nil
}

func startSessionCancelChild(t *testing.T, r *Runtime, active session.Session, runner subagent.Runner, spawnID string) taskapi.Snapshot {
	t.Helper()
	active, err := r.ensureSessionController(t.Context(), active)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{SpawnID: spawnID, Agent: "helper", Prompt: "inspect", Role: session.ParticipantRoleSidecar})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCancelSessionSubagentsRetainsProducerUntilTerminal(t *testing.T) {
	for _, synchronousCompletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending producer", true: "synchronous completion"}[synchronousCompletion], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runner := &sessionCancelRunner{completeOnCancel: synchronousCompletion}
				r, active := newSubagentTaskTestRuntime(t, runner)
				started := startSessionCancelChild(t, r, active, runner, "child")
				if runner.producerContext.Err() != nil {
					t.Fatal("Spawn return cancelled the live producer context")
				}
				parent := newRunner(t.Context(), "new-parent", func() {}, nil)
				r.registerActiveRun(active.SessionRef, active, "new-parent-turn", parent)
				endpoints, err := r.CancelSessionSubagents(t.Context(), active.SessionRef)
				if err != nil || len(endpoints) != 1 || endpoints[0].EndpointKey != started.Ref.TaskID || endpoints[0].SessionID != started.Ref.SessionID {
					t.Fatalf("cancel endpoints = %#v, %v", endpoints, err)
				}
				entry, err := r.tasks.store.Get(t.Context(), started.Ref.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if synchronousCompletion {
					if entry.Running || entry.State != taskapi.StateCancelled {
						t.Fatalf("producer completion lost: %#v", entry)
					}
				} else {
					if !entry.Running || entry.State != taskapi.StateUnknownOutcome || taskStringValue(entry.Metadata[subagentCancelPhaseKey]) != string(subagentCancelPhaseApplied) {
						t.Fatalf("cancel fabricated producer termination: %#v", entry)
					}
					if _, err := r.CancelSessionSubagents(t.Context(), active.SessionRef); err != nil {
						t.Fatal(err)
					}
					runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{State: delegation.StateCancelled})
				}
				synctest.Wait()
				if runner.cancelCalls != 1 {
					t.Fatalf("remote Cancel calls = %d, want one durable claim", runner.cancelCalls)
				}
				if queued := parent.drainSubmissions(); len(queued) != 0 {
					t.Fatalf("cancelled child woke a later parent: %#v", queued)
				}
				if _, err := r.tasks.Read(t.Context(), active.SessionRef, taskapi.ControlRequest{TaskID: started.Ref.TaskID, Principal: session.ActorKindController}); err != nil {
					t.Fatalf("cancelled Task history unavailable: %v", err)
				}
			})
		})
	}
}

func TestCancelSessionSubagentsRevokesParentInputAdmissions(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runner := &sessionCancelRunner{spawnState: delegation.StateCompleted}
				r, active := newSubagentTaskTestRuntime(t, runner)
				started := startSessionCancelChild(t, r, active, runner, "source")
				current, err := r.sessions.Session(t.Context(), active.SessionRef)
				if err != nil {
					t.Fatal(err)
				}
				source := current.Participants[0]
				entered, sent := make(chan struct{}), make(chan error, 1)
				parent := func(ctx context.Context) error {
					close(entered)
					<-ctx.Done() // A RunInputClosed retry must receive this revoked context.
					return ctx.Err()
				}
				go func() {
					if batch {
						sent <- r.SubmitAgentInputBatch(t.Context(), active.SessionRef, agent.AgentInputParent, []agent.AgentInputBatchEntry{{Participant: &source, Message: agent.AgentCommunicationInput{Input: "late report"}}}, func(ctx context.Context, _ session.Session, _ []agent.AgentCommunicationInput) error {
							return parent(ctx)
						})
					} else {
						sent <- r.SubmitParticipantInput(t.Context(), active.SessionRef, source, agent.AgentInput{Target: agent.AgentInputParent, Input: "late report"}, func(ctx context.Context, _ session.Session, _ session.ActorRef, _ agent.AgentInput) error {
							return parent(ctx)
						})
					}
				}()
				<-entered
				if _, err := r.CancelSessionSubagents(t.Context(), active.SessionRef); err != nil {
					t.Fatal(err)
				}
				if err := <-sent; !errors.Is(err, context.Canceled) {
					t.Fatalf("admitted callback = %v", err)
				}
				err = r.SubmitParticipantInput(t.Context(), active.SessionRef, source, agent.AgentInput{Target: agent.AgentInputParent, Input: "old sender"}, func(context.Context, session.Session, session.ActorRef, agent.AgentInput) error {
					t.Fatal("revoked sender dispatched")
					return nil
				})
				if !errorcode.Is(err, errorcode.PermissionDenied) {
					t.Fatalf("old binding admission = %v", err)
				}
				if _, err := r.SubmitChildInput(t.Context(), active.SessionRef, agent.ChildInputCommand{Target: started.Handle, Source: session.ActorRef{Kind: session.ActorKindController, ID: current.Controller.ControllerID}, Input: "old target"}); !errorcode.Is(err, errorcode.NotFound) {
					t.Fatalf("revived old Task target = %v", err)
				}
				fresh := startSessionCancelChild(t, r, current, runner, "fresh")
				if fresh.Ref.TaskID == started.Ref.TaskID {
					t.Fatal("new explicit spawn revived the original Task")
				}
			})
		})
	}
}

func TestCancelSessionSubagentsJoinsSpawnAndReturnsLateIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &sessionCancelRunner{entered: make(chan struct{}), release: make(chan struct{})}
		r, active := newSubagentTaskTestRuntime(t, runner)
		spawned := make(chan error, 1)
		go func() {
			_, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{SpawnID: "late", Agent: "helper", Prompt: "inspect"})
			spawned <- err
		}()
		<-runner.entered
		type result struct {
			endpoints []agent.ChildEndpointRef
			err       error
		}
		cancelled := make(chan result, 1)
		go func() {
			endpoints, err := r.CancelSessionSubagents(t.Context(), active.SessionRef)
			cancelled <- result{endpoints, err}
		}()
		<-runner.producerContext.Done()
		synctest.Wait()
		select {
		case got := <-cancelled:
			t.Fatalf("cancel returned before Spawn supplied its identity: %#v", got)
		default:
		}
		close(runner.release)
		if err := <-spawned; !errors.Is(err, context.Canceled) {
			t.Fatalf("Spawn cancellation = %v", err)
		}
		got := <-cancelled
		if got.err != nil || len(got.endpoints) != 1 || got.endpoints[0].SessionID == "" || runner.cancelCalls != 1 {
			t.Fatalf("late child cancellation = %#v, %v, effects %d", got.endpoints, got.err, runner.cancelCalls)
		}
		current, err := r.sessions.Session(t.Context(), active.SessionRef)
		if err != nil || len(current.Participants) != 0 {
			t.Fatalf("late child attached after cancellation: %#v, %v", current.Participants, err)
		}
		_, err = r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{SpawnID: "late", Agent: "helper", Prompt: "inspect"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled spawn retry = %v", err)
		}
		runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{State: delegation.StateCancelled})
	})
}

func TestCancelSessionSubagentsRecoversRehydratedOrphans(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "configured runner"}[configured], func(t *testing.T) {
			runner := &sessionCancelRunner{}
			original, active := newSubagentTaskTestRuntime(t, runner)
			started := startSessionCancelChild(t, original, active, runner, "orphan")
			legacy, err := original.tasks.store.Get(t.Context(), started.Ref.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			legacy.Spec = map[string]any{"agent": "helper", "agent_id": "agent-" + started.Ref.TaskID, "delegation_id": started.Ref.TaskID, "session_id": started.Ref.SessionID, "turn_seq": int64(1), "retained": "legacy spec"}
			legacy.Metadata = map[string]any{"retained": "legacy metadata"}
			if _, err := original.tasks.store.(taskapi.CASStore).Put(t.Context(), taskapi.PutRequest{Entry: legacy, ExpectedRevision: legacy.Revision}); err != nil {
				t.Fatal(err)
			}
			legacy, err = original.tasks.store.Get(t.Context(), started.Ref.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			config := Config{Sessions: original.sessions, TaskStore: original.tasks.store, AgentFactory: chat.Factory{}}
			if configured {
				config.Subagents = runner
			}
			r, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			// Merely reading the durable running Task must not mint producer liveness.
			if _, err := r.tasks.lookupSubagent(t.Context(), active.SessionRef, started.Ref.TaskID); err != nil {
				t.Fatal(err)
			}
			endpoints, err := r.CancelSessionSubagents(t.Context(), active.SessionRef)
			if err != nil || len(endpoints) != 1 {
				t.Fatalf("orphan cancel = %#v, %v", endpoints, err)
			}
			entry, err := r.tasks.store.Get(t.Context(), started.Ref.TaskID)
			if err != nil || entry.Running || entry.State != taskapi.StateInterrupted || runner.cancelCalls != 0 {
				t.Fatalf("orphan became live or falsely remotely cancelled: %#v, %v, effects %d", entry, err, runner.cancelCalls)
			}
			delete(entry.Spec, subagentCancelPhaseKey)
			delete(entry.Spec, subagentCancelTurnSeqKey)
			for _, key := range []string{subagentCancelPhaseKey, subagentCancelTurnSeqKey, "state", "running", "interrupted_reason"} {
				delete(entry.Metadata, key)
			}
			if !reflect.DeepEqual(entry.Spec, legacy.Spec) || !reflect.DeepEqual(entry.Metadata, legacy.Metadata) {
				t.Fatalf("cancellation rewrote legacy fields: spec %#v metadata %#v", entry.Spec, entry.Metadata)
			}
		})
	}
}

func TestCancelSessionSubagentsReturnsBindingWithoutTask(t *testing.T) {
	r, active := newSubagentTaskTestRuntime(t, nil)
	binding := session.ParticipantBinding{ID: "child-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated, SessionID: "child-session", DelegationID: "missing-task"}
	if _, err := r.sessions.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	endpoints, err := r.CancelSessionSubagents(t.Context(), active.SessionRef)
	if err != nil || len(endpoints) != 1 || endpoints[0].SessionID != binding.SessionID || endpoints[0].EndpointKey != binding.DelegationID {
		t.Fatalf("binding endpoint lost: %#v, %v", endpoints, err)
	}
}

func TestCancelSessionSubagentsFileHistoryAndModelContextRoundTrip(t *testing.T) {
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "cancel-round-trip"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &sessionCancelRunner{spawnState: delegation.StateCompleted}
	r, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, TaskStore: sessionfile.NewTaskStore(sessions), AgentFactory: chat.Factory{}, Subagents: runner}))
	if err != nil {
		t.Fatal(err)
	}
	started := startSessionCancelChild(t, r, active, runner, "history")
	if _, err := r.CancelSessionSubagents(t.Context(), active.SessionRef); err != nil {
		t.Fatal(err)
	}
	want, err := r.tasks.store.Get(t.Context(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	got, err := sessionfile.NewTaskStore(reopened).Get(t.Context(), started.Ref.TaskID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cancelled Task round trip = %#v, %v; want %#v", got, err, want)
	}
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil || len(loaded.Session.Participants) != 0 {
		t.Fatalf("durable binding survived cancellation: %#v, %v", loaded, err)
	}
	var retained bool
	for _, event := range loaded.Events {
		if event != nil && session.EventText(event) == "retained child result" {
			retained = true
		}
	}
	if !retained {
		t.Fatal("canonical child history was removed")
	}
	assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
}

type postSpawnInstallGate struct {
	taskapi.Store
	taskapi.CASStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *postSpawnInstallGate) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	entry, err := s.CASStore.Put(ctx, req)
	if err == nil && spawnPhaseOf(entry) == spawnPhasePostSpawn {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	return entry, err
}

func TestCancelSessionSubagentsJoinsDurableSpawnBeforeProducerInstallation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &sessionCancelRunner{}
		r, active := newSubagentTaskTestRuntime(t, runner)
		gate := &postSpawnInstallGate{Store: r.tasks.store, CASStore: r.tasks.store.(taskapi.CASStore), entered: make(chan struct{}), release: make(chan struct{})}
		r.tasks.store = gate
		spawned := make(chan error, 1)
		go func() {
			_, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{SpawnID: "install-gap", Agent: "helper", Prompt: "inspect"})
			spawned <- err
		}()
		<-gate.entered
		cancelled := make(chan error, 1)
		go func() { _, err := r.CancelSessionSubagents(t.Context(), active.SessionRef); cancelled <- err }()
		<-runner.producerContext.Done()
		synctest.Wait()
		entry, err := gate.Get(t.Context(), runner.spawnContext.TaskID)
		if err != nil || !entry.Running || entry.State != taskapi.StateRunning || taskStringValue(entry.Metadata[subagentCancelPhaseKey]) != "" {
			t.Fatalf("in-flight Spawn was classified as orphan: %#v, %v", entry, err)
		}
		close(gate.release)
		if err := <-spawned; !errors.Is(err, context.Canceled) {
			t.Fatalf("Spawn = %v", err)
		}
		if err := <-cancelled; err != nil {
			t.Fatal(err)
		}
		if runner.cancelCalls != 1 {
			t.Fatalf("real producer Cancel calls = %d", runner.cancelCalls)
		}
		runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{State: delegation.StateCancelled})
	})
}

type sessionCancelInputRunner struct {
	*sessionCancelRunner
	inputEntered chan struct{}
	inputRelease chan struct{}
	inputContext context.Context
	inputRequest agent.ChildInputRequest
}

func (r *sessionCancelInputRunner) SubmitChildInput(ctx context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	r.inputContext, r.inputRequest = ctx, req
	close(r.inputEntered)
	<-r.inputRelease
	return agent.ChildInputResult{StartedActivity: true}, nil
}

func TestCancelSessionSubagentsJoinsDispatchedFollowupBeforeReceipt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &sessionCancelInputRunner{sessionCancelRunner: &sessionCancelRunner{spawnState: delegation.StateCompleted}, inputEntered: make(chan struct{}), inputRelease: make(chan struct{})}
		r, active := newSubagentTaskTestRuntime(t, runner)
		started := startSessionCancelChild(t, r, active, runner, "followup")
		current, err := r.sessions.Session(t.Context(), active.SessionRef)
		if err != nil {
			t.Fatal(err)
		}
		sent := make(chan error, 1)
		go func() {
			_, err := r.SubmitChildInput(t.Context(), active.SessionRef, agent.ChildInputCommand{Target: started.Handle, Source: session.ActorRef{Kind: session.ActorKindController, ID: current.Controller.ControllerID}, Input: "follow up"})
			sent <- err
		}()
		<-runner.inputEntered
		cancelled := make(chan error, 1)
		go func() { _, err := r.CancelSessionSubagents(t.Context(), active.SessionRef); cancelled <- err }()
		<-runner.inputContext.Done()
		synctest.Wait()
		if runner.cancelCalls != 0 {
			t.Fatal("cancellation reconciled an unreturned follow-up admission")
		}
		close(runner.inputRelease)
		if err := <-sent; err != nil {
			t.Fatalf("accepted receipt was replaced with cancellation: %v", err)
		}
		if err := <-cancelled; err != nil {
			t.Fatal(err)
		}
		entry, err := r.tasks.store.Get(t.Context(), started.Ref.TaskID)
		generation, _ := subagentCancelTurnSeq(entry.Metadata)
		if err != nil || runner.cancelCalls != 1 || generation != 2 || !entry.Running {
			t.Fatalf("admitted next generation escaped cancellation: %#v, %v, effects %d", entry, err, runner.cancelCalls)
		}
		runner.inputRequest.Completion.PublishSubagentCompletion(delegation.Result{State: delegation.StateCancelled})
	})
}

func TestCancelSessionSubagentsPreservesUnknownInputOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &sessionCancelRunner{spawnState: delegation.StateCompleted}
		r, active := newSubagentTaskTestRuntime(t, runner)
		startSessionCancelChild(t, r, active, runner, "source")
		current, err := r.sessions.Session(t.Context(), active.SessionRef)
		if err != nil {
			t.Fatal(err)
		}
		entered, sent := make(chan struct{}), make(chan error, 1)
		outcome := errorcode.New(errorcode.UnknownOutcome, "receipt was lost after dispatch")
		go func() {
			sent <- r.SubmitParticipantInput(t.Context(), active.SessionRef, current.Participants[0], agent.AgentInput{Target: agent.AgentInputParent, Input: "report"}, func(ctx context.Context, _ session.Session, _ session.ActorRef, _ agent.AgentInput) error {
				close(entered)
				<-ctx.Done()
				return outcome
			})
		}()
		<-entered
		if _, err := r.CancelSessionSubagents(t.Context(), active.SessionRef); err != nil {
			t.Fatal(err)
		}
		if err := <-sent; !errors.Is(err, outcome) || !errorcode.Is(err, errorcode.UnknownOutcome) {
			t.Fatalf("unknown effect replaced by cancellation: %v", err)
		}
	})
}
