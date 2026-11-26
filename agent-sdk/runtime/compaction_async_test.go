package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestmeta"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"iter"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const asyncFixtureSummary = "CONTEXT CHECKPOINT\n\nUser objective: complete the synthetic fixture. Approval is still required for external effects. Preserve unknown outcomes and the unanswered question; closing it grants no permission. First tool completed. Continue from the raw tail."

type asyncProbeRequest struct {
	request *model.Request
	scope   *asyncCompactScope
}

type asyncProbeModel struct {
	foreground     chan asyncProbeRequest
	background     chan *model.Request
	responses      chan snapshotResponse
	summaries      chan snapshotResponse
	ignoreStop     bool
	imageInput     bool
	cancelObserved chan struct{}
}

func newAsyncProbeModel() *asyncProbeModel {
	return &asyncProbeModel{
		foreground: make(chan asyncProbeRequest, 8), background: make(chan *model.Request, 8),
		responses: make(chan snapshotResponse, 8), summaries: make(chan snapshotResponse, 8),
	}
}

func (*asyncProbeModel) Name() string             { return "async-fixture" }
func (*asyncProbeModel) ProviderName() string     { return "fixture" }
func (*asyncProbeModel) ContextWindowTokens() int { return 10000 }
func (m *asyncProbeModel) Capabilities() model.Capabilities {
	caps := runtimeTestModelCapabilities()
	caps.ImageInput = m.imageInput
	return caps
}
func (m *asyncProbeModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		responses := m.responses
		background := isRuntimeCompactionRequest(req)
		if background {
			if m.cancelObserved != nil {
				stop := context.AfterFunc(ctx, func() { close(m.cancelObserved) })
				defer stop()
			}
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
	instructions  agent.SessionInstructionsResolver
}

func newAsyncHarness(t *testing.T, fault string, workspace ...session.WorkspaceRef) *asyncHarness {
	t.Helper()
	root := t.TempDir()
	store := &asyncFaultStore{Store: newLogicalFileStore(t, sessionfile.Config{RootDir: root}), fault: fault}
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
	h.tool = tool.NamedTool{Def: tool.Definition{Name: "work", Description: "synthetic work",
		InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if h.effects.Add(1) == 2 {
			close(h.secondTool)
			select {
			case <-h.releaseSecond:
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
		}
		return tool.Result{Content: []tool.ContentBlock{tool.Text("raw tool fact: outcome unknown; original receipt retained")}}, nil
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
	run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue fixture", AgentSpec: agent.AgentSpec{
		ResolveModelRequest: h.source.resolve, ResolveSessionInstructions: h.instructions,
	}})
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
	t.Parallel()
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
	data, ok := session.CheckpointDataFromEvent(next.scope.job.result.CompactEvent)
	if !ok || data.Generator != "model_context" {
		t.Fatalf("generator = %q, want model_context", data.Generator)
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCompactionForegroundTailSteeringAndFileReplay(t *testing.T) {
	t.Parallel()
	h := newAsyncHarness(t, "cas")
	h.instructions = func(_ context.Context, summaryID string) (string, func(context.Context) error, error) {
		return "Session instructions from summary=" + summaryID, nil, nil
	}
	run := h.run(t)
	next := h.startCandidate(t)
	if want := []model.Part{model.NewTextPart("Session instructions from summary=")}; !reflect.DeepEqual(next.request.Instructions, want) {
		t.Fatalf("request while generating candidate used a different prefix: %+v", next.request.Instructions)
	}
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
	if want := []model.Part{model.NewTextPart("Session instructions from summary=" + next.scope.job.id)}; !reflect.DeepEqual(after.Instructions, want) {
		t.Fatalf("first request using the adopted summary did not update instructions: %+v", after.Instructions)
	}
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
	gauge, ok := latestProviderTokenSnapshotUsing(mainInvocationEvents(loaded.Events), 0, nil)
	if !ok || gauge.BaselineTokens != 500 {
		t.Fatalf("foreground gauge mixed background usage: %+v", gauge)
	}
	// A fresh Runtime over the same files must build the same complete request,
	// plus precisely the accepted final answer and the new probe input.
	resumed, err := New(Config{Sessions: h.store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := resumed.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "replay probe", AgentSpec: agent.AgentSpec{
		ResolveModelRequest: h.source.resolve, ResolveSessionInstructions: h.instructions,
	}})
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestAsyncCompactionDefaultOffAndThresholds(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"default_off", "invalid_ratio", "above_sync", "below_threshold"} {
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

			}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncToolResponse("first")
			next := receiveSnapshotValue(t, h.llm.foreground)
			if next.scope != nil && next.scope.job != nil {
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
	t.Parallel()
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

func TestAsyncCompactionBudgetAfterAdoption(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		largeTail bool
	}{
		{name: "old provider usage"},
		{name: "large retained tool input", largeTail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			run := h.run(t)
			next := h.startCandidate(t)
			second := asyncToolResponse("second")
			second.response.Usage = model.Usage{Reported: true, PromptTokens: 9_100, CompletionTokens: 5, TotalTokens: 9_105}
			if test.largeTail {
				args, err := json.Marshal(map[string]string{"detail": strings.Repeat("retained input with detail ", 1500)})
				if err != nil {
					t.Fatal(err)
				}
				second.response.Message.Parts = []model.Part{model.NewToolUsePart("second", "work", args)}
			}
			h.llm.responses <- second
			receiveSnapshotValue(t, h.secondTool)
			h.completeCandidate(t, next.scope)
			close(h.releaseSecond)

			wantTrigger := "async_candidate"
			select {
			case req := <-h.llm.background:
				if !test.largeTail {
					t.Fatal("old provider usage triggered a second compaction")
				}
				checkpoint, _, _ := compact.LatestCompactEvent(h.load(t).Events)
				if checkpoint == nil || checkpoint.ID != next.scope.job.id {
					t.Fatalf("ready candidate was not adopted before synchronous recovery: %+v", checkpoint)
				}
				if !strings.Contains(asyncMessagesText(req.Messages), "CONTEXT CHECKPOINT") {
					t.Fatal("synchronous recovery did not use the newly committed checkpoint")
				}
				assembled := model.CloneRequest(req)
				assembled.Messages = assembled.Messages[:len(assembled.Messages)-1]
				usage, _ := usageForModelRequest(mainInvocationEvents(h.load(t).Events), h.llm, assembled, h.rt.compaction)
				if usage.Source != compact.UsageSourceEstimated || !evaluateWatermark(usage, h.rt.compaction).ShouldCompact {
					t.Fatalf("large tail should exceed the budget on its own: %+v", usage)
				}
				h.llm.summaries <- asyncTextResponse(asyncFixtureSummary+" Second checkpoint.", 5_000)
				receiveSnapshotValue(t, h.llm.foreground)
				wantTrigger = "model_request_context_limit"
			case nextRequest := <-h.llm.foreground:
				if test.largeTail {
					t.Fatal("large retained input reached the provider without recovery")
				}
				events := mainInvocationEvents(h.load(t).Events)
				usage, _ := usageForModelRequest(events, h.llm, nextRequest.request, h.rt.compaction)
				if usage.Source != compact.UsageSourceEstimated || usage.TotalTokens >= 1_000 {
					t.Fatalf("post-checkpoint budget = %+v", usage)
				}
				gauge := computeUsageSnapshotForModel(events, h.llm.ContextWindowTokens(), h.rt.compaction,
					h.llm.ProviderName(), h.llm.Name(), estimatePromptEventTokens)
				if gauge.Source != compact.UsageSourceEstimated || gauge.TotalTokens >= 1_000 {
					t.Fatalf("post-checkpoint gauge = %+v", gauge)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("neither ordinary dispatch nor synchronous recovery reached the model")
			}
			h.llm.responses <- asyncTextResponse("fixture complete", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			events := h.load(t).Events
			_, data, _ := compact.LatestCompactEvent(events)
			if data.Trigger != wantTrigger || h.effects.Load() != 2 {
				t.Fatalf("checkpoint trigger = %q, want %q; effects = %d", data.Trigger, wantTrigger, h.effects.Load())
			}
			usage := computeUsageSnapshotForModel(events, h.llm.ContextWindowTokens(), h.rt.compaction,
				h.llm.ProviderName(), h.llm.Name(), estimatePromptEventTokens)
			if usage.Source != compact.UsageSourceProvider || usage.TotalTokens >= 1_000 {
				t.Fatalf("fresh provider usage did not become the context baseline: %+v", usage)
			}
		})
	}
}

func TestAsyncCompactionRejectsLostFence(t *testing.T) {
	t.Parallel()
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
	if adopted, err := h.rt.adoptAsyncCompaction(h.ctx, l, next.scope.job); adopted != nil || !errors.Is(err, session.ErrFenceConflict) {
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
	t.Parallel()
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
	t.Parallel()
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
	data, _ := session.CheckpointDataFromEvent(next.scope.job.result.CompactEvent)
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
	t.Parallel()
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
	t.Parallel()
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	l := &autoCompactGatedLLM{runtime: h.rt, inner: h.llm, sessionRef: h.active.SessionRef}
	original := next.scope.job.result
	data, _ := session.CheckpointDataFromEvent(original.CompactEvent)
	for _, mutation := range []func(*session.CheckpointData){
		func(d *session.CheckpointData) { d.SummarizedThroughSeq++ },
		func(d *session.CheckpointData) { d.SummarizedThroughSeq-- },
		func(d *session.CheckpointData) { d.SummarizedThroughID = "wrong" },
		func(d *session.CheckpointData) { d.ContractVersion++ },
		func(d *session.CheckpointData) { d.Revision++ },
		func(d *session.CheckpointData) { d.SourceEventCount++ },
	} {
		changed := data
		mutation(&changed)
		next.scope.job.result.CompactEvent = buildCompactEvent(h.active, asyncFixtureSummary, changed)
		if adopted, err := h.rt.adoptAsyncCompaction(h.ctx, l, next.scope.job); adopted != nil || err != nil {
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
	t.Parallel()
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
	t.Parallel()
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

func TestAsyncCompactionActualDrainPrecedesCanonicalFinal(t *testing.T) {
	t.Parallel()
	h := newAsyncHarness(t, "")
	h.llm.ignoreStop = true
	h.llm.cancelObserved = make(chan struct{})
	run := h.run(t)
	h.startCandidate(t)
	h.llm.responses <- asyncTextResponse("candidate after drain", 6000)
	receiveSnapshotValue(t, h.llm.cancelObserved)
	for _, e := range h.load(t).Events {
		if e.Type == session.EventTypeAssistant && e.Message != nil && e.Message.TextContent() == "candidate after drain" {
			t.Fatal("final committed before the real producer drained")
		}
	}
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range h.load(t).Events {
		found = found || e.Type == session.EventTypeAssistant && e.Message != nil && e.Message.TextContent() == "candidate after drain"
	}
	if !found {
		t.Fatal("accepted final absent after actual drain")
	}
}

func TestAsyncCompactionStartsWithNormalizedToolHistory(t *testing.T) {
	t.Parallel()
	h := newAsyncHarness(t, "")
	call := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "invalid", Name: "work", Args: `{"input":`}}, "")
	result := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("invalid", "work", map[string]any{"error": "invalid arguments"}, true))
	for _, event := range []*session.Event{
		{Type: session.EventTypeToolCall, Message: &call, Tool: &session.EventTool{ID: "invalid", Name: "work", Status: "pending"}},
		{Type: session.EventTypeToolResult, Message: &result, Tool: &session.EventTool{ID: "invalid", Name: "work", Status: "error"}},
	} {
		if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{
			SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: event,
		}); err != nil {
			t.Fatal(err)
		}
	}
	run := h.run(t)
	foreground := receiveSnapshotValue(t, h.llm.foreground)
	if len(foreground.request.Messages) != 2 {
		t.Fatalf("messages = %#v, want history and current user input without the invalid tool batch", foreground.request.Messages)
	}
	if foreground.request.Source == nil {
		t.Fatal("normalized request has no source binding")
	}
	// Speculation requires model/tool progress after the current user input.
	h.llm.responses <- asyncToolResponse("first")
	foreground = receiveSnapshotValue(t, h.llm.foreground)
	if foreground.request.Source == nil {
		t.Fatal("normalized history lost its source binding after tool progress")
	}
	background := receiveSnapshotValue(t, h.llm.background)
	want := model.CloneRequest(foreground.request)
	want.Messages = append(want.Messages, model.NewTextMessage(model.RoleUser, inContextCompactionPrompt))
	if !reflect.DeepEqual(background, want) {
		t.Fatal("candidate did not reuse the normalized foreground request")
	}
	h.completeCandidate(t, foreground.scope)
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type asyncMeasurement struct {
	Evidence         string             `json:"evidence"`
	Mode             string             `json:"mode"`
	ForegroundWaitMS int64              `json:"foreground_wait_ms"`
	GenerationMS     int64              `json:"generation_ms"`
	AdoptionMS       int64              `json:"adoption_ms"`
	Usage            model.Usage        `json:"usage"`
	Attempts         []model.Invocation `json:"attempts,omitempty"`
	Answer           string             `json:"answer,omitempty"`
}

func writeAsyncMeasurements(t *testing.T, measurements []asyncMeasurement) {
	t.Helper()
	data, err := json.MarshalIndent(measurements, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if path := os.Getenv("CAELIS_ASYNC_POC_REPORT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// This measures controlled scheduling, not provider performance. A 300 ms
// summary delay is inserted in both runs; input/output usage is synthetic.
func TestAsyncCompactionFixtureMeasurement(t *testing.T) {
	t.Parallel()
	var measurements []asyncMeasurement
	for _, mode := range []string{"synchronous", "async"} {
		h := newAsyncHarness(t, "")
		if mode == "synchronous" {
			h.rt.compaction.Async.Enabled = false
			h.rt.compaction.WatermarkRatio = .1
		}
		run := h.run(t)
		receiveSnapshotValue(t, h.llm.foreground)
		start := time.Now()
		h.llm.responses <- asyncToolResponse("first")
		receiveSnapshotValue(t, h.llm.background)
		generationStart := time.Now()
		var next asyncProbeRequest
		if mode == "async" {
			next = receiveSnapshotValue(t, h.llm.foreground)
		}
		foregroundWait := time.Since(start)
		// Only the test's response scheduler sleeps; production never waits for
		// speculative readiness at an ordinary model request boundary.
		time.Sleep(300 * time.Millisecond)
		h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100)
		if mode == "async" {
			receiveSnapshotValue(t, next.scope.job.done)
		} else {
			next = receiveSnapshotValue(t, h.llm.foreground)
			foregroundWait = time.Since(start)
		}
		generation := time.Since(generationStart)
		h.llm.responses <- asyncToolResponse("second")
		receiveSnapshotValue(t, h.secondTool)
		adoptionStart := time.Now()
		close(h.releaseSecond)
		receiveSnapshotValue(t, h.llm.foreground)
		adoption := time.Since(adoptionStart)
		h.llm.responses <- asyncTextResponse("done", 500)
		if err := run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
		measurements = append(measurements, asyncMeasurement{
			Evidence: "fixture; 300ms injected generation delay; synthetic usage", Mode: mode,
			ForegroundWaitMS: foregroundWait.Milliseconds(), GenerationMS: generation.Milliseconds(), AdoptionMS: adoption.Milliseconds(),
			Usage: model.Usage{Reported: true, PromptTokens: 2100, CompletionTokens: 16, TotalTokens: 2116},
		})
	}
	writeAsyncMeasurements(t, measurements)
}

// asyncLiveModel uses a real provider only for candidate generation. Foreground
// steps remain controlled so timing and fault interleavings are reproducible.
// Its 10k window is a test trigger, not a claim about the provider's capacity.
type asyncLiveModel struct {
	*asyncProbeModel
	provider model.LLM
	started  chan struct{}
}

func (m *asyncLiveModel) Name() string { return m.provider.Name() }
func (m *asyncLiveModel) ProviderName() string {
	return m.provider.(interface{ ProviderName() string }).ProviderName()
}
func (*asyncLiveModel) TracksInvocations() {}
func (m *asyncLiveModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if isRuntimeCompactionRequest(req) {
		select {
		case m.started <- struct{}{}:
		default:
		}
		return model.Generate(ctx, m.provider, req)
	}
	return model.Generate(ctx, m.asyncProbeModel, req)
}

// Run explicitly with CAELIS_ASYNC_LIVE=1 and DEEPSEEK_API_KEY. No Host, user
// configuration or production Session is opened. All transmitted text is the
// synthetic fixture below, the fixed compaction prompt, and inert tool specs.
func TestAsyncCompactionLiveProvider(t *testing.T) {
	t.Parallel()
	if os.Getenv("CAELIS_ASYNC_LIVE") != "1" {
		t.Skip("opt-in provider experiment")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is unavailable")
	}
	provider, err := providers.New(providers.Config{
		Provider: "deepseek", API: model.APIDeepSeek, Model: "deepseek-flash",
		BaseURL: "https://api.deepseek.com/anthropic", Auth: providers.AuthConfig{Type: model.AuthAPIKey, Token: key},
		Timeout: 45 * time.Second, MaxOutputTok: 1200, ContextWindowTokens: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newAsyncHarness(t, "")
	h.rt.compaction.Async.GenerationTimeout = 45 * time.Second
	semantic := `Synthetic audit case. User goal: prepare an ORBIT deployment draft. Preserve the exact code COBALT-731 and budget cap 17 units. Earlier permission to publish is revoked; do not publish or send anything. The question about target region remains unanswered: a host closure notice is not an answer or approval. A remote write with receipt action-42 has unknown outcome; reconcile only that original receipt, never retry under a new ID. Local verification completed, but the report has not yet been delivered. A later note asking for a concise answer does not replace these requirements.`
	if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: userTextEvent(semantic)}); err != nil {
		t.Fatal(err)
	}
	live := &asyncLiveModel{asyncProbeModel: h.llm, provider: provider, started: make(chan struct{}, 4)}
	h.source.set(agent.ModelRequestSnapshot{Revision: "live-epoch", Model: live, Tools: []tool.Tool{h.tool}, Reasoning: model.ReasoningConfig{Effort: "none"}})
	run := h.run(t)
	receiveSnapshotValue(t, h.llm.foreground)
	start := time.Now()
	h.llm.responses <- asyncToolResponse("first")
	next := receiveSnapshotValue(t, h.llm.foreground)
	foregroundWait := time.Since(start)
	receiveSnapshotValue(t, live.started)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	wait, cancel := context.WithTimeout(t.Context(), 55*time.Second)
	defer cancel()
	select {
	case <-next.scope.job.done:
	case <-wait.Done():
		t.Fatal(wait.Err())
	}
	if next.scope.job.err != nil || next.scope.job.receiptErr != nil {
		t.Fatalf("provider generation=%v receipt=%v", next.scope.job.err, next.scope.job.receiptErr)
	}
	generation := time.Since(start)
	adoptionStart := time.Now()
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground).request
	adoption := time.Since(adoptionStart)
	h.llm.responses <- asyncTextResponse("fixture foreground complete", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkpoint, _, ok := compact.LatestCompactEvent(h.load(t).Events)
	if !ok {
		t.Fatal("real candidate was not adopted")
	}
	var measurements []asyncMeasurement
	for _, event := range h.load(t).Events {
		if session.IsModelInvocationReceipt(event) && event.Invocation.Provider == "deepseek" {
			measurements = append(measurements, asyncMeasurement{Evidence: "real provider candidate; scripted foreground; synthetic 10k window", Mode: "async", ForegroundWaitMS: foregroundWait.Milliseconds(), GenerationMS: generation.Milliseconds(), AdoptionMS: adoption.Milliseconds(), Answer: session.EventText(checkpoint), Usage: usageFromAsyncReceipt(event)})
		}
	}
	question := `Return a JSON object with keys objective, code, budget, publish_allowed, region_answered, receipt_id, receipt_status, retry_new_id_allowed, report_delivered. Answer from the conversation evidence; do not invent approval or success.`
	for _, input := range []struct {
		name    string
		request *model.Request
	}{{"original", next.request}, {"compacted", after}} {
		request := model.CloneRequest(input.request)
		request.Tools = nil
		request.DisableTools = true
		request.Messages = append(request.Messages, model.NewTextMessage(model.RoleUser, question))
		measurement := asyncMeasurement{Evidence: "real provider semantic probe; synthetic evidence; manual comparison required", Mode: input.name}
		ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { measurement.Attempts = append(measurement.Attempts, in) })
		probeStart := time.Now()
		for event, err := range model.Generate(ctx, provider, request) {
			if err != nil {
				t.Fatal(err)
			}
			if event != nil && event.Response != nil && event.TurnComplete {
				measurement.Answer = event.Message.TextContent()
				measurement.Usage = event.Usage
			}
		}
		measurement.GenerationMS = time.Since(probeStart).Milliseconds()
		if strings.TrimSpace(measurement.Answer) == "" {
			t.Fatal("empty semantic probe")
		}
		measurements = append(measurements, measurement)
	}
	writeAsyncMeasurements(t, measurements)
}

func usageFromAsyncReceipt(event *session.Event) model.Usage {
	usage := event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["usage"]
	data, err := json.Marshal(usage)
	if err != nil {
		panic(fmt.Sprintf("fixture receipt encoding: %v", err))
	}
	var out model.Usage
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	out.Reported = true
	return out
}

// Hook the assembled chat request before the Runtime gate can load the Store.
type asyncAssemblyFactory struct {
	hook func(*model.Request)
}

func (f asyncAssemblyFactory) NewAgent(ctx context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	resolve := spec.ResolveModelRequest
	spec.ResolveModelRequest = func(ctx context.Context) (agent.ModelRequestSnapshot, error) {
		snapshot, err := resolve(ctx)
		if err == nil {
			snapshot.Model = &asyncAssemblyModel{LLM: snapshot.Model, hook: f.hook}
		}
		return snapshot, err
	}
	return (chat.Factory{}).NewAgent(ctx, spec)
}

type asyncAssemblyModel struct {
	model.LLM
	hook func(*model.Request)
}

func (*asyncAssemblyModel) TracksInvocations()                 {}
func (m *asyncAssemblyModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (m *asyncAssemblyModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.hook(req)
	return model.Generate(ctx, m.LLM, req)
}

func TestAsyncCompactionFailedInContextNeverGeneratesColdCheckpoint(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"empty", "length", "tools"} {
		t.Run(failure, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			run := h.run(t)
			next := h.startCandidate(t)
			response := asyncTextResponse("", 2000)
			switch failure {
			case "length":
				response = asyncTextResponse(asyncFixtureSummary, 2000)
				response.response.FinishReason = model.FinishReasonLength
				response.response.FinishReasonReported = true
			case "tools":
				response = asyncToolResponse("forbidden")
			}
			h.llm.summaries <- response
			receiveSnapshotValue(t, next.scope.job.done)
			if next.scope.job.err == nil {
				t.Fatal("failed generation accepted")
			}
			h.llm.responses <- asyncToolResponse("second")
			receiveSnapshotValue(t, h.secondTool)
			close(h.releaseSecond)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.llm.background:
				t.Fatal("strict generation issued another request")
			default:
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("failed generation wrote checkpoint")
			}
		})
	}
}

func TestAsyncCompactionRejectsUnboundOrChangedRequest(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unbound", "source_hash", "messages_hash", "open_call", "parallel_open_call"} {
		t.Run(scenario, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			calls := 0
			h.rt.agentFactory = asyncAssemblyFactory{hook: func(req *model.Request) {
				calls++
				if calls != 2 {
					return
				}
				if req.Source == nil {
					t.Error("second request has no assembly binding")
					return
				}
				switch scenario {
				case "unbound":
					req.Source = nil
				case "source_hash":
					req.Source.SourcePrefixHash = "mismatch"
				case "messages_hash":
					req.Source.MessagesHash = "mismatch"
				default:
					calls := []model.ToolCall{{ID: "open-a", Name: "work", Args: "{}"}}
					if scenario == "parallel_open_call" {
						calls = append(calls, model.ToolCall{ID: "open-b", Name: "work", Args: "{}"})
					}
					req.Messages = append(req.Messages, model.MessageFromToolCalls(model.RoleAssistant, calls, ""))
					req.Source.MessagesHash, _ = requestmeta.Fingerprint(req.Messages)
				}
			}}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncToolResponse("first")
			next := receiveSnapshotValue(t, h.llm.foreground)
			if next.scope.job != nil {
				t.Error("ineligible request launched a candidate")
			}
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.llm.background:
				t.Fatal("ineligible request generated a summary")
			default:
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("ineligible request wrote checkpoint")
			}
		})
	}
}
