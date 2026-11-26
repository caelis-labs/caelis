package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"iter"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type interleavedCompactionModel struct {
	invocationFaultModel
	mutate func()
}

func TestCompactionReceiptAndCheckpointKeepOriginalFence(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent("objective")}); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	llm := &interleavedCompactionModel{mutate: func() {
		if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
			t.Fatal(err)
		}
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := rt.Compact(ctx, CompactRequest{SessionRef: active.SessionRef, Model: llm})
	if !errors.Is(err, session.ErrFenceConflict) || result.Compacted || llm.calls != 1 {
		t.Fatalf("compact=%+v calls=%d err=%v", result, llm.calls, err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range loaded.Events {
		if session.IsModelInvocationReceipt(event) || compact.IsCompactEvent(event) {
			t.Fatal("stale fence committed accounting or checkpoint")
		}
	}
}

func (m *interleavedCompactionModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.mutate()
		m.invocationFaultModel.Generate(ctx, req)(yield)
	}
}

func TestCompactionAccountingSurvivesConcurrentJournalWithoutAdmittingStaleContext(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "journal", true: "model_context"}[visible], func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent("original objective")}); err != nil {
				t.Fatal(err)
			}
			other := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			llm := &interleavedCompactionModel{mutate: func() {
				event := session.NewModelInvocationReceipt(model.Invocation{ID: "concurrent-review", Outcome: "completed"}, "guardian")
				if visible {
					event = userTextEvent("new unsummarized instruction")
				}
				if _, err := other.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: event}); err != nil {
					t.Fatal(err)
				}
			}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: llm})
			if visible {
				if !errors.Is(err, session.ErrRevisionConflict) || result.Compacted {
					t.Fatalf("stale compaction = %+v, %v", result, err)
				}
			} else if err != nil || !result.Compacted {
				t.Fatalf("journal-only compaction = %+v, %v", result, err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, event := range loaded.Events {
				if session.IsModelInvocationReceipt(event) {
					receipts++
				}
				if visible && compact.IsCompactEvent(event) {
					t.Fatal("stale checkpoint committed")
				}
			}
			wantReceipts := 2
			if visible {
				wantReceipts = 1
			}
			if receipts != wantReceipts || llm.calls != 1 {
				t.Fatalf("receipts=%d calls=%d", receipts, llm.calls)
			}
			probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
			rt, err = New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "probe", AgentSpec: agent.AgentSpec{Name: "main", Model: probe}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			want := []model.Message{model.NewTextMessage(model.RoleUser, "original objective"), model.NewTextMessage(model.RoleUser, "new unsummarized instruction"), model.NewTextMessage(model.RoleUser, "probe")}
			if !visible {
				want = []model.Message{*result.Event.Message, model.NewTextMessage(model.RoleUser, "probe")}
			}
			if got := <-probe.messages; !reflect.DeepEqual(got, want) {
				t.Fatalf("rebuilt context = %#v, want %#v", got, want)
			}
		})
	}
}

type coverageModel struct {
	calls    int
	response *model.Response
	err      error
}

func (*coverageModel) Name() string         { return "coverage" }
func (*coverageModel) ProviderName() string { return "fixture" }
func (m *coverageModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if m.calls == 1 && m.err != nil {
			yield(nil, m.err)
			return
		}
		response := m.response
		if response == nil || m.calls > 1 {
			response = &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "CONTEXT CHECKPOINT\n\nPreserve the user's objective and authorization constraints. Keep unknown outcomes explicit and continue the remaining implementation and validation."), TurnComplete: true}
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

func coverageFingerprint(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func coverageMessage(role model.Role, text string) *model.Message {
	message := model.NewTextMessage(role, text)
	return &message
}

func coverageRequest(t *testing.T, llm *coverageModel) compact.Request {
	t.Helper()
	req := compact.Request{
		Model:  llm,
		Events: []*session.Event{{ID: "user", Seq: 1, Type: session.EventTypeUser, Message: coverageMessage(model.RoleUser, "preserve the objective")}, {ID: "assistant", Seq: 2, Type: session.EventTypeAssistant, Message: coverageMessage(model.RoleAssistant, "continue validation")}},
		InContextRequest: &model.Request{Instructions: []model.Part{model.NewTextPart("preserve all constraints")}, Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "preserve the objective"),
			model.NewTextMessage(model.RoleAssistant, "continue validation"),
		}},
		RequireInContext: true,
	}
	bindCoverage(t, &req)
	return req
}

func bindCoverage(t *testing.T, req *compact.Request) {
	t.Helper()
	delta := compact.EventsAfterLatestCompact(req.Events)
	req.Coverage = &compact.Coverage{
		SourceThroughSeq: session.LastEventSeq(req.Events), SummarizedThroughSeq: session.LastEventSeq(delta),
		SummarizedThroughID: lastEventID(delta), SourceEventCount: len(delta),
		SourcePrefixHash: coverageFingerprint(t, req.Events), FrozenRequestHash: coverageFingerprint(t, req.InContextRequest),
	}
}

func TestCompactionCoverageRejectsInvalidBeforeGeneration(t *testing.T) {
	for _, path := range []string{"force", "compact"} {
		for _, tt := range []struct {
			name   string
			mutate func(*compact.Request)
		}{
			{"missing_request", func(r *compact.Request) { r.InContextRequest = nil }},
			{"missing_coverage", func(r *compact.Request) { r.Coverage = nil }},
			{"empty_missing_coverage", func(r *compact.Request) { r.Events = nil; r.Coverage = nil }},
			{"empty_missing_request", func(r *compact.Request) { r.Events = nil; bindCoverage(t, r); r.InContextRequest = nil }},
			{"source_hash", func(r *compact.Request) { r.Events[0].Message = coverageMessage(model.RoleUser, "mutated source") }},
			{"frozen_hash", func(r *compact.Request) {
				r.InContextRequest.Instructions = []model.Part{model.NewTextPart("mutated instructions")}
			}},
			{"source_seq", func(r *compact.Request) { r.Coverage.SourceThroughSeq++ }},
			{"count", func(r *compact.Request) { r.Coverage.SourceEventCount++ }},
			{"id", func(r *compact.Request) { r.Coverage.SummarizedThroughID = "wrong" }},
			{"seq", func(r *compact.Request) { r.Coverage.SummarizedThroughSeq-- }},
			{"summarized_after_source", func(r *compact.Request) { r.Coverage.SummarizedThroughSeq = r.Coverage.SourceThroughSeq + 1 }},
			{"coverage_requires_request", func(r *compact.Request) {
				r.RequireInContext = false
				r.Coverage.RequireInContext = true
				r.InContextRequest = nil
			}},
		} {
			t.Run(path+"/"+tt.name, func(t *testing.T) {
				llm := &coverageModel{}
				req := coverageRequest(t, llm)
				tt.mutate(&req)
				compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
				var result compact.Result
				var err error
				if path == "force" {
					result, err = compactor.Force(t.Context(), req, "test")
				} else {
					result, err = compactor.compact(t.Context(), req, "test")
				}
				var coverageErr *compact.CoverageError
				if !errors.As(err, &coverageErr) {
					t.Fatalf("error = %v, want CoverageError", err)
				}
				if coverageErr.Reason == "" || coverageErr.Retryable() {
					t.Fatalf("invalid coverage error: %#v", coverageErr)
				}
				if llm.calls != 0 || result.Compacted || result.CompactEvent != nil {
					t.Fatalf("calls=%d result=%+v", llm.calls, result)
				}
			})
		}
	}
}

func TestCompactionCoverageAllowsCheckpointAfterRawTail(t *testing.T) {
	llm := &coverageModel{}
	req := coverageRequest(t, llm)
	checkpoint := &session.Event{ID: "checkpoint", Seq: 3, Type: session.EventTypeCompact, Text: "older summary", Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(session.CheckpointData{ContractVersion: compact.CompactContractVersion, SummarizedThroughSeq: 1, SummarizedThroughID: "user"})}}
	req.Events = append(req.Events, checkpoint)
	req.InContextRequest.Messages = []model.Message{
		model.NewTextMessage(model.RoleUser, "older summary"),
		model.NewTextMessage(model.RoleAssistant, "continue validation"),
	}
	bindCoverage(t, &req)
	if req.Coverage.SourceThroughSeq != 3 || req.Coverage.SummarizedThroughSeq != 2 {
		t.Fatalf("coverage = %+v", req.Coverage)
	}
	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
	result, err := compactor.Force(t.Context(), req, "test")
	if err != nil || !result.Compacted || llm.calls != 1 {
		t.Fatalf("calls=%d result=%+v error=%v", llm.calls, result, err)
	}
	data, ok := session.CheckpointDataFromEvent(result.CompactEvent)
	if !ok || data.SummarizedThroughSeq != 2 || data.SourceEventCount != 1 {
		t.Fatalf("checkpoint coverage = %+v", data)
	}
}

func TestCompactionRequireInContextNeverFallsBack(t *testing.T) {
	for _, requirement := range []string{"request", "coverage"} {
		for _, failure := range []string{"empty", "tools", "length", "error"} {
			t.Run(requirement+"/"+failure, func(t *testing.T) {
				llm := &coverageModel{response: &model.Response{TurnComplete: true}}
				switch failure {
				case "tools":
					llm.response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call", Name: "tool"}}, "")
				case "length":
					llm.response.FinishReason = model.FinishReasonLength
					llm.response.FinishReasonReported = true
				case "error":
					llm.err = errors.New("generation failed")
				}
				req := coverageRequest(t, llm)
				if requirement == "coverage" {
					req.RequireInContext = false
					req.Coverage.RequireInContext = true
				}
				compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
				result, err := compactor.Force(t.Context(), req, "test")
				if err == nil || llm.calls != 1 || result.Compacted || result.CompactEvent != nil {
					t.Fatalf("calls=%d result=%+v error=%v", llm.calls, result, err)
				}
			})
		}
	}
}

func TestCompactionRequireInContextOptionalRetainsFallback(t *testing.T) {
	llm := &coverageModel{err: errors.New("in-context generation failed")}
	req := coverageRequest(t, llm)
	req.RequireInContext, req.Coverage = false, nil
	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
	result, err := compactor.Force(t.Context(), req, "test")
	if err != nil || !result.Compacted || llm.calls != 2 {
		t.Fatalf("calls=%d result=%+v error=%v", llm.calls, result, err)
	}
}

type invocationFaultModel struct {
	mode    string
	calls   int
	cancel  context.CancelFunc
	failure error
}

func (*invocationFaultModel) Name() string         { return "invocation-probe" }
func (*invocationFaultModel) ProviderName() string { return "probe" }
func (m *invocationFaultModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "CONTEXT CHECKPOINT\n\nUser objective remains inspect evidence. Tool outcomes not observed remain unknown. Next action: verify results.")
		if m.mode == "repair" && m.calls == 1 {
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{Name: "echo", Args: "{}"}}, "")
		}
		if m.cancel != nil {
			m.cancel()
		}
		response := &model.Response{Message: message, Provider: "probe", Model: m.Name(), Usage: model.Usage{PromptTokens: 10, CachedInputTokens: 3, CompletionTokens: 2, TotalTokens: 12}, TurnComplete: m.failure == nil}
		if !yield(&model.StreamEvent{Type: model.StreamEventPartDelta, Response: response}, nil) {
			return
		}
		if m.failure != nil {
			yield(nil, m.failure)
		}
	}
}

func TestRuntimeInvocationReceiptsSurviveRepairFailureCancellationAndReopen(t *testing.T) {
	for _, mode := range []string{"repair", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			llm := &invocationFaultModel{mode: mode}
			if mode == "failure" {
				llm.failure = errors.New("provider stream failure")
			}
			if mode == "cancel" {
				llm.cancel = cancel
			}
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{Name: "main", Model: llm}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainRunnerEvents(t, run.Handle)
			if mode == "repair" && err != nil {
				t.Fatal(err)
			}
			if mode == "failure" && !errors.Is(err, llm.failure) {
				t.Fatalf("provider cause lost: %v", err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			before, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			after, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Events, after.Events) {
				t.Fatal("reopen changed whole durable events")
			}
			count := 0
			for _, event := range after.Events {
				if session.IsModelInvocationReceipt(event) {
					count++
					if session.IsMainInvocationVisibleEvent(event) || session.IsClientReplayEvent(event) {
						t.Fatal("receipt entered context/replay")
					}
				}
			}
			if count != llm.calls || count == 0 {
				t.Fatalf("calls=%d durable receipts=%d", llm.calls, count)
			}
			total := 0
			for _, event := range session.InvocationAccountingEvents(after.Events) {
				if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
					total += usage.TotalTokens
				}
			}
			if total != 12*llm.calls {
				t.Fatalf("tokens=%d calls=%d", total, llm.calls)
			}
		})
	}
}

func TestRuntimeCompactionPersistsEveryAttemptEvenWhenCheckpointFails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			sessions, active := newTestSessionService(t, "compact-usage")
			for range 6 {
				if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent("inspect retained evidence and missing results")}); err != nil {
					t.Fatal(err)
				}
			}
			rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			llm := &invocationFaultModel{}
			if fail {
				llm.failure = errors.New("compaction provider failed")
			}
			result, err := rt.Compact(context.Background(), CompactRequest{SessionRef: active.SessionRef, Model: llm, Trigger: "manual"})
			if fail && !errors.Is(err, llm.failure) {
				t.Fatalf("cause lost: %v", err)
			}
			if !fail && (err != nil || !result.Compacted) {
				t.Fatalf("compact=%#v error=%v", result, err)
			}
			loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range loaded.Events {
				if session.IsModelInvocationReceipt(event) {
					count++
				}
			}
			if count != llm.calls || count == 0 {
				t.Fatalf("calls=%d receipts=%d", llm.calls, count)
			}
			if result.Session.Revision != loaded.Session.Revision {
				t.Fatalf("returned revision=%d stored=%d", result.Session.Revision, loaded.Session.Revision)
			}
		})
	}
}

type failingInvocationStore struct {
	session.Service
	failure error
}

func (s failingInvocationStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if session.IsModelInvocationReceipt(req.Event) {
		return nil, s.failure
	}
	return s.Service.AppendEvent(ctx, req)
}
func TestRuntimeReceiptPersistenceFailureCannotBecomeSuccessfulTurn(t *testing.T) {
	store, active := newTestSessionService(t, "receipt-failure")
	failure := errors.New("receipt write failed")
	rt, err := New(Config{Sessions: failingInvocationStore{Service: store, failure: failure}, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &invocationFaultModel{}
	run, err := rt.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{Name: "main", Model: llm}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainRunnerEvents(t, run.Handle)
	if !errors.Is(err, failure) || llm.calls != 1 {
		t.Fatalf("error=%v calls=%d", err, llm.calls)
	}
	events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == session.EventTypeAssistant {
			t.Fatal("receipt failure yielded durable success")
		}
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
	started := next.scope.job != nil
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
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_%t", async), func(t *testing.T) {
			h := newAsyncHarness(t, "")
			h.rt.compaction.Async.Enabled = async
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
