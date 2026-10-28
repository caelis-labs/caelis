package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const asyncFixtureSummary = "CONTEXT CHECKPOINT\n\nUser objective: complete the synthetic fixture. Approval is still required for external effects. Preserve unknown outcomes and the unanswered question; closing it grants no permission. First tool completed. Continue from the raw tail."

type asyncProbeRequest struct {
	request *model.Request
	scope   *asyncCompactScope
}

type asyncProbeModel struct {
	foreground chan asyncProbeRequest
	background chan *model.Request
	responses  chan snapshotResponse
	summaries  chan snapshotResponse
	ignoreStop bool
}

func newAsyncProbeModel() *asyncProbeModel {
	return &asyncProbeModel{
		foreground: make(chan asyncProbeRequest, 8), background: make(chan *model.Request, 8),
		responses: make(chan snapshotResponse, 8), summaries: make(chan snapshotResponse, 8),
	}
}

func (*asyncProbeModel) Name() string                     { return "async-fixture" }
func (*asyncProbeModel) ProviderName() string             { return "fixture" }
func (*asyncProbeModel) ContextWindowTokens() int         { return 10000 }
func (*asyncProbeModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (m *asyncProbeModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		responses := m.responses
		background := isRuntimeCompactionRequest(req)
		if background {
			m.background <- model.CloneRequest(req)
			responses = m.summaries
		} else {
			m.foreground <- asyncProbeRequest{model.CloneRequest(req), asyncScope(ctx)}
		}
		var response snapshotResponse
		if background && m.ignoreStop {
			response = <-responses
		} else {
			select {
			case response = <-responses:
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
		if response.err != nil {
			yield(nil, response.err)
			return
		}
		yield(model.StreamEventFromResponse(response.response), nil)
	}
}

func asyncTextResponse(text string, tokens int) snapshotResponse {
	return snapshotResponse{response: &model.Response{
		Message: model.NewTextMessage(model.RoleAssistant, text), TurnComplete: true, StepComplete: true,
		Usage: model.Usage{Reported: true, PromptTokens: tokens, CompletionTokens: 16, TotalTokens: tokens + 16},
	}}
}

func asyncToolResponse(id string) snapshotResponse {
	return snapshotResponse{response: &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewToolUsePart(id, "work", []byte(`{}`))}},
		TurnComplete: true, StepComplete: true,
	}}
}

type asyncFaultStore struct {
	*sessionfile.Store
	fault            string
	injected         atomic.Bool
	beforeCheckpoint func()
}

func (s *asyncFaultStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	checkpoint := compact.IsCompactEvent(req.Event)
	if checkpoint && s.beforeCheckpoint != nil {
		s.beforeCheckpoint()
	}
	if checkpoint && s.fault == "before" {
		return nil, errors.New("fixture: checkpoint disk write failed")
	}
	if checkpoint && s.fault == "cas" && s.injected.CompareAndSwap(false, true) {
		_, err := s.Store.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: req.SessionRef, MutationGuard: req.MutationGuard, Event: userTextEvent("concurrent CAS tail: do not publish"),
		})
		if err != nil {
			return nil, err
		}
	}
	event, err := s.Store.AppendEvent(ctx, req)
	if err == nil && ((checkpoint && s.fault == "after") || (session.IsModelInvocationReceipt(req.Event) && s.fault == "receipt_after" && req.Event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "compaction")) {
		return nil, errors.New("fixture: committed response lost")
	}
	return event, err
}

type asyncHarness struct {
	root          string
	rt            *Runtime
	store         *asyncFaultStore
	active        session.Session
	ctx           context.Context
	fence         session.SessionFence
	llm           *asyncProbeModel
	source        *requestSnapshotSource
	effects       atomic.Int64
	secondTool    chan struct{}
	releaseSecond chan struct{}
	tool          tool.Tool
}

func newAsyncHarness(t *testing.T, fault string, workspace ...session.WorkspaceRef) *asyncHarness {
	t.Helper()
	root := t.TempDir()
	store := &asyncFaultStore{Store: sessionfile.NewStore(sessionfile.Config{RootDir: root}), fault: fault}
	start := session.StartSessionRequest{AppName: "async-poc", UserID: "fixture"}
	if len(workspace) > 0 {
		start.Workspace = workspace[0]
	}
	active, err := store.StartSession(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent(strings.Repeat("synthetic history; ", 600))})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "fixture-host"})
	if err != nil {
		t.Fatal(err)
	}
	h := &asyncHarness{root: root, store: store, active: active, fence: fence, ctx: session.ContextWithRuntimeFence(t.Context(), fence),
		llm: newAsyncProbeModel(), source: &requestSnapshotSource{}, secondTool: make(chan struct{}), releaseSecond: make(chan struct{})}
	h.tool = tool.NamedTool{Def: tool.Definition{Name: "work", Description: "synthetic work", EffectClass: tool.EffectReadOnly,
		InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if h.effects.Add(1) == 2 {
			close(h.secondTool)
			select {
			case <-h.releaseSecond:
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
		}
		return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewTextPart("raw tool fact: outcome unknown; original receipt retained")}}, nil
	}}
	h.source.set(agent.ModelRequestSnapshot{Revision: "epoch-1", Model: h.llm, Tools: []tool.Tool{h.tool}})
	h.rt, err = New(Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{
		Enabled: true, WatermarkRatio: .9, ForceWatermarkRatio: .95, ReserveOutputTokens: 100, SafetyMarginTokens: 100,
		Async: AsyncCompactionConfig{Enabled: true, StartRatio: .1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *asyncHarness) run(t *testing.T) agent.RunResult {
	t.Helper()
	run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue fixture", AgentSpec: agent.AgentSpec{ResolveModelRequest: h.source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = run.Handle.Close()
		select {
		case h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100):
		default:
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = run.Handle.WaitCompletion(cleanup)
	})
	return run
}

func (h *asyncHarness) startCandidate(t *testing.T) asyncProbeRequest {
	t.Helper()
	receiveSnapshotValue(t, h.llm.foreground)
	h.llm.responses <- asyncToolResponse("first")
	next := receiveSnapshotValue(t, h.llm.foreground)
	background := receiveSnapshotValue(t, h.llm.background)
	want := model.CloneRequest(next.request)
	want.Messages = append(want.Messages, model.NewTextMessage(model.RoleUser, inContextCompactionPrompt))
	if !reflect.DeepEqual(background, want) {
		t.Fatal("background must preserve the complete foreground request and append only the compaction instruction")
	}
	if next.scope == nil || next.scope.job == nil {
		t.Fatal("candidate not launched")
	}
	return next
}

func (h *asyncHarness) completeCandidate(t *testing.T, scope *asyncCompactScope) {
	t.Helper()
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100)
	receiveSnapshotValue(t, scope.job.done)
	if scope.job.err != nil || scope.job.receiptErr != nil {
		t.Fatalf("generation=%v receipt=%v", scope.job.err, scope.job.receiptErr)
	}
}

func (h *asyncHarness) load(t *testing.T) session.LoadedSession {
	t.Helper()
	loaded, err := h.store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: h.active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestAsyncCompactionFrozenRequestMatchesForeground(t *testing.T) {
	h := newAsyncHarness(t, "")
	h.source.set(agent.ModelRequestSnapshot{
		Revision: "epoch-1", Model: h.llm, Tools: []tool.Tool{h.tool},
		Instructions: []model.Part{model.NewTextPart("preserve these exact instructions")},
		Reasoning:    model.ReasoningConfig{Effort: "high"},
		Request:      agent.ModelRequestOptions{ServiceTier: model.ServiceTierPriority},
	})
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	data, ok := compact.CompactEventDataFromEvent(next.scope.job.result.CompactEvent)
	if !ok || data.Generator != "model_context" {
		t.Fatalf("generator = %q, want model_context", data.Generator)
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCompactionForegroundTailSteeringAndFileReplay(t *testing.T) {
	h := newAsyncHarness(t, "cas")
	run := h.run(t)
	next := h.startCandidate(t)
	// A second provider request and tool run complete admission while the
	// summary is deliberately blocked. No write queue is held by generation.
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	if err := run.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindConversation, Text: "steering: keep the question open; do not publish"}); err != nil {
		t.Fatal(err)
	}
	h.completeCandidate(t, next.scope)
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground).request
	joined := asyncMessagesText(after.Messages)
	if !strings.Contains(joined, "CONTEXT CHECKPOINT") || !strings.Contains(joined, "steering:") || !strings.Contains(joined, "concurrent CAS tail") {
		t.Fatalf("adopted request lost checkpoint or tail: %s", joined)
	}
	if strings.Contains(joined, "synthetic history;") {
		t.Fatal("covered source remained")
	}
	if len(after.Messages) < 4 {
		t.Fatalf("tail not retained: %#v", after.Messages)
	}
	h.llm.responses <- asyncTextResponse("fixture complete", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.effects.Load() != 2 {
		t.Fatalf("effects=%d", h.effects.Load())
	}
	loaded := h.load(t)
	checkpoint, data, ok := compact.LatestCompactEvent(loaded.Events)
	if !ok || data.SummarizedThroughSeq != next.scope.job.coverage.SummarizedThroughSeq || checkpoint.ID != next.scope.job.id {
		t.Fatal("coverage/identity mismatch")
	}
	var compactionReceipts, chatReceipts int
	for _, event := range loaded.Events {
		if !session.IsModelInvocationReceipt(event) {
			continue
		}
		if event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "compaction" {
			compactionReceipts++
		} else {
			chatReceipts++
		}
	}
	if compactionReceipts != 1 || chatReceipts != 3 {
		t.Fatalf("receipts compaction=%d chat=%d", compactionReceipts, chatReceipts)
	}
	h.source.mu.Lock()
	admissions := len(h.source.admissions)
	h.source.mu.Unlock()
	if admissions != 4 {
		t.Fatalf("adoption fabricated request admission: %d", admissions)
	}
	gauge, ok := latestProviderTokenSnapshotUsing(mainInvocationEvents(loaded.Events), nil)
	if !ok || gauge.BaselineTokens != 500 {
		t.Fatalf("foreground gauge mixed background usage: %+v", gauge)
	}
	// A fresh Runtime over the same files must build the same complete request,
	// plus precisely the accepted final answer and the new probe input.
	resumed, err := New(Config{Sessions: h.store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := resumed.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "replay probe", AgentSpec: agent.AgentSpec{ResolveModelRequest: h.source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	got := receiveSnapshotValue(t, h.llm.foreground).request
	want := model.CloneRequest(after)
	// This Runtime has speculation disabled; the prior request's local source
	// binding does not describe the replay request with its two new messages.
	want.Source = nil
	want.Messages = append(want.Messages, model.NewTextMessage(model.RoleAssistant, "fixture complete"), model.NewTextMessage(model.RoleUser, "replay probe"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("whole request replay mismatch\ngot: %#v\nwant: %#v", got, want)
	}
	h.llm.responses <- asyncTextResponse("replay complete", 600)
	if err := probe.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCompactionDurabilityAndUnknownWriteReconciliation(t *testing.T) {
	for _, fault := range []string{"before", "after", "receipt_after"} {
		t.Run(fault, func(t *testing.T) {
			h := newAsyncHarness(t, fault)
			run := h.run(t)
			next := h.startCandidate(t)
			h.completeCandidate(t, next.scope)
			h.llm.responses <- asyncToolResponse("second")
			receiveSnapshotValue(t, h.secondTool)
			close(h.releaseSecond)
			if fault != "before" {
				got := receiveSnapshotValue(t, h.llm.foreground)
				if !strings.Contains(asyncMessagesText(got.request.Messages), "CONTEXT CHECKPOINT") {
					t.Fatal("confirmed commit not adopted")
				}
				h.llm.responses <- asyncTextResponse("done", 500)
			}
			err := run.Handle.WaitCompletion(t.Context())
			if (err != nil) != (fault == "before") {
				t.Fatalf("completion=%v", err)
			}
			loaded := h.load(t)
			checkpoints, receipts := 0, 0
			for _, event := range loaded.Events {
				if compact.IsCompactEvent(event) {
					checkpoints++
				}
				if session.IsModelInvocationReceipt(event) && event.ID == next.scope.job.result.CompactEvent.ID {
					t.Fatal("checkpoint is not a billable attempt")
				}
				if session.IsModelInvocationReceipt(event) && event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "compaction" {
					receipts++
				}
			}
			want := 1
			if fault == "before" {
				want = 0
			}
			if checkpoints != want || receipts != 1 {
				t.Fatalf("checkpoints=%d receipts=%d", checkpoints, receipts)
			}
			if h.effects.Load() != 2 {
				t.Fatal("tool was replayed or lost")
			}
			// A persistence failure leaves the original source available on resume.
			if fault == "before" && !strings.Contains(fmt.Sprint(compact.PromptEventsFromLatestCompact(loaded.Events)), "synthetic history;") {
				// Compare the actual event texts, not pointer formatting.
				found := false
				for _, event := range compact.PromptEventsFromLatestCompact(loaded.Events) {
					found = found || strings.Contains(session.EventText(event), "synthetic history;")
				}
				if !found {
					t.Fatal("failed checkpoint destroyed original source")
				}
			}
		})
	}
}

func TestAsyncCompactionCancelDrainsBeforeCompletionAndSnapshotRelease(t *testing.T) {
	h := newAsyncHarness(t, "")
	h.llm.ignoreStop = true
	var releases atomic.Int64
	h.source.set(agent.ModelRequestSnapshot{Revision: "epoch-1", Model: h.llm, Tools: []tool.Tool{h.tool}, Release: func() { releases.Add(1) }})
	run := h.run(t)
	next := h.startCandidate(t)
	if err := run.Handle.Cancel().Err; err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := run.Handle.WaitCompletion(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("producer not drained but completion=%v", err)
	}
	if releases.Load() != 1 {
		t.Fatalf("candidate snapshot prematurely released: %d", releases.Load())
	}
	// Even a provider's late success is only accounting after cancellation.
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100)
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("completion=%v", err)
	}
	if releases.Load() != 2 {
		t.Fatalf("snapshot leases not released: %d", releases.Load())
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("cancelled candidate adopted")
	}
	if next.scope.job.receiptErr != nil {
		t.Fatal(next.scope.job.receiptErr)
	}
	if err := h.store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(h.fence)); err != nil {
		t.Fatal(err)
	}
	// Returning completion means there are no remaining candidate writes.
	before := h.load(t).Session.Revision
	if before != h.load(t).Session.Revision {
		t.Fatal("write after completion")
	}
}

func TestAsyncCompactionRejectsChangedSourceAndCutPairs(t *testing.T) {
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	job := next.scope.job
	base := h.load(t)
	for _, change := range []string{"same_length_prefix", "state_revocation", "epoch_metadata", "second_checkpoint", "out_of_order_seq", "cross_cut_result", "open_call"} {
		t.Run(change, func(t *testing.T) {
			current := base
			current.Events = session.CloneEvents(base.Events)
			current.State = session.CloneState(base.State)
			current.Session = session.CloneSession(base.Session)
			switch change {
			case "same_length_prefix":
				current.Events[0].Message = ptrMessage(model.NewTextMessage(model.RoleUser, "different source of the same event count"))
			case "state_revocation":
				if current.State == nil {
					current.State = map[string]any{}
				}
				current.State["approval"] = "revoked"
			case "epoch_metadata":
				current.Session.Metadata = map[string]any{"epoch": "new"}
			case "second_checkpoint":
				event := session.CloneEvent(job.result.CompactEvent)
				event.Seq = session.LastEventSeq(current.Events) + 1
				current.Events = append(current.Events, event)
			case "out_of_order_seq":
				current.Events[0].Seq = 0
			case "cross_cut_result", "open_call":
				kind := session.EventTypeToolResult
				if change == "open_call" {
					kind = session.EventTypeToolCall
				}
				current.Events = append(current.Events, &session.Event{Type: kind, Visibility: session.VisibilityCanonical, Seq: session.LastEventSeq(current.Events) + 1, Tool: &session.EventTool{ID: "first", Name: "work"}})
			}
			if job.validSource(current) {
				t.Fatalf("accepted %s", change)
			}
		})
	}
	h.llm.responses <- asyncTextResponse("done without adoption boundary", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func ptrMessage(message model.Message) *model.Message { return &message }

func TestAsyncCompactionModelAndCatalogEpochChange(t *testing.T) {
	for _, change := range []string{"revision", "model", "schema", "revoke"} {
		t.Run(change, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			run := h.run(t)
			next := h.startCandidate(t)
			h.completeCandidate(t, next.scope)
			h.llm.responses <- asyncToolResponse("second")
			receiveSnapshotValue(t, h.secondTool)
			currentModel := h.llm
			configuration := agent.ModelRequestSnapshot{Revision: "epoch-1", Model: currentModel, Tools: []tool.Tool{h.tool}}
			switch change {
			case "revision", "revoke":
				configuration.Revision = "epoch-2"
			case "model":
				currentModel = newAsyncProbeModel()
				configuration.Model = &renamedAsyncProbe{asyncProbeModel: currentModel}
			case "schema":
				configuration.Tools = nil
			}
			if change == "revoke" {
				configuration.Tools = nil
			}
			h.source.set(configuration)
			close(h.releaseSecond)
			request := receiveSnapshotValue(t, currentModel.foreground).request
			if strings.Contains(asyncMessagesText(request.Messages), "CONTEXT CHECKPOINT") {
				t.Fatal("stale candidate adopted")
			}
			if change == "revoke" && len(request.Tools) != 0 {
				t.Fatal("revoked tools restored")
			}
			currentModel.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("stale candidate durable")
			}
		})
	}
}

type renamedAsyncProbe struct{ *asyncProbeModel }

func (*renamedAsyncProbe) Name() string { return "changed-model" }

func TestAsyncCompactionDefaultOffThresholdsAndFenceRequired(t *testing.T) {
	for _, mode := range []string{"default_off", "invalid_ratio", "above_sync", "below_threshold", "no_fence"} {
		t.Run(mode, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			switch mode {
			case "default_off":
				h.rt.compaction.Async = AsyncCompactionConfig{}
			case "invalid_ratio":
				h.rt.compaction.Async.StartRatio = -1
			case "above_sync":
				h.rt.compaction.Async.StartRatio = .99
			case "below_threshold":
				h.rt.compaction.Async.StartRatio = .8
			case "no_fence":
				if err := h.store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(h.fence)); err != nil {
					t.Fatal(err)
				}
				h.ctx = t.Context()
			}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncToolResponse("first")
			next := receiveSnapshotValue(t, h.llm.foreground)
			if next.scope != nil && next.scope.started {
				t.Fatal("unexpected speculation")
			}
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.llm.background:
				t.Fatal("background dispatch")
			default:
			}
		})
	}
}

func TestAsyncCompactionPendingHardGateCancelsThenFallsBack(t *testing.T) {
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	// Simulate a very large, durably admitted steering input after tool progress.
	if err := run.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindConversation, Text: strings.Repeat("large tail ", 6000)}); err != nil {
		t.Fatal(err)
	}
	close(h.releaseSecond)
	// A fresh steering input is preserved by the ordinary overflow gate. Make
	// the provider reject it; synchronous recovery must cancel the pending job.
	receiveSnapshotValue(t, h.llm.foreground)
	h.llm.responses <- snapshotResponse{err: &model.ContextOverflowError{Cause: errors.New("fixture overflow")}}
	receiveSnapshotValue(t, next.scope.job.done)
	if !errors.Is(next.scope.job.err, context.Canceled) {
		t.Fatalf("candidate cancellation=%v", next.scope.job.err)
	}
	receiveSnapshotValue(t, h.llm.background)
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 5000)
	after := receiveSnapshotValue(t, h.llm.foreground)
	if !strings.Contains(asyncMessagesText(after.request.Messages), "CONTEXT CHECKPOINT") {
		t.Fatal("synchronous fallback not installed")
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.effects.Load() != 2 {
		t.Fatalf("effects=%d", h.effects.Load())
	}
	checkpoints := 0
	for _, event := range h.load(t).Events {
		if compact.IsCompactEvent(event) {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("fallback checkpoints=%d want 1", checkpoints)
	}
}

func TestAsyncCompactionRejectsLostFence(t *testing.T) {
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	// Finish durable tool work first, then exercise adoption directly at the
	// same safe point with a now-stale Host fence.
	if err := h.store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(h.fence)); err != nil {
		t.Fatal(err)
	}
	l := &autoCompactGatedLLM{runtime: h.rt, inner: h.llm, sessionRef: h.active.SessionRef}
	if adopted, err := h.rt.adoptAsyncCompaction(h.ctx, l, next.scope.job); adopted || !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("adopted=%v error=%v", adopted, err)
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("completion=%v", err)
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("stale fence checkpoint")
	}
}

func TestAsyncCompactionTailParallelPairsAndUnknownResults(t *testing.T) {
	event := func(kind session.EventType, id string) *session.Event {
		return &session.Event{Type: kind, Tool: &session.EventTool{ID: id, Output: map[string]any{"status": "unknown_outcome"}}}
	}
	paired := []*session.Event{event(session.EventTypeToolCall, "a"), event(session.EventTypeToolCall, "b"), event(session.EventTypeToolResult, "b"), event(session.EventTypeToolResult, "a")}
	if !asyncToolPairsComplete(paired) {
		t.Fatal("out-of-order parallel results rejected")
	}
	if asyncToolPairsComplete(paired[:3]) || asyncToolPairsComplete(paired[2:]) {
		t.Fatal("partial/cross-cut pair accepted")
	}
}

func TestAsyncCompactionAppendAfterAssemblyRemainsInTail(t *testing.T) {
	h := newAsyncHarness(t, "")
	const marker = "canonical input raced request assembly: never publish COBALT"
	calls := 0
	var appended *session.Event
	h.rt.agentFactory = asyncAssemblyFactory{hook: func(req *model.Request) {
		calls++
		if calls != 2 {
			return
		}
		var err error
		appended, err = h.store.AppendEvent(h.ctx, session.AppendEventRequest{
			SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: userTextEvent(marker),
		})
		if err != nil {
			t.Error(err)
		}
	}}
	run := h.run(t)
	next := h.startCandidate(t)
	if appended == nil || next.request.Source == nil || appended.Seq <= next.request.Source.SourceThroughSeq {
		t.Fatal("fixture append must follow the bound request source")
	}
	if strings.Contains(asyncMessagesText(next.request.Messages), marker) {
		t.Fatal("external append entered the assembled request")
	}
	h.completeCandidate(t, next.scope)
	data, _ := compact.CompactEventDataFromEvent(next.scope.job.result.CompactEvent)
	if data.SummarizedThroughSeq >= appended.Seq {
		t.Fatal("external append entered summary coverage")
	}
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground)
	text := asyncMessagesText(after.request.Messages)
	if !strings.Contains(text, marker) || !strings.Contains(text, "CONTEXT CHECKPOINT") {
		t.Fatalf("adopted context lost tail: %s", text)
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCompactionToolRequestHasNoEffectAndKeepsOriginalContext(t *testing.T) {
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.llm.summaries <- asyncToolResponse("forbidden-background-effect")
	receiveSnapshotValue(t, next.scope.job.done)
	if !errors.Is(next.scope.job.err, errCompactionToolRequest) {
		t.Fatalf("generation=%v", next.scope.job.err)
	}
	select {
	case <-h.llm.background:
		t.Fatal("strict in-context generation fell back")
	default:
	}
	if h.effects.Load() != 1 {
		t.Fatal("background tool executed")
	}
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground)
	if strings.Contains(asyncMessagesText(after.request.Messages), "CONTEXT CHECKPOINT") {
		t.Fatal("unusable candidate adopted")
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.effects.Load() != 2 {
		t.Fatal("foreground effects changed")
	}
}

func TestAsyncCompactionCoverageContractCannotAdvanceOrRegress(t *testing.T) {
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	l := &autoCompactGatedLLM{runtime: h.rt, inner: h.llm, sessionRef: h.active.SessionRef}
	original := next.scope.job.result
	data, _ := compact.CompactEventDataFromEvent(original.CompactEvent)
	for _, mutation := range []func(*compact.CompactEventData){
		func(d *compact.CompactEventData) { d.SummarizedThroughSeq++ },
		func(d *compact.CompactEventData) { d.SummarizedThroughSeq-- },
		func(d *compact.CompactEventData) { d.SummarizedThroughID = "wrong" },
		func(d *compact.CompactEventData) { d.ContractVersion++ },
		func(d *compact.CompactEventData) { d.Revision++ },
		func(d *compact.CompactEventData) { d.SourceEventCount++ },
	} {
		changed := data
		mutation(&changed)
		next.scope.job.result.CompactEvent = buildCompactEvent(h.active, asyncFixtureSummary, changed)
		if adopted, err := h.rt.adoptAsyncCompaction(h.ctx, l, next.scope.job); adopted || err != nil {
			t.Fatalf("bad coverage adopted=%v err=%v", adopted, err)
		}
	}
	next.scope.job.result = original
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCompactionRevokedAuthorityDoesNotAdoptOrDispatch(t *testing.T) {
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	revoked := errors.New("fixture authority revoked")
	h.source.set(agent.ModelRequestSnapshot{Revision: "epoch-1", Model: h.llm, Tools: []tool.Tool{h.tool}, Validate: func(context.Context) (func(), error) { return nil, revoked }})
	close(h.releaseSecond)
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, revoked) {
		t.Fatalf("completion=%v", err)
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("revoked candidate adopted")
	}
	select {
	case <-h.llm.foreground:
		t.Fatal("provider dispatched after revocation")
	default:
	}
}

func TestAsyncCompactionValidationGuardsEpochThroughDurableCommit(t *testing.T) {
	h := newAsyncHarness(t, "")
	entered, finish := make(chan struct{}), make(chan struct{})
	h.store.beforeCheckpoint = func() { close(entered); <-finish }
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	close(h.releaseSecond)
	receiveSnapshotValue(t, entered)
	updated := make(chan struct{})
	go func() {
		h.source.set(agent.ModelRequestSnapshot{Revision: "epoch-2", Model: h.llm, Tools: []tool.Tool{h.tool}})
		close(updated)
	}()
	select {
	case <-updated:
		t.Fatal("epoch update crossed guarded checkpoint commit")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	receiveSnapshotValue(t, updated)
	receiveSnapshotValue(t, h.llm.foreground)
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); !ok {
		t.Fatal("validated checkpoint absent")
	}
}

func asyncMessagesText(messages []model.Message) string {
	data, _ := json.Marshal(messages)
	return string(data)
}
