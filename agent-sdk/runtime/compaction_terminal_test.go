package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestmeta"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"image"
	"image/png"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type compactionTerminalModel struct {
	responses []*model.Response
	requests  []*model.Request
}

func (*compactionTerminalModel) Name() string         { return "terminal-fixture" }
func (*compactionTerminalModel) ProviderName() string { return "fixture" }
func (m *compactionTerminalModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		index := min(len(m.requests), len(m.responses)-1)
		m.requests = append(m.requests, model.CloneRequest(req))
		yield(model.StreamEventFromResponse(m.responses[index]), nil)
	}
}

func compactionTerminalResponse(reason model.FinishReason) *model.Response {
	return &model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, asyncFixtureSummary),
		TurnComplete: true, FinishReason: reason, FinishReasonReported: true,
		Usage: model.Usage{Reported: true, PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
	}
}

func TestCollectCompactionResponseTerminalReasons(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		reason   model.FinishReason
		reported bool
		status   model.ResponseStatus
		wantErr  error
	}{
		{"stop", model.FinishReasonStop, true, model.ResponseStatusCompleted, nil},
		{"length", model.FinishReasonLength, true, "", errCompactionTruncated},
		{"content_filter", model.FinishReasonContentFilter, true, "", errCompactionFiltered},
		{"refusal", model.FinishReasonRefusal, true, "", errCompactionRefused},
		{"tool_calls", model.FinishReasonToolCalls, true, "", errCompactionToolRequest},
		{"reported_unknown", model.FinishReasonUnknown, true, "", errCompactionIncomplete},
		{"future_reason", model.FinishReason("future_reason"), true, "", errCompactionIncomplete},
		{"unreported", model.FinishReasonUnknown, false, "", nil},
		{"legacy_length", model.FinishReasonLength, false, "", errCompactionTruncated},
		{"failed", model.FinishReasonStop, true, model.ResponseStatusFailed, errCompactionIncomplete},
		{"cancelled", model.FinishReasonStop, true, model.ResponseStatusCancelled, errCompactionIncomplete},
		{"in_progress", model.FinishReasonStop, true, model.ResponseStatusInProgress, errCompactionIncomplete},
		{"future_status", model.FinishReasonStop, true, model.ResponseStatus("future_status"), errCompactionIncomplete},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := compactionTerminalResponse(tt.reason)
			response.FinishReasonReported, response.Status = tt.reported, tt.status
			llm := &compactionTerminalModel{responses: []*model.Response{response}}
			got, err := collectCompactionResponse(t.Context(), llm, &model.Request{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if (got != nil) != (tt.wantErr == nil) {
				t.Fatalf("response = %#v, error = %v", got, err)
			}
		})
	}
}

func TestCollectCompactionResponseUnreportedLogs(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	response := compactionTerminalResponse(model.FinishReasonUnknown)
	response.FinishReasonReported = false
	response.Provider, response.Model = "fixture", "terminal-fixture"
	llm := &compactionTerminalModel{responses: []*model.Response{response}}
	if _, err := collectCompactionResponse(t.Context(), llm, &model.Request{}); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["component"] != "context_compaction" || entry["finish_reason_reported"] != false ||
		entry["provider"] != response.Provider || entry["model"] != response.Model {
		t.Fatalf("missing structured terminal evidence: %s", &logs)
	}
	if strings.Contains(logs.String(), asyncFixtureSummary) {
		t.Fatal("diagnostic exposed the response body")
	}
}

func TestCompactionTerminalErrorsDoNotRetryOrFallback(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		reason model.FinishReason
		want   error
	}{
		{"length", model.FinishReasonLength, errCompactionTruncated},
		{"content_filter", model.FinishReasonContentFilter, errCompactionFiltered},
		{"refusal", model.FinishReasonRefusal, errCompactionRefused},
		{"reported_unknown", model.FinishReasonUnknown, errCompactionIncomplete},
	} {
		for _, path := range []string{"independent", "in_context", "salvage"} {
			t.Run(tt.name+"/"+path, func(t *testing.T) {
				llm := &compactionTerminalModel{responses: []*model.Response{compactionTerminalResponse(tt.reason)}}
				req := compact.Request{Model: llm, Events: []*session.Event{userTextEvent("preserve all original evidence"), assistantEvent("evidence is still unverified")}}
				wantAttempts := 1
				if path == "in_context" {
					req.InContextRequest = &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "original context")}}
				}
				if path == "salvage" {
					short := compactionTerminalResponse(model.FinishReasonStop)
					short.Message = model.NewTextMessage(model.RoleAssistant, "# Heading")
					llm.responses = append([]*model.Response{short}, llm.responses...)
					wantAttempts = 2
				}
				compactor := newCodexStyleCompactor(CompactionConfig{}, nil).(compact.ForceEngine)
				result, err := compactor.Force(t.Context(), req, "manual")
				if !errors.Is(err, tt.want) || result.Compacted || result.CompactEvent != nil {
					t.Errorf("compacted=%v has_checkpoint=%v error=%v, want %v", result.Compacted, result.CompactEvent != nil, err, tt.want)
				}
				if len(llm.requests) != wantAttempts {
					t.Errorf("attempts=%d, want %d without retry, fallback or segmentation", len(llm.requests), wantAttempts)
				}
			})
		}
	}
}

func TestCompactionRejectsShortRawMarkdown(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"independent", "in_context", "salvage"} {
		t.Run(path, func(t *testing.T) {
			response := compactionTerminalResponse(model.FinishReasonStop)
			response.Message = model.NewTextMessage(model.RoleAssistant, "# Heading")
			llm := &compactionTerminalModel{responses: []*model.Response{response}}
			var text string
			var err error
			switch path {
			case "independent":
				text, err = modelCompactMarkdown(t.Context(), llm, "", []*session.Event{userTextEvent("retain the original evidence")}, "")
			case "in_context":
				text, err = modelCompactMarkdownInContext(t.Context(), llm, &model.Request{})
			case "salvage":
				text, err = salvageCompactMarkdown(t.Context(), llm, "original evidence", "", "")
			}
			if err == nil || text != "" {
				t.Errorf("short body accepted: text=%q error=%v", text, err)
			}
			if path != "in_context" {
				for _, req := range llm.requests {
					if !req.DisableTools {
						t.Error("summary generation must disable tools, including salvage")
					}
				}
			}
		})
	}
}

func TestRuntimeCompactionTruncatedPreservesContextAndReceipts(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"foreground", "manual"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			store := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "terminal", UserID: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			appendTestEvent(t, store, active.SessionRef, userTextEvent(strings.Repeat("retain original evidence; ", 60)))
			appendTestEvent(t, store, active.SessionRef, assistantEvent("unverified result must remain unknown"))
			before, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			llm := &compactionTerminalModel{responses: []*model.Response{compactionTerminalResponse(model.FinishReasonLength)}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{
				Enabled: true, DefaultContextWindowTokens: 128, ReserveOutputTokens: 16, SafetyMarginTokens: 8,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if path == "foreground" {
				run, runErr := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{Model: llm}})
				if runErr != nil {
					t.Fatal(runErr)
				}
				_, err = drainRunnerEvents(t, run.Handle)
			} else {
				_, err = rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: llm, Trigger: "manual"})
			}
			if !errors.Is(err, errCompactionTruncated) {
				t.Errorf("error=%v, want truncated", err)
			}
			reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
			after, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			assertCompactionTerminalReceipts(t, after.Events, len(llm.requests))
			if len(llm.requests) != 1 {
				t.Errorf("attempts=%d, want one", len(llm.requests))
			}
			prompt := compact.PromptEventsFromLatestCompact(mainInvocationEvents(after.Events))
			if len(prompt) < len(before.Events) || !reflect.DeepEqual(prompt[:len(before.Events)], before.Events) {
				t.Fatal("failure changed the original model context")
			}
		})
	}
}

func assertCompactionTerminalReceipts(t *testing.T, events []*session.Event, attempts int) {
	t.Helper()
	receipts := 0
	for _, event := range events {
		if compact.IsCompactEvent(event) {
			t.Error("unusable summary was written as a checkpoint")
		}
		if session.IsModelInvocationReceipt(event) {
			receipts++
		}
	}
	if receipts != attempts {
		t.Errorf("receipts=%d, attempts=%d", receipts, attempts)
	}
}

func TestAsyncCompactionTruncatedNeverCommitsAtNextRequest(t *testing.T) {
	t.Parallel()
	h := newAsyncHarness(t, "")
	run := h.run(t)
	next := h.startCandidate(t)
	h.llm.summaries <- snapshotResponse{response: compactionTerminalResponse(model.FinishReasonLength)}
	receiveSnapshotValue(t, next.scope.job.done)
	// Cross the actual adoption boundary before checking the durable Store.
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground).request
	if !strings.Contains(asyncMessagesText(after.Messages), "synthetic history;") {
		t.Error("original context was replaced by the truncated candidate")
	}
	assertCompactionTerminalReceipts(t, h.load(t).Events, 3) // two foreground attempts and one candidate
	if !errors.Is(next.scope.job.err, errCompactionTruncated) || next.scope.job.result.Compacted {
		t.Errorf("candidate error=%v compacted=%v", next.scope.job.err, next.scope.job.result.Compacted)
	}
	h.llm.responses <- asyncTextResponse("fixture complete", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.source.mu.Lock()
	attempts := len(h.source.admissions)
	h.source.mu.Unlock()
	if attempts != 4 {
		t.Errorf("attempts=%d, want three foreground attempts and one candidate", attempts)
	}
	assertCompactionTerminalReceipts(t, h.load(t).Events, attempts)
	if len(h.llm.background) != 0 {
		t.Fatal("terminal rejection retried or segmented the candidate")
	}
	// Rebuild the complete model request from reopened files, not only UI events.
	reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: h.root})
	resumed, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := resumed.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "replay probe", AgentSpec: agent.AgentSpec{ResolveModelRequest: h.source.resolve}})
	if err != nil {
		t.Fatal(err)
	}
	got := receiveSnapshotValue(t, h.llm.foreground).request
	want := model.CloneRequest(after)
	// Replay here has speculation disabled; source bindings are request-local.
	want.Source = nil
	want.Messages = append(want.Messages, model.NewTextMessage(model.RoleAssistant, "fixture complete"), model.NewTextMessage(model.RoleUser, "replay probe"))
	if !reflect.DeepEqual(got, want) {
		t.Error("reopened Store changed the complete model request")
	}
	h.llm.responses <- asyncTextResponse("replay complete", 600)
	if err := probe.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type compactionTerminalTransport func(*http.Request) (*http.Response, error)

func (f compactionTerminalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCompactionProviderTerminalNeverRetriesOrFallsBack(t *testing.T) {
	t.Parallel()
	for _, api := range []model.APIType{model.APIOllama, model.APIGemini} {
		for _, stream := range []bool{false, true} {
			for _, reason := range []string{"length", "unknown", "filtered", "unspecified"} {
				if api == model.APIOllama && (reason == "filtered" || reason == "unspecified") {
					continue
				}
				t.Run(fmt.Sprintf("%s/stream=%v/%s", api, stream, reason), func(t *testing.T) {
					calls, admissions, receipts := 0, 0, 0
					transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
						calls++
						var body map[string]any
						if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
							return nil, err
						}
						wire := ""
						if api == model.APIOllama {
							wire = fmt.Sprintf(`{"model":"fixture","message":{"role":"assistant","content":"partial summary with enough text to look useful"},"done":true,"done_reason":%q,"prompt_eval_count":11,"eval_count":7}`, reason)
						} else {
							rawReason := map[string]string{"length": "MAX_TOKENS", "unknown": "FUTURE_REASON", "filtered": "SAFETY", "unspecified": "FINISH_REASON_UNSPECIFIED"}[reason]
							wire = fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial summary with enough text to look useful"}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`, rawReason)
							if strings.Contains(req.URL.Path, "streamGenerateContent") {
								wire = "data: " + wire + "\n\n"
							}
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(wire)), Request: req}, nil
					})
					llm, err := providers.New(providers.Config{API: api, Provider: string(api), Model: "fixture", BaseURL: "http://fixture.invalid", Auth: providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}, HTTPClient: &http.Client{Transport: transport}, Retry: model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}})
					if err != nil {
						t.Fatal(err)
					}
					ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) {
						receipts++
						if in.Usage.TotalTokens != 18 {
							t.Errorf("usage=%+v", in.Usage)
						}
					})
					ctx = model.WithInvocationAdmission(ctx, func(context.Context, *model.Request) error {
						admissions++
						return nil
					})
					c := newCodexStyleCompactor(CompactionConfig{Enabled: true}, nil).(compact.ForceEngine)
					result, err := c.Force(ctx, compact.Request{Model: llm, Events: []*session.Event{userTextEvent("retain original source")}, InContextRequest: &model.Request{Stream: stream, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source")}}}, "manual")
					if !isCompactionTerminalError(err) || result.Compacted || calls != 1 || admissions != 1 || receipts != 1 {
						t.Fatalf("terminal=%v compacted=%v HTTP=%d admissions=%d receipts=%d", err, result.Compacted, calls, admissions, receipts)
					}
				})
			}
		}
	}
}

type cancelledCompactionModel struct {
	calls   int
	cancel  context.CancelFunc
	yielded int
}

func (*cancelledCompactionModel) Name() string { return "cancelled-compaction" }
func (m *cancelledCompactionModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.cancel != nil {
			m.cancel()
		}
		for range 3 {
			m.yielded++
			if !yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "A sufficiently long checkpoint from a cancelled request."), TurnComplete: true}), nil) {
				return
			}
		}
	}
}
func TestCompactionRejectsProviderSuccessAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_request", false: "during_stream"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			llm := &cancelledCompactionModel{}
			if before {
				cancel()
			} else {
				llm.cancel = cancel
			}
			text, err := modelCompactMarkdownInContext(ctx, llm, &model.Request{})
			if !errors.Is(err, context.Canceled) || text != "" {
				t.Errorf("checkpoint = %q, error = %v, want empty/cancelled", text, err)
			}
			if before && llm.calls != 0 {
				t.Errorf("provider calls = %d, want zero", llm.calls)
			}
			if !before && llm.yielded != 1 {
				t.Errorf("stream consumed %d events, want one", llm.yielded)
			}
		})
	}
}

func TestAsyncCompactionGenerationBeyond30Seconds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newAsyncHarness(t, "")
		run := h.run(t)
		next := h.startCandidate(t)
		time.Sleep(40 * time.Second)
		select {
		case <-next.scope.job.done:
			t.Fatalf("candidate prematurely ended: %v", next.scope.job.err)
		default:
		}
		h.completeCandidate(t, next.scope)
		h.llm.responses <- asyncTextResponse("done", 500)
		if err := run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

type recoveringSummaryModel struct{ calls int }

func (*recoveringSummaryModel) Name() string { return "summary-fixture" }
func (m *recoveringSummaryModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if m.calls <= 30 {
			yield(nil, errorcode.New(errorcode.Unavailable, "offline"))
			return
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) != 600*time.Second {
			panic("summary attempt did not receive fresh 600-second budget")
		}
		timer := time.NewTimer(40 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, asyncFixtureSummary), TurnComplete: true}), nil)
	}
}

func TestCompactionForegroundOfflineWaitingHasNoTotalDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		r := &Runtime{}
		inner := &recoveringSummaryModel{}
		llm := model.WithRetry(inner, model.RetryConfig{})
		started := time.Now()
		c := newCodexStyleCompactor(CompactionConfig{Enabled: true}, nil).(compact.ForceEngine)
		result, err := c.Force(r.compactionRequestContext(t.Context()), compact.Request{Model: llm, Events: []*session.Event{userTextEvent("retain original source")}}, "fixture")
		if err != nil || !result.Compacted || inner.calls != 31 || time.Since(started) <= 600*time.Second {
			t.Fatalf("calls=%d elapsed=%s compacted=%t err=%v", inner.calls, time.Since(started), result.Compacted, err)
		}
	})
}

func appendTestEventWithGuard(t *testing.T, h *asyncHarness, event *session.Event) {
	t.Helper()
	if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: event}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionPreparedImageSource(t *testing.T) {
	t.Parallel()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewGray(image.Rect(0, 0, 2100, 1100))); err != nil {
		t.Fatal(err)
	}
	message := model.NewMessage(model.RoleUser, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data.Bytes())}, "image/png", "screenshot.png"))
	event := &session.Event{Type: session.EventTypeUser, Message: &message}
	for _, mode := range []string{"async", "idle"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "idle" {
				h, actual := completedIdleSource(t, event)
				job := prepareIdleJob(t, h)
				if !reflect.DeepEqual(job.candidate.frozen.Messages[:len(actual.Messages)], actual.Messages) {
					t.Fatal("idle lost prepared prefix")
				}
				return
			}
			h := newAsyncHarness(t, "")
			h.llm.imageInput = true
			appendTestEventWithGuard(t, h, event)
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncToolResponse("first")
			next := receiveSnapshotValue(t, h.llm.foreground)
			hash, err := requestmeta.Fingerprint(next.request.Messages)
			if err != nil || next.request.Source == nil || next.request.Source.MessagesHash != hash || next.scope == nil || next.scope.job == nil {
				t.Fatal("image normalization invalidated bound dispatch source")
			}
			background := receiveSnapshotValue(t, h.llm.background)
			want := model.CloneRequest(next.request)
			want.Messages = append(want.Messages, model.NewTextMessage(model.RoleUser, inContextCompactionPrompt))
			if !reflect.DeepEqual(background, want) {
				t.Fatal("summary changed prepared foreground prefix")
			}
			h.completeCandidate(t, next.scope)
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
