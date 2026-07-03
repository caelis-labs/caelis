package local

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/impl/tool/builtin/web"
	"github.com/caelis-labs/caelis/ports/compact"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/ports/tool"
)

func TestAutoCompactModelWrapperPreservesWebSearcher(t *testing.T) {
	t.Parallel()

	runtime := forceGateRuntimeForWrapTest()
	wrapped := runtime.wrapModelForAutoCompaction(session.SessionRef{SessionID: "sess-web-search"}, searchCapableGateModel{})
	if _, ok := wrapped.(model.WebSearcher); !ok {
		t.Fatalf("wrapped model = %T, want WebSearcher capability", wrapped)
	}

	result, err := web.NewSearch().Call(context.Background(), tool.Call{
		Input:        json.RawMessage(`{"query":"latest release","max_results":1}`),
		RuntimeModel: wrapped,
	})
	if err != nil {
		t.Fatalf("web_search Call() error = %v", err)
	}
	payload := webSearchResultPayloadForTest(t, result)
	if got := payload["status"]; got != "completed" {
		t.Fatalf("status = %#v, want completed: %#v", got, payload)
	}
	if got := payload["provider"]; got != "test-search" {
		t.Fatalf("provider = %#v, want test-search", got)
	}
}

func TestAutoCompactModelWrapperDoesNotExposeWebSearcherForUnsupportedModel(t *testing.T) {
	t.Parallel()

	runtime := forceGateRuntimeForWrapTest()
	wrapped := runtime.wrapModelForAutoCompaction(session.SessionRef{SessionID: "sess-no-web-search"}, staticModel{text: "ok"})
	if _, ok := wrapped.(model.WebSearcher); ok {
		t.Fatalf("wrapped model = %T unexpectedly exposes WebSearcher", wrapped)
	}
}

func TestAutoCompactModelWrapperSkipsNonForceCompactor(t *testing.T) {
	t.Parallel()

	base := staticModel{text: "ok"}
	runtime := &Runtime{
		compaction: CompactionConfig{Enabled: true},
		compactor:  nonForceGateCompactor{},
	}
	wrapped := runtime.wrapModelForAutoCompaction(session.SessionRef{SessionID: "sess-non-force-gate"}, base)
	if wrapped != base {
		t.Fatalf("wrapped model = %T, want original model when compactor cannot force", wrapped)
	}
}

func TestAutoCompactDecisionBeforeModelRequestUsesRequestEstimate(t *testing.T) {
	t.Parallel()

	runtime, activeSession := newGateDecisionRuntimeForTest(t, CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.50,
		ForceWatermarkRatio:        0.80,
		DefaultContextWindowTokens: 256,
		ReserveOutputTokens:        8,
		SafetyMarginTokens:         4,
	})
	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, strings.Repeat("request-estimate ", 40)),
		},
	}

	decision, err := runtime.autoCompactDecisionBeforeModelRequest(context.Background(), activeSession.SessionRef, staticModel{text: "ok"}, req)
	if err != nil {
		t.Fatalf("autoCompactDecisionBeforeModelRequest() error = %v", err)
	}
	if decision.Reason != "model_request_context_watermark" {
		t.Fatalf("reason = %q, want model_request_context_watermark", decision.Reason)
	}
	if decision.Kind != compactionRecoveryKindWatermark {
		t.Fatalf("kind = %q, want %q", decision.Kind, compactionRecoveryKindWatermark)
	}
	if decision.RequestTokens == 0 || decision.Usage.TotalTokens < decision.RequestTokens {
		t.Fatalf("decision usage/request = %+v/%d, want request estimate included", decision.Usage, decision.RequestTokens)
	}
	if len(decision.Events) == 0 {
		t.Fatal("expected decision to retain event snapshot for recovery")
	}
}

func TestAutoCompactDecisionBeforeModelRequestSkipsFreshUserWithoutCurrentTurnProgress(t *testing.T) {
	t.Parallel()

	runtime, activeSession := newGateDecisionRuntimeForTest(t, CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.10,
		ForceWatermarkRatio:        0.20,
		DefaultContextWindowTokens: 256,
		ReserveOutputTokens:        8,
		SafetyMarginTokens:         4,
	})
	appendTestEvent(t, runtime.sessions, activeSession.SessionRef, userTextEvent("fresh user prompt"))

	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, strings.Repeat("would-cross-watermark ", 80)),
		},
	}
	decision, err := runtime.autoCompactDecisionBeforeModelRequest(context.Background(), activeSession.SessionRef, staticModel{text: "ok"}, req)
	if err != nil {
		t.Fatalf("autoCompactDecisionBeforeModelRequest() error = %v", err)
	}
	if decision.Reason != "" {
		t.Fatalf("reason = %q, want no compaction before current-turn model progress", decision.Reason)
	}
}

func TestAutoCompactDecisionBeforeModelRequestAllowsProgressAfterLatestUser(t *testing.T) {
	t.Parallel()

	runtime, activeSession := newGateDecisionRuntimeForTest(t, CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.10,
		ForceWatermarkRatio:        0.20,
		DefaultContextWindowTokens: 256,
		ReserveOutputTokens:        8,
		SafetyMarginTokens:         4,
	})
	appendTestEvent(t, runtime.sessions, activeSession.SessionRef, userTextEvent("fresh user prompt"))
	appendTestEvent(t, runtime.sessions, activeSession.SessionRef, assistantEvent("current turn progress"))

	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, strings.Repeat("cross-watermark ", 80)),
		},
	}
	decision, err := runtime.autoCompactDecisionBeforeModelRequest(context.Background(), activeSession.SessionRef, staticModel{text: "ok"}, req)
	if err != nil {
		t.Fatalf("autoCompactDecisionBeforeModelRequest() error = %v", err)
	}
	if decision.Reason == "" {
		t.Fatal("expected compaction decision after current-turn model progress")
	}
	if decision.Kind != compactionRecoveryKindWatermark {
		t.Fatalf("kind = %q, want %q", decision.Kind, compactionRecoveryKindWatermark)
	}
}

func TestAutoCompactDecisionAfterRetryExhaustedUsesEmergencyWatermark(t *testing.T) {
	t.Parallel()

	runtime, activeSession := newGateDecisionRuntimeForTest(t, CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             10.0,
		ForceWatermarkRatio:        10.0,
		DefaultContextWindowTokens: 256,
		ReserveOutputTokens:        8,
		SafetyMarginTokens:         4,
	})
	assistant := assistantEvent("provider high-water baseline")
	assistant.Meta = map[string]any{
		"prompt_tokens":     245,
		"completion_tokens": 1,
		"total_tokens":      246,
	}
	appendTestEvent(t, runtime.sessions, activeSession.SessionRef, userTextEvent("high water user"))
	appendTestEvent(t, runtime.sessions, activeSession.SessionRef, assistant)

	decision, compact, err := runtime.autoCompactDecisionAfterModelRequestFailure(
		context.Background(),
		activeSession.SessionRef,
		staticModel{text: "ok"},
		&model.Request{},
		&model.RetryExhaustedError{MaxRetries: 5},
	)
	if err != nil {
		t.Fatalf("autoCompactDecisionAfterModelRequestFailure() error = %v", err)
	}
	if !compact {
		t.Fatal("expected retry-exhausted high-water decision to compact")
	}
	if decision.Reason != "model_request_retry_exhausted_high_water" {
		t.Fatalf("reason = %q, want model_request_retry_exhausted_high_water", decision.Reason)
	}
	if decision.Kind != compactionRecoveryKindRetryExhausted {
		t.Fatalf("kind = %q, want %q", decision.Kind, compactionRecoveryKindRetryExhausted)
	}
	if len(decision.Events) == 0 {
		t.Fatal("expected decision to retain event snapshot for recovery")
	}
}

func forceGateRuntimeForWrapTest() *Runtime {
	cfg := normalizeCompactionConfig(CompactionConfig{Enabled: true})
	return &Runtime{
		compaction: cfg,
		compactor:  newCodexStyleCompactor(cfg),
	}
}

type nonForceGateCompactor struct{}

func (nonForceGateCompactor) Prepare(context.Context, compact.Request) (compact.Result, error) {
	return compact.Result{}, nil
}

func (nonForceGateCompactor) CompactOnOverflow(context.Context, compact.Request, error) (compact.Result, error) {
	return compact.Result{}, nil
}

type searchCapableGateModel struct{}

func (searchCapableGateModel) Name() string { return "search-capable-gate" }

func (searchCapableGateModel) ProviderName() string { return "test-search" }

func (searchCapableGateModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}

func (searchCapableGateModel) SearchWeb(_ context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	return model.WebSearchResponse{
		Query:    req.Query,
		Provider: "test-search",
		Model:    "search-capable-gate",
		Answer:   "answer",
		Results: []model.WebSearchResult{{
			Title: "Result",
			URL:   "https://example.com/result",
		}},
	}, nil
}

func webSearchResultPayloadForTest(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].JSON == nil {
		t.Fatalf("result content = %#v, want one JSON part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("decode result JSON: %v", err)
	}
	return payload
}

func newGateDecisionRuntimeForTest(t *testing.T, cfg CompactionConfig) (*Runtime, session.Session) {
	t.Helper()
	sessions, activeSession := newTestSessionService(t, "sess-gate-decision")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("initial user"))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("initial assistant"))
	return &Runtime{
		sessions:   sessions,
		compaction: normalizeCompactionConfig(cfg),
	}, activeSession
}
