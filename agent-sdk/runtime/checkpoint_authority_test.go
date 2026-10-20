package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type checkpointTestCatalog struct {
	ready    []tool.Tool
	validate checkpointValidator
	checks   atomic.Int32
}

func (s *checkpointTestCatalog) Tools() []tool.Tool    { return s.ready }
func (*checkpointTestCatalog) CatalogRevision() string { return "checkpoint-catalog" }
func (s *checkpointTestCatalog) ValidateCatalog(ctx context.Context, _ string) (func(), error) {
	s.checks.Add(1)
	return s.validate(ctx)
}

type checkpointRecoveryModel struct {
	overflow  bool
	attempted bool
	work      int
	summaries int
}

func (*checkpointRecoveryModel) Name() string             { return "checkpoint-recovery" }
func (*checkpointRecoveryModel) ProviderName() string     { return "fixture" }
func (*checkpointRecoveryModel) ContextWindowTokens() int { return 10000 }
func (*checkpointRecoveryModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *checkpointRecoveryModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "finished")
		switch {
		case isRuntimeCompactionRequest(req):
			m.summaries++
			message = model.NewTextMessage(model.RoleAssistant, asyncFixtureSummary)
		case m.work == 0:
			m.work++
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "work", Name: "work", Args: `{}`}}, "")
		case m.overflow && !m.attempted:
			m.attempted = true
			yield(nil, &model.ContextOverflowError{Cause: errors.New("fixture overflow")})
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, StepComplete: true}), nil)
	}
}

// The dynamic SDK resolver's parent Validate is optional for synchronous
// recovery. Runtime catalog/grant guards must still protect the actual append.
// Seed retirement through real ToolSearch/tool_retire calls, then reactivate.
func TestExperimentalDynamicRecoveryOwnsCheckpointGuards(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, retirement := range []bool{false, true} {
			for _, parent := range []bool{false, true} {
				for _, overflow := range []bool{false, true} {
					for _, failure := range []string{"none", "catalog", "grant"} {
						t.Run(fmt.Sprintf("async_%t/retirement_%t/parent_%t/overflow_%t/%s", async, retirement, parent, overflow, failure), func(t *testing.T) {
							testDynamicRecoveryGuards(t, async, retirement, parent, overflow, failure)
						})
					}
				}
			}
		}
	}
}

func testDynamicRecoveryGuards(t *testing.T, async, retirement, parent, overflow bool, failure string) {
	t.Helper()
	store, active := newTestSessionService(t, "dynamic-checkpoint-guards")
	source := &checkpointTestCatalog{ready: []tool.Tool{tool.NamedTool{Def: fixtureMCPDefinition("checkpoint-source")}}}
	binding := tool.BindingForDefinition(source.ready[0].Definition())
	search := toolsearch.NewSource(source, toolsearch.NewLexicalRanker())
	seed, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, ExperimentalToolRetirement: true, PolicyRegistry: retirementPolicy(), DefaultPolicyMode: "retirement-test"})
	if err != nil {
		t.Fatal(err)
	}
	script := &retirementScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "discover", Name: tool.ToolSearchToolName, Args: `{"query":"read"}`}}
		case 1:
			return []model.ToolCall{{ID: "mark", Name: tool.RetireToolName, Args: retirementMarkArgs(binding)}}
		}
		return nil
	}}
	run, err := seed.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "discover and mark", AgentSpec: agent.AgentSpec{Model: script, Tools: []tool.Tool{search}, DeferredTools: source}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	before, ok := exposureFromEvents(loaded.Events)
	if !ok || len(before.Active) != 1 || len(before.Pending) != 1 {
		t.Fatalf("seed exposure=%+v", before)
	}
	grants, err := NewMCPGrantStore(filepath.Join(t.TempDir(), "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := mcpGrantFor(source.ready[0].Definition(), active)
	if err != nil {
		t.Fatal(err)
	}
	var held, released int
	source.validate = func(context.Context) (func(), error) {
		if failure == "catalog" {
			return nil, tool.ErrCatalogSnapshotStale
		}
		if failure == "grant" {
			// Change the grant epoch after capture, before the grant guard.
			if err := grants.Revoke(grant, "session", active.SessionID); err != nil {
				return nil, err
			}
		}
		held++
		return func() { released++ }, nil
	}
	llm := &checkpointRecoveryModel{overflow: overflow}
	cfg := CompactionConfig{Enabled: true, WatermarkRatio: .15, ForceWatermarkRatio: .2, ReserveOutputTokens: 10, SafetyMarginTokens: 10, Async: AsyncCompactionConfig{Enabled: async}}
	if overflow {
		cfg.WatermarkRatio, cfg.ForceWatermarkRatio = .9, .95
	}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, MCPGrants: grants, ExperimentalToolRetirement: retirement, Compaction: cfg})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	work := tool.NamedTool{Def: tool.Definition{Name: "work", EffectClass: tool.EffectReadOnly, InputSchema: map[string]any{"type": "object"}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
		effects++
		return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart(strings.Repeat("new independent evidence ", 500))}}, nil
	}}
	var parentHeld, parentReleased, resolved, snapshotReleased int
	run, err = rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "continue with local work", AgentSpec: agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		resolved++
		snapshot := agent.ModelRequestSnapshot{Revision: "profile-1", Model: llm, Tools: []tool.Tool{search, work}, DeferredTools: source, Release: func() { snapshotReleased++ }}
		if parent {
			snapshot.Validate = func(context.Context) (func(), error) {
				parentHeld++
				return func() { parentReleased++ }, nil
			}
		}
		return snapshot, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := drainRunnerEvents(t, run.Handle)
	if effects != 1 || llm.summaries == 0 || llm.attempted != overflow {
		t.Fatalf("recovery not exercised exactly once: effects=%d summaries=%d overflow=%v", effects, llm.summaries, llm.attempted)
	}
	if held != released || parentHeld != parentReleased || resolved != snapshotReleased {
		t.Fatalf("unbalanced releases: catalog=%d/%d parent=%d/%d snapshot=%d/%d", held, released, parentHeld, parentReleased, resolved, snapshotReleased)
	}
	guarded := async || retirement
	if guarded && source.checks.Load() == 0 {
		t.Fatal("Runtime catalog guard was not consulted")
	}
	if !guarded && source.checks.Load() != 0 {
		t.Fatal("default-off recovery acquired experimental guards")
	}
	rejected := guarded && failure != "none"
	if rejected && !errors.Is(runErr, agent.ErrModelRequestSnapshotStale) {
		t.Fatalf("stale authority did not stop Turn: %v", runErr)
	}
	if !rejected && runErr != nil {
		t.Fatal(runErr)
	}
	loaded, err = store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	_, _, committed := compact.LatestCompactEvent(loaded.Events)
	if committed == rejected {
		t.Fatalf("checkpoint committed=%v rejected=%v", committed, rejected)
	}
	after, _ := exposureFromEvents(loaded.Events)
	if rejected && !reflect.DeepEqual(after, before) {
		t.Fatalf("failed checkpoint changed exposure: before=%+v after=%+v", before, after)
	}
	if committed && retirement && (len(after.Active) != 0 || len(after.Pending) != 0) {
		t.Fatalf("durable checkpoint did not retire marked binding: %+v", after)
	}
}

func TestAsyncCompactionDynamicMissingParentValidateDisablesSpeculation(t *testing.T) {
	h := newAsyncHarness(t, "")
	run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Revision: "optional-validator", Model: h.llm, Tools: []tool.Tool{h.tool}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Handle.Close(); _ = run.Handle.WaitCompletion(context.Background()) })
	receiveSnapshotValue(t, h.llm.foreground)
	h.llm.responses <- asyncToolResponse("first")
	next := receiveSnapshotValue(t, h.llm.foreground)
	if next.scope == nil {
		t.Fatal("async scope absent")
	}
	next.scope.mu.Lock()
	started := next.scope.started
	next.scope.mu.Unlock()
	if started {
		t.Fatal("Runtime-only guards enabled speculation without embedding validation")
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.llm.background:
		t.Fatal("background model dispatched without parent Validate")
	default:
	}
}

func TestAsyncCompactionDynamicRemovedParentValidatePreventsAdoption(t *testing.T) {
	h := newAsyncHarness(t, "")
	var removed atomic.Bool
	run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{ResolveModelRequest: func(ctx context.Context) (agent.ModelRequestSnapshot, error) {
		snapshot, err := h.source.resolve(ctx)
		if removed.Load() {
			// Same revision/model/catalog; only embedding validation disappears.
			snapshot.Validate = nil
		}
		return snapshot, err
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Handle.Close(); _ = run.Handle.WaitCompletion(context.Background()) })
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	removed.Store(true)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground)
	if strings.Contains(asyncMessagesText(after.request.Messages), "CONTEXT CHECKPOINT") {
		t.Fatal("candidate adopted after embedding validation disappeared")
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("candidate persisted without current embedding validation")
	}
}

// A custom Engine may support overflow without ForceEngine, so its provider is
// not wrapped by the request watermark gate and cannot supply captured authority.
type overflowOnlyCompactor struct{ compact.Engine }

func TestExperimentalDynamicRecoveryRejectsMissingRequestAuthority(t *testing.T) {
	for _, flags := range []struct{ async, retirement bool }{{true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("async_%t/retirement_%t", flags.async, flags.retirement), func(t *testing.T) {
			h := newAsyncHarness(t, "")
			h.rt.compaction.Async.Enabled, h.rt.toolRetirement = flags.async, flags.retirement
			compactor := &snapshotOverflowCompactor{err: errors.New("unguarded overflow reached generator")}
			h.rt.compactor = &overflowOnlyCompactor{Engine: compactor}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- snapshotResponse{err: &model.ContextOverflowError{Cause: errors.New("fixture overflow")}}
			err := run.Handle.WaitCompletion(t.Context())
			if err == nil || !strings.Contains(err.Error(), "dynamic compaction recovery requires request authority") {
				t.Fatalf("missing request authority was not rejected: %v", err)
			}
			if compactor.request.Model != nil || compactor.prepareCalls != 0 {
				t.Fatal("generation started without captured authority")
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("unguarded dynamic recovery committed a checkpoint")
			}
		})
	}
}
