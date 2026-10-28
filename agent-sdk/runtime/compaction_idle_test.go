package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type idleProbeModel struct {
	*asyncProbeModel
	name string
}

func (m *idleProbeModel) Name() string {
	if m.name != "" {
		return m.name
	}
	return "idle-fixture"
}
func (*idleProbeModel) ContextWindowTokens() int { return 40000 }

func completedIdleSource(t *testing.T) (*asyncHarness, *model.Request) {
	t.Helper()
	h := newAsyncHarness(t, "")
	h.rt.compaction.Async.Enabled = false
	h.rt.compaction.Idle.Enabled = true
	m := &idleProbeModel{asyncProbeModel: h.llm}
	h.source.set(agent.ModelRequestSnapshot{Revision: "epoch-1", Model: m})
	history := model.NewTextMessage(model.RoleAssistant, strings.Repeat("synthetic durable context fact; ", 2500))
	if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: &session.Event{Type: session.EventTypeAssistant, Message: &history, Scope: &session.EventScope{TurnID: "old-turn"}}}); err != nil {
		t.Fatal(err)
	}
	run := h.run(t)
	actual := receiveSnapshotValue(t, h.llm.foreground).request
	h.llm.responses <- asyncTextResponse("final durable answer", 24000)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.rt.DiscardIdleCompactionSources)
	return h, actual
}

func prepareIdleJob(t *testing.T, h *asyncHarness) *IdleCompactionJob {
	t.Helper()
	job, err := h.rt.PrepareIdleCompaction(t.Context(), h.active.SessionRef)
	if err != nil || job == nil {
		t.Fatalf("prepare idle=%v job=%v snapshot=%+v", err, job, h.rt.compactionRequests[compactionSessionKey(h.active.SessionRef)])
	}
	t.Cleanup(job.Close)
	return job
}
func releaseIdleJob(t *testing.T, h *asyncHarness, job *IdleCompactionJob) session.IdleCompactionWindow {
	t.Helper()
	window, err := h.store.ReleaseSessionFenceForIdleCompaction(t.Context(), session.NewIdleCompactionRelease(h.fence, job.Source("fixture-host", "activation")))
	if err != nil {
		t.Fatal(err)
	}
	return window
}

func TestIdleCompactionCompletedSourceAndFileReplay(t *testing.T) {
	h, actual := completedIdleSource(t)
	var releases atomic.Int64
	snapshot := h.source.current
	snapshot.Release = func() { releases.Add(1) }
	h.source.set(snapshot)
	resolved := h.source.resolved.Load()
	job := prepareIdleJob(t, h)
	if h.source.resolved.Load() != resolved+1 || releases.Load() != 0 {
		t.Fatal("idle did not acquire an independent live snapshot")
	}

	window := releaseIdleJob(t, h, job)
	if job.candidate.coverage.SourceThroughSeq <= actual.Source.SourceThroughSeq {
		t.Fatal("completed source did not advance beyond immutable dispatch S")
	}
	if !reflect.DeepEqual(job.candidate.frozen.Source, actual.Source) {
		t.Fatal("dispatch proof was rewritten")
	}
	done := make(chan error, 1)
	go func() {
		adopted, err := job.Run(context.Background(), window)
		if err == nil && !adopted {
			err = errors.New("candidate not adopted")
		}
		done <- err
	}()
	request := receiveSnapshotValue(t, h.llm.background)
	want := model.CloneRequest(actual)
	want.Messages = append(want.Messages, model.NewTextMessage(model.RoleAssistant, "final durable answer"), model.NewTextMessage(model.RoleUser, inContextCompactionPrompt))
	if !reflect.DeepEqual(request, want) {
		t.Fatal("idle request did not preserve exact sent prefix and final answer")
	}
	response := asyncTextResponse(asyncFixtureSummary, 120)
	response.response.Usage.CachedInputTokens = 24000
	response.response.Usage.CachedInputTokensReported = true
	response.response.Usage.CacheWriteTokens = 30
	response.response.Usage.CacheWriteTokensReported = true
	h.llm.summaries <- response
	if err := receiveSnapshotValue(t, done); err != nil {
		t.Fatal(err)
	}
	h.source.mu.Lock()
	admission := h.source.admissions[len(h.source.admissions)-1]
	h.source.mu.Unlock()
	if admission.TurnID != "" || admission.JobID != job.candidate.id {
		t.Fatalf("idle reused completed Turn admission: %+v", admission)
	}
	job.Close()
	if releases.Load() != 1 {
		t.Fatalf("independent snapshot release count=%d", releases.Load())
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: h.root})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: h.active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, data, ok := compact.LatestCompactEvent(loaded.Events)
	if !ok || data.SummarizedThroughSeq >= checkpoint.Seq {
		t.Fatalf("invalid checkpoint=%+v data=%+v", checkpoint, data)
	}
	var receipts int
	for _, event := range loaded.Events {
		if session.IsModelInvocationReceipt(event) && event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "compaction" {
			receipts++
			if session.IsMainInvocationVisibleEvent(event) {
				t.Fatal("accounting leaked into prompt")
			}
		}
	}
	if receipts != 1 {
		t.Fatalf("receipts=%d", receipts)
	}
	projected := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(projected) != 1 || !strings.Contains(session.EventText(projected[0]), "User objective:") || strings.Contains(session.EventText(projected[0]), "synthetic durable context fact") {
		t.Fatalf("replay kept covered source: %d", len(projected))
	}
}

func TestIdleCompactionSourceAndCacheEligibility(t *testing.T) {
	for _, scenario := range []string{"queued_tail", "failed_final", "elapsed", "backwards_clock", "codex_never_expires"} {
		t.Run(scenario, func(t *testing.T) {
			h, _ := completedIdleSource(t)
			key := compactionSessionKey(h.active.SessionRef)
			snapshot := h.rt.compactionRequests[key]
			switch scenario {
			case "queued_tail":
				if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: userTextEvent("a later queued input")}); err != nil {
					t.Fatal(err)
				}
			case "failed_final":
				snapshot.completed = false
				h.rt.compactionRequests[key] = snapshot
			case "elapsed":
				h.rt.clock = func() time.Time { return snapshot.startedAt.Add(271 * time.Second) }
			case "backwards_clock":
				h.rt.clock = func() time.Time { return snapshot.startedAt.Add(-time.Second) }
			case "codex_never_expires":
				snapshot.model = &idleProbeModel{asyncProbeModel: h.llm, name: "magpie-codex"}
				h.rt.compactionRequests[key] = snapshot
			}
			job, err := h.rt.PrepareIdleCompaction(t.Context(), h.active.SessionRef)
			if err != nil || job != nil {
				t.Fatalf("ineligible source accepted: %v %v", job, err)
			}
		})
	}
}

func TestIdleCompactionForegroundCancelsCheckpointButKeepsReceipt(t *testing.T) {
	h, _ := completedIdleSource(t)
	h.llm.ignoreStop = true
	job := prepareIdleJob(t, h)
	window := releaseIdleJob(t, h, job)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := job.Run(ctx, window); done <- err }()
	receiveSnapshotValue(t, h.llm.background)
	foreground, err := h.store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: h.active.SessionRef, OwnerID: "fixture-host"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("provider was not drained: %v", err)
	default:
	}
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 150)
	if err := receiveSnapshotValue(t, done); err == nil {
		t.Fatal("cancelled job reported adoption")
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("cancelled idle job wrote checkpoint")
	}
	if err := job.FlushAccounting(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := h.store.SessionFence(t.Context(), h.active.SessionRef)
	if err != nil || current.FenceID != foreground.FenceID {
		t.Fatal("accounting disturbed foreground fence")
	}
}

type idleGrantRaceStore struct {
	*asyncFaultStore
	foreground session.SessionFence
}

func (s *idleGrantRaceStore) IssueIdleCompactionGrant(ctx context.Context, req session.IssueIdleCompactionGrantRequest) (session.IdleCompactionGrant, error) {
	fence, err := s.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: req.Window.SessionRef(), OwnerID: "foreground"})
	if err != nil {
		return session.IdleCompactionGrant{}, err
	}
	s.foreground = fence
	return s.asyncFaultStore.IssueIdleCompactionGrant(ctx, req)
}
func TestIdleReadyCandidateAdoptsUnderNewForegroundFenceAndKeepsTail(t *testing.T) {
	testIdleReadyCandidate(t, false)
}
func TestIdleReadyCandidateExpiresBeforeForegroundAdoption(t *testing.T) {
	testIdleReadyCandidate(t, true)
}
func testIdleReadyCandidate(t *testing.T, expired bool) {
	t.Helper()
	h, _ := completedIdleSource(t)
	job := prepareIdleJob(t, h)
	window := releaseIdleJob(t, h, job)
	store := &idleGrantRaceStore{asyncFaultStore: h.store}
	h.rt.sessions = store
	done := make(chan error, 1)
	go func() {
		adopted, err := job.Run(t.Context(), window)
		if adopted {
			err = errors.New("revoked idle grant committed")
		}
		done <- err
	}()
	receiveSnapshotValue(t, h.llm.background)
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 120)
	if err := receiveSnapshotValue(t, done); !errors.Is(err, session.ErrIdleCompactionRevoked) {
		t.Fatalf("revocation=%v", err)
	}
	job.Close()
	if len(h.rt.idleCandidates) != 1 {
		t.Fatal("ready content was discarded with old authority")
	}
	if expired {
		now := time.Now().Add(121 * time.Second)
		h.rt.clock = func() time.Time { return now }
	}
	h.fence = store.foreground
	h.ctx = session.ContextWithRuntimeFence(t.Context(), h.fence)
	run := h.run(t)
	request := receiveSnapshotValue(t, h.llm.foreground).request
	text := ""
	for _, message := range request.Messages {
		text += message.TextContent() + "\n"
	}
	if expired {
		if strings.Contains(text, "User objective:") || !strings.Contains(text, "synthetic durable context fact") || strings.Count(text, "continue fixture") != 2 {
			t.Fatalf("expired candidate changed context: %s", text)
		}
	} else if !strings.Contains(text, "User objective:") || strings.Contains(text, "synthetic durable context fact") || strings.Count(text, "continue fixture") != 1 {
		t.Fatalf("new request did not contain checkpoint plus exactly the new tail: %s", text)
	}
	h.llm.responses <- asyncTextResponse("resumed foreground", 100)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded := h.load(t)
	_, data, ok := compact.LatestCompactEvent(loaded.Events)
	if expired {
		if ok {
			t.Fatal("expired candidate persisted")
		}
		return
	}
	if !ok || data.SummarizedThroughSeq != job.candidate.coverage.SummarizedThroughSeq {
		t.Fatal("foreground expanded the summary's original coverage")
	}
}

func TestIdleCompactionTerminalFailuresPreserveContextAndAccountOnce(t *testing.T) {
	for _, reason := range []model.FinishReason{model.FinishReasonLength, model.FinishReasonContentFilter, model.FinishReasonRefusal, model.FinishReasonUnknown} {
		t.Run(string(reason), func(t *testing.T) {
			h, _ := completedIdleSource(t)
			job := prepareIdleJob(t, h)
			window := releaseIdleJob(t, h, job)
			before := mainInvocationEvents(h.load(t).Events)
			done := make(chan error, 1)
			go func() { _, err := job.Run(t.Context(), window); done <- err }()
			receiveSnapshotValue(t, h.llm.background)
			response := asyncTextResponse(asyncFixtureSummary, 120)
			response.response.FinishReason = reason
			response.response.FinishReasonReported = true
			h.llm.summaries <- response
			if err := receiveSnapshotValue(t, done); err == nil {
				t.Fatal("incomplete summary accepted")
			}
			if len(h.llm.background) != 0 {
				t.Fatal("terminal failure retried or fell back")
			}
			loaded, err := h.store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: h.active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, mainInvocationEvents(loaded.Events)) {
				t.Fatal("failed candidate changed context")
			}
			receipts := 0
			for _, event := range loaded.Events {
				if session.IsModelInvocationReceipt(event) && event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "compaction" {
					receipts++
					if event.Scope != nil && event.Scope.TurnID != "" {
						t.Fatal("idle receipt billed a completed Turn")
					}
				}
			}
			if receipts != 1 {
				t.Fatalf("receipts=%d", receipts)
			}
		})
	}
}

func TestIdleCommitDoesNotQueueBehindForegroundWriter(t *testing.T) {
	h, _ := completedIdleSource(t)
	job := prepareIdleJob(t, h)
	window := releaseIdleJob(t, h, job)
	release, err := h.rt.acquireSessionWrite(t.Context(), h.active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	first := sessionWriteTailForTest(&h.rt.sessionWrites, h.active.SessionRef)
	done := make(chan error, 1)
	go func() { _, err := job.Run(t.Context(), window); done <- err }()
	receiveSnapshotValue(t, h.llm.background)
	h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 120)
	if err := receiveSnapshotValue(t, done); !errors.Is(err, errSessionWriteBusy) {
		t.Fatalf("idle did not yield the busy writer: %v", err)
	}
	if sessionWriteTailForTest(&h.rt.sessionWrites, h.active.SessionRef) != first {
		t.Fatal("idle joined the foreground queue")
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("idle committed behind foreground")
	}
	if len(h.rt.idleCandidates) != 1 {
		t.Fatal("complete candidate was discarded before a foreground boundary")
	}
}
