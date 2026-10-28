package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
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
				compactor := newCodexStyleCompactor(CompactionConfig{MaxRetryAttempts: 3, RetryBaseDelay: time.Nanosecond, RetryMaxDelay: time.Nanosecond}, nil).(compact.ForceEngine)
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
	for _, path := range []string{"foreground", "manual"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
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
				MaxRetryAttempts: 3, RetryBaseDelay: time.Nanosecond, RetryMaxDelay: time.Nanosecond,
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
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
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
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: h.root})
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
