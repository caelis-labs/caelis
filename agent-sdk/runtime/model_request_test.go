package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type requestSnapshotSource struct {
	mu         sync.Mutex
	current    agent.ModelRequestSnapshot
	admissions []agent.ModelRequestAdmission
	resolved   atomic.Int64
}

func (s *requestSnapshotSource) set(snapshot agent.ModelRequestSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = agent.CloneModelRequestSnapshot(snapshot)
}

func (s *requestSnapshotSource) resolve(context.Context) (agent.ModelRequestSnapshot, error) {
	s.mu.Lock()
	snapshot := agent.CloneModelRequestSnapshot(s.current)
	s.mu.Unlock()
	s.resolved.Add(1)
	revision := snapshot.Revision
	snapshot.Admit = func(_ context.Context, admission agent.ModelRequestAdmission) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.current.Revision != revision {
			return agent.ErrModelRequestSnapshotStale
		}
		s.admissions = append(s.admissions, admission)
		return nil
	}
	return snapshot, nil
}

type snapshotResponse struct {
	response *model.Response
	err      error
}

type requestSnapshotModel struct {
	name      string
	requests  chan *model.Request
	responses chan snapshotResponse
	calls     atomic.Int64
}

func newRequestSnapshotModel(name string) *requestSnapshotModel {
	return &requestSnapshotModel{name: name, requests: make(chan *model.Request, 4), responses: make(chan snapshotResponse, 4)}
}

func (m *requestSnapshotModel) Name() string { return m.name }
func (*requestSnapshotModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *requestSnapshotModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls.Add(1)
		select {
		case m.requests <- model.CloneRequest(req):
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		select {
		case result := <-m.responses:
			if result.err != nil {
				yield(nil, result.err)
			} else {
				yield(model.StreamEventFromResponse(result.response), nil)
			}
		case <-ctx.Done():
			yield(nil, ctx.Err())
		}
	}
}

func receiveSnapshotValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatal("request boundary did not arrive:", ctx.Err())
		var zero T
		return zero
	}
}

func snapshotTestTool(revision string, calls chan<- tool.Call) tool.Tool {
	return tool.NamedTool{Def: tool.Definition{
		Name: "shared", Description: revision, EffectClass: tool.EffectNonIdempotent,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{revision: map[string]any{"type": "string"}}},
	}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		calls <- call
		return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewTextPart(revision)}}, nil
	}}
}

func snapshotTestConfig(revision string, llm model.LLM, tools ...tool.Tool) agent.ModelRequestSnapshot {
	return agent.ModelRequestSnapshot{
		Revision: revision, Model: llm, Tools: tools,
		Instructions: []model.Part{model.NewTextPart("instructions " + revision)},
		Reasoning:    model.ReasoningConfig{Effort: revision},
	}
}

func TestRuntimeModelRequestSnapshotsPinInflightToolsAndRefreshSameTurn(t *testing.T) {
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	oldModel, newModel := newRequestSnapshotModel("old-model"), newRequestSnapshotModel("new-model")
	oldCalls, newCalls := make(chan tool.Call, 1), make(chan tool.Call, 1)
	oldConfig := snapshotTestConfig("old", oldModel, snapshotTestTool("old", oldCalls))
	oldConfig.Request.ServiceTier = model.ServiceTierPriority
	newConfig := snapshotTestConfig("new", newModel, snapshotTestTool("new", newCalls))
	source := &requestSnapshotSource{}
	source.set(oldConfig)
	var policyCalls atomic.Int64
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "test", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		policyCalls.Add(1)
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	interceptor := &recordingLifecycleInterceptor{}
	rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "test", LifecycleInterceptors: []agent.LifecycleInterceptor{interceptor}})
	if err != nil {
		t.Fatal(err)
	}
	stream := true
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", TurnID: "canonical-turn", AgentSpec: agent.AgentSpec{
		ResolveModelRequest: source.resolve, Request: agent.ModelRequestOptions{Stream: &stream, ServiceTier: model.ServiceTierPriority},
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := receiveSnapshotValue(t, oldModel.requests)
	source.set(newConfig) // Commits while the old provider is blocked in flight.
	oldModel.responses <- snapshotResponse{response: toolCallResponse("reused-call", "shared")}
	oldCall := receiveSnapshotValue(t, oldCalls)
	second := receiveSnapshotValue(t, newModel.requests)
	newModel.responses <- snapshotResponse{response: toolCallResponse("reused-call", "shared")}
	newCall := receiveSnapshotValue(t, newCalls)
	third := receiveSnapshotValue(t, newModel.requests)
	newModel.responses <- snapshotResponse{response: textResponse("done", model.Usage{})}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	for index, request := range []*model.Request{first, second, third} {
		revision, tier := "new", model.ServiceTier("")
		if index == 0 {
			revision, tier = "old", model.ServiceTierPriority
		}
		if len(request.Instructions) != 1 || request.Instructions[0].Text.Text != "instructions "+revision || request.Reasoning.Effort != revision || request.ServiceTier != tier || !request.Stream || len(request.Tools) != 1 || request.Tools[0].Function.Description != revision {
			t.Fatalf("mixed request snapshot %d: %#v", index, request)
		}
	}
	if oldCall.RuntimeModel.Name() != "old-model" || newCall.RuntimeModel.Name() != "new-model" || oldCall.Execution.TurnID != "canonical-turn" || newCall.Execution.TurnID != "canonical-turn" || oldCall.Execution.ItemID == newCall.Execution.ItemID {
		t.Fatalf("tool model/identity not pinned: old=%#v new=%#v", oldCall, newCall)
	}
	if policyCalls.Load() != 2 || !interceptor.saw(agent.LifecycleTool) || !interceptor.saw(agent.LifecycleModel) {
		t.Fatal("dynamic tools or models bypassed Runtime wrappers")
	}
	if !reflect.DeepEqual(first.Messages, second.Messages[:len(first.Messages)]) || !reflect.DeepEqual(second.Messages, third.Messages[:len(second.Messages)]) {
		t.Fatal("request snapshot change rewrote the sent history prefix")
	}
	secondPrefix, thirdPrefix := model.CloneRequest(second), model.CloneRequest(third)
	secondPrefix.Messages, thirdPrefix.Messages = nil, nil
	if !reflect.DeepEqual(secondPrefix, thirdPrefix) {
		t.Fatal("unchanged configuration changed the model prefix")
	}
	source.mu.Lock()
	admissions := append([]agent.ModelRequestAdmission(nil), source.admissions...)
	source.mu.Unlock()
	if len(admissions) != 3 || admissions[0].RequestID == admissions[1].RequestID || admissions[1].RequestID == admissions[2].RequestID {
		t.Fatalf("attempt identities=%#v", admissions)
	}
	for _, admission := range admissions {
		if admission.RequestID == "" || admission.TurnID != "canonical-turn" {
			t.Fatalf("unbound admission=%#v", admission)
		}
	}
	loaded, err := sessions.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	users, succeeded := 0, map[string]bool{}
	for _, event := range loaded.Events {
		if event.Type == session.EventTypeUser {
			users++
		}
		if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Status == session.ToolExecutionSucceeded {
			succeeded[event.Journal.ToolExecution.Key.StepID] = true
		}
	}
	if users != 1 || len(succeeded) != 2 {
		t.Fatalf("canonical users=%d tool executions=%v", users, succeeded)
	}
	// Reopen the physical store and rebuild provider context through the real
	// Runtime, proving the live prefix survives whole-object persistence.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	resumed, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := resumed.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "resume", AgentSpec: agent.AgentSpec{ResolveModelRequest: source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := receiveSnapshotValue(t, newModel.requests)
	newModel.responses <- snapshotResponse{response: textResponse("resumed", model.Usage{})}
	if err := replay.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Messages) != len(third.Messages)+2 || !reflect.DeepEqual(third.Messages, rebuilt.Messages[:len(third.Messages)]) {
		a, _ := json.Marshal(third.Messages)
		b, _ := json.Marshal(rebuilt.Messages)
		t.Fatalf("replayed prefix changed:\nlive %s\nreplay %s", a, b)
	}
}

type snapshotLifecycleBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *snapshotLifecycleBarrier) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	if event.Operation == agent.LifecycleModel {
		b.once.Do(func() {
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return next(ctx)
}

func TestRuntimeModelRequestSnapshotRefreshesBeforeFinalAdmission(t *testing.T) {
	sessions, active := newJournalTestSession(t, "snapshot-build-race")
	oldModel, newModel := newRequestSnapshotModel("old"), newRequestSnapshotModel("new")
	source := &requestSnapshotSource{}
	source.set(snapshotTestConfig("old", oldModel))
	barrier := &snapshotLifecycleBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, LifecycleInterceptors: []agent.LifecycleInterceptor{barrier}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{ResolveModelRequest: source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	receiveSnapshotValue(t, barrier.entered)
	// Request construction has finished; only final attempt admission remains.
	// Clearing every optional field must not recover defaults from the old spec.
	source.set(agent.ModelRequestSnapshot{Revision: "new", Model: newModel})
	close(barrier.release)
	request := receiveSnapshotValue(t, newModel.requests)
	newModel.responses <- snapshotResponse{response: textResponse("done", model.Usage{})}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if oldModel.calls.Load() != 0 || newModel.calls.Load() != 1 || source.resolved.Load() != 2 || len(request.Instructions) != 0 || len(request.Tools) != 0 || request.Reasoning != (model.ReasoningConfig{}) || request.ServiceTier != "" {
		t.Fatalf("stale request sent or clear lost: old=%d new=%d resolves=%d request=%#v", oldModel.calls.Load(), newModel.calls.Load(), source.resolved.Load(), request)
	}
}

func TestRuntimeModelRequestSnapshotRefreshesBeforeProviderRetry(t *testing.T) {
	sessions, active := newJournalTestSession(t, "snapshot-retry-race")
	oldModel, newModel := newRequestSnapshotModel("old"), newRequestSnapshotModel("new")
	source := &requestSnapshotSource{}
	source.set(snapshotTestConfig("old", model.WithRetry(oldModel, model.RetryConfig{MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})))
	rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{ResolveModelRequest: source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	receiveSnapshotValue(t, oldModel.requests)
	source.set(snapshotTestConfig("new", newModel))
	oldModel.responses <- snapshotResponse{err: errors.New("retryable provider failure")}
	receiveSnapshotValue(t, newModel.requests)
	newModel.responses <- snapshotResponse{response: textResponse("done", model.Usage{})}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if oldModel.calls.Load() != 1 || newModel.calls.Load() != 1 {
		t.Fatalf("old provider retried after update: old=%d new=%d", oldModel.calls.Load(), newModel.calls.Load())
	}
	events, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	receipts := 0
	for _, event := range events {
		if session.IsModelInvocationReceipt(event) {
			receipts++
		}
		if strings.Contains(session.EventText(event), "snapshot is stale") {
			t.Fatal("internal refresh escaped into canonical history")
		}
	}
	if receipts != 2 {
		t.Fatalf("receipts=%d want only two dispatched attempts", receipts)
	}
}

type snapshotOverflowCompactor struct {
	prepareCalls int
	request      compact.Request
	err          error
}

func (c *snapshotOverflowCompactor) Prepare(context.Context, compact.Request) (compact.Result, error) {
	c.prepareCalls++
	return compact.Result{}, errors.New("stale bootstrap invoked compaction")
}

func (c *snapshotOverflowCompactor) CompactOnOverflow(_ context.Context, request compact.Request, cause error) (compact.Result, error) {
	if !model.IsContextOverflow(cause) {
		return compact.Result{}, errors.New("overflow cause lost")
	}
	c.request = request
	return compact.Result{}, c.err
}

func (*snapshotOverflowCompactor) Force(context.Context, compact.Request, string) (compact.Result, error) {
	return compact.Result{}, errors.New("configuration update forced compaction")
}

func TestRuntimeModelRequestSnapshotOverflowUsesActualModelAndTier(t *testing.T) {
	sessions, active := newJournalTestSession(t, "snapshot-overflow")
	oldModel, newModel := newRequestSnapshotModel("bootstrap"), newRequestSnapshotModel("selected")
	source := &requestSnapshotSource{}
	snapshot := snapshotTestConfig("selected", newModel)
	snapshot.Request.ServiceTier = model.ServiceTierPriority
	source.set(snapshot)
	stop := errors.New("captured overflow request")
	compactor := &snapshotOverflowCompactor{err: stop}
	rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true}, Compactor: compactor})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{Model: oldModel, ResolveModelRequest: source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	receiveSnapshotValue(t, newModel.requests)
	newModel.responses <- snapshotResponse{err: &model.ContextOverflowError{Cause: errors.New("provider context full")}}
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, stop) {
		t.Fatalf("overflow recovery error=%v", err)
	}
	if compactor.prepareCalls != 0 || oldModel.calls.Load() != 0 || compactor.request.Model == nil || compactor.request.Model.Name() != newModel.Name() || compactor.request.ServiceTier != model.ServiceTierPriority {
		t.Fatalf("compaction used bootstrap: prepare=%d old=%d request=%#v", compactor.prepareCalls, oldModel.calls.Load(), compactor.request)
	}
}
