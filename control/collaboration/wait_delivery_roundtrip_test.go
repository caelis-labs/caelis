package collaboration

import (
	"context"
	"encoding/json"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestAutomaticDeliveryWakesWaitThreadAndDrainsRuntimeInputOnce(t *testing.T) {
	for _, timing := range []string{"before wait", "during wait"} {
		t.Run(timing, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				probe := &waitDeliveryModel{started: make(chan struct{}), release: make(chan struct{})}
				root := t.TempDir()
				store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "test", Workspace: session.WorkspaceRef{Key: "work", CWD: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
				backend := &waitDeliveryBackend{}
				service, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
				if err != nil {
					t.Fatal(err)
				}
				defer service.Close()
				parent := Identity{Session: active.SessionID, Member: "parent"}
				tools := Tools(true, func(ctx context.Context, req Request) (json.RawMessage, error) {
					return service.Call(ctx, parent, req)
				})
				rt, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "wait for the child", AgentSpec: agent.AgentSpec{Name: "parent", Model: probe, Tools: tools}})
				if err != nil {
					t.Fatal(err)
				}
				defer run.Handle.Close()
				backend.runner = run.Handle.(agent.BatchSubmissionRunner)
				<-probe.started
				if timing == "during wait" {
					close(probe.release)
					synctest.Wait()
				}
				// The child remains running. Force automatic delivery to consume
				// the mailbox before WaitThread can receive it on its next poll.
				message, err := service.Send(t.Context(), Identity{Session: active.SessionID, Member: "child"}, "parent", "child needs a decision", "")
				if err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				if err := service.deliverRecipient(t.Context(), parent); err != nil {
					t.Fatal(err)
				}
				if timing == "before wait" {
					close(probe.release)
				}
				synctest.Wait()
				if got := probe.CallCount(); got != 2 {
					t.Fatalf("model calls after admission = %d, want 2 without waiting for timeout", got)
				}
				if elapsed := time.Since(started); elapsed != 0 {
					t.Fatalf("admitted input waited %s", elapsed)
				}
				live := probe.LastRequest()
				assertWaitDeliveryContext(t, live, message.Text, "input")

				// The next wait has no new input: a consumed notification must not
				// wake it again. Its one-second timeout advances virtual time.
				if err := run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
				if elapsed := time.Since(started); elapsed != time.Second {
					t.Fatalf("empty follow-up wait elapsed = %s, want 1s", elapsed)
				}
				assertWaitDeliveryContext(t, probe.LastRequest(), message.Text, "timeout")
				if backend.delivered != 1 {
					t.Fatalf("backend deliveries = %d, want 1", backend.delivered)
				}
				if mail, err := service.Receive(t.Context(), parent); err != nil || len(mail) != 0 {
					t.Fatalf("automatically delivered mail remained: %v, %v", mail, err)
				}

				loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				var received int
				for _, event := range loaded.Events {
					if event.Actor.ID == "child" && event.Message != nil && strings.Contains(event.Message.TextContent(), message.Text) {
						received++
					}
				}
				if received != 1 {
					t.Fatalf("durable child inputs = %d, want 1", received)
				}
				reloaded := &waitDeliveryModel{}
				resumed, err := chat.NewWithTools("parent", reloaded, tools, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, err := range resumed.Run(agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: loaded.Session, Events: loaded.Events})) {
					if err != nil {
						t.Fatal(err)
					}
				}
				assertWaitDeliveryContext(t, reloaded.LastRequest(), message.Text, "input")
			})
		})
	}
}

func assertWaitDeliveryContext(t *testing.T, request *model.Request, text, reason string) {
	t.Helper()
	raw, err := json.Marshal(request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), text); got != 1 {
		t.Fatalf("input occurrences = %d, want 1: %s", got, raw)
	}
	if !strings.Contains(string(raw), `\"reason\":\"`+reason+`\"`) {
		t.Fatalf("missing WaitThread reason %q: %s", reason, raw)
	}
}

type waitDeliveryBackend struct {
	runner    agent.BatchSubmissionRunner
	delivered int
}

func (*waitDeliveryBackend) List(context.Context, string) ([]Thread, error) {
	return []Thread{{Handle: "parent", State: "running", CanDeliver: true}, {Handle: "child", State: "running"}}, nil
}

func (b *waitDeliveryBackend) Deliver(ctx context.Context, _ string, messages []Message) error {
	inputs := make([]agent.AgentCommunicationInput, 0, len(messages))
	for _, message := range messages {
		inputs = append(inputs, agent.AgentCommunicationInput{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: message.From, Name: message.From}, Input: message.Text})
	}
	if err := b.runner.SubmitBatch(ctx, inputs); err != nil {
		return err
	}
	b.delivered += len(messages)
	return nil
}

type waitDeliveryModel struct {
	mu       sync.Mutex
	requests []*model.Request
	started  chan struct{}
	release  chan struct{}
}

func (*waitDeliveryModel) Name() string { return "wait-delivery" }

func (*waitDeliveryModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true}
}

func (m *waitDeliveryModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	m.requests = append(m.requests, model.CloneRequest(request))
	call := len(m.requests)
	m.mu.Unlock()
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "handled"), TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop}
		if m.started != nil && call <= 2 {
			id, args := "wait-for-child", `{"timeout_seconds":60}`
			if call == 1 {
				close(m.started)
				<-m.release
			} else {
				id, args = "wait-after-drain", `{"timeout_seconds":1}`
			}
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: id, Name: "WaitThread", Args: args}}, "")
			response.FinishReason = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}

func (m *waitDeliveryModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *waitDeliveryModel) LastRequest() *model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return model.CloneRequest(m.requests[len(m.requests)-1])
}
