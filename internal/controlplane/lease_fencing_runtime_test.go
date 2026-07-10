package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	localruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestLeaseTakeoverFencesNonCooperativeToolResultAndReplaysUnknownOutcome(t *testing.T) {
	t.Parallel()

	clock := &fencingClock{now: time.Unix(1_000, 0)}
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{Clock: clock.Now}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "fencing-user", PreferredSessionID: "fencing-runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := allowPolicyRegistry{mode: policy.NamedMode{ID: "allow", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	started := make(chan struct{})
	finish := make(chan struct{})
	target := tool.NamedTool{
		Def: tool.Definition{Name: "SIDE_EFFECT", EffectClass: tool.EffectNonIdempotent, InputSchema: map[string]any{"type": "object"}},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			close(started)
			<-finish // Deliberately ignore cancellation and lease loss.
			return tool.Result{ID: "call-fenced", Name: "SIDE_EFFECT", Content: []model.Part{model.NewTextPart("stale success")}}, nil
		},
	}
	runtimeA, err := localruntime.New(localruntime.Config{Sessions: service, AgentFactory: chat.Factory{}, Clock: clock.Now, PolicyRegistry: registry, DefaultPolicyMode: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	leasedA, err := NewLeasedRuntime(LeasedRuntimeConfig{Runtime: runtimeA, Leases: service, OwnerID: "host-a", TTL: 10 * time.Second, HeartbeatInterval: 9 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runA, err := leasedA.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef, Input: "perform the side effect",
		AgentSpec: agent.AgentSpec{Name: "chat", Model: &fencingToolModel{}, Tools: []tool.Tool{target}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("non-cooperative tool did not start")
	}

	clock.Advance(11 * time.Second)
	recoveryModel := &fencingCaptureModel{reply: "recovered"}
	runtimeB, err := localruntime.New(localruntime.Config{Sessions: service, AgentFactory: chat.Factory{}, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	leasedB, err := NewLeasedRuntime(LeasedRuntimeConfig{Runtime: runtimeB, Leases: service, OwnerID: "host-b", TTL: 10 * time.Second, HeartbeatInterval: 9 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := leasedB.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef, Input: "recover without retrying",
		AgentSpec: agent.AgentSpec{Name: "chat", Model: recoveryModel},
	})
	if err != nil {
		t.Fatalf("takeover Run() error = %v", err)
	}
	if err := drainControlplaneRunner(runB.Handle); err != nil {
		t.Fatalf("takeover runner error = %v", err)
	}
	if !messagesContain(recoveryModel.messages, "unknown_outcome") {
		t.Fatalf("takeover model context = %#v, want durable unknown_outcome", recoveryModel.messages)
	}

	close(finish)
	if err := drainControlplaneRunner(runA.Handle); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("stale runner error = %v, want ErrLeaseConflict", err)
	}
	events, err := service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Status == session.ToolExecutionSucceeded && event.Journal.ToolExecution.Key.RunID == runA.Handle.RunID() {
			t.Fatalf("stale success became durable: %#v", event)
		}
	}

	replayModel := &fencingCaptureModel{reply: "verified"}
	runtimeC, err := localruntime.New(localruntime.Config{Sessions: service, AgentFactory: chat.Factory{}, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	leasedC, err := NewLeasedRuntime(LeasedRuntimeConfig{Runtime: runtimeC, Leases: service, OwnerID: "host-c", TTL: 10 * time.Second, HeartbeatInterval: 9 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runC, err := leasedC.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef, Input: "verify replay", AgentSpec: agent.AgentSpec{Name: "chat", Model: replayModel}})
	if err != nil {
		t.Fatal(err)
	}
	if err := drainControlplaneRunner(runC.Handle); err != nil {
		t.Fatal(err)
	}
	wantReplay := append(model.CloneMessages(recoveryModel.messages), model.NewTextMessage(model.RoleAssistant, "recovered"), model.NewTextMessage(model.RoleUser, "verify replay"))
	if !reflect.DeepEqual(replayModel.messages, wantReplay) {
		t.Fatalf("rebuilt model context = %#v, want live-produced %#v", replayModel.messages, wantReplay)
	}
}

type fencingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fencingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fencingClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type allowPolicyRegistry struct{ mode policy.Mode }

func (r allowPolicyRegistry) Lookup(context.Context, string) (policy.Mode, bool, error) {
	return r.mode, true, nil
}

type fencingToolModel struct{ calls int }

func (m *fencingToolModel) Name() string { return "fencing-tool" }

func (m *fencingToolModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true}
}

func (m *fencingToolModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	call := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if call == 1 {
			yield(model.StreamEventFromResponse(&model.Response{
				Message:      model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call-fenced", Name: "SIDE_EFFECT", Args: `{}`}}, ""),
				TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonToolCalls,
			}), nil)
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "unexpected"), TurnComplete: true}), nil)
	}
}

type fencingCaptureModel struct {
	reply    string
	messages []model.Message
}

func (m *fencingCaptureModel) Name() string { return "fencing-capture" }

func (m *fencingCaptureModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.messages = model.CloneMessages(req.Messages)
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, m.reply), TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted}), nil)
	}
}

func drainControlplaneRunner(runner agent.Runner) error {
	var out error
	for _, err := range runner.Events() {
		out = errors.Join(out, err)
	}
	return out
}

func messagesContain(messages []model.Message, needle string) bool {
	raw, _ := json.Marshal(messages)
	return strings.Contains(string(raw), needle)
}
