package web

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type plainLLM struct{}

func (plainLLM) Name() string { return "plain-model" }

func (plainLLM) ProviderName() string { return "plain" }

func (plainLLM) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}

type searchableLLM struct {
	plainLLM
	resp model.WebSearchResponse
	err  error
}

func (s searchableLLM) SearchWeb(_ context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	if s.err != nil {
		return model.WebSearchResponse{}, s.err
	}
	resp := s.resp
	resp.Query = req.Query
	return resp, nil
}

type unavailableReasonLLM struct {
	plainLLM
	reason string
}

func (u unavailableReasonLLM) WebSearchUnavailableReason() string {
	return u.reason
}

func TestSearchToolReturnsUnavailableFallbackForUnsupportedProvider(t *testing.T) {
	t.Parallel()

	result, err := NewSearch().Call(context.Background(), tool.Call{
		Input:        json.RawMessage(`{"query":"latest release"}`),
		RuntimeModel: plainLLM{},
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "unavailable" {
		t.Fatalf("status = %#v, want unavailable", got)
	}
	if got := payload["provider"]; got != "plain" {
		t.Fatalf("provider = %#v, want plain", got)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "web_fetch") {
		t.Fatalf("message = %q, want web_fetch fallback guidance", message)
	}
}

func TestSearchToolReturnsUnavailableFallbackForRetryWrappedUnsupportedProvider(t *testing.T) {
	t.Parallel()

	result, err := NewSearch().Call(context.Background(), tool.Call{
		Input:        json.RawMessage(`{"query":"latest release"}`),
		RuntimeModel: model.WithRetry(plainLLM{}, model.RetryConfig{}),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.IsError {
		t.Fatal("result.IsError = true, want unavailable fallback to be non-error")
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "unavailable" {
		t.Fatalf("status = %#v, want unavailable", got)
	}
}

func TestSearchToolReturnsUnavailableFallbackForProviderReason(t *testing.T) {
	t.Parallel()

	result, err := NewSearch().Call(context.Background(), tool.Call{
		Input: json.RawMessage(`{"query":"latest release"}`),
		RuntimeModel: unavailableReasonLLM{
			plainLLM: plainLLM{},
			reason:   "Xiaomi Token Plan endpoints do not support provider-native web_search. Use a native Xiaomi MiMo API key, or use web_fetch with a known URL.",
		},
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.IsError {
		t.Fatal("result.IsError = true, want unavailable fallback to be non-error")
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "unavailable" {
		t.Fatalf("status = %#v, want unavailable", got)
	}
	message, _ := payload["message"].(string)
	for _, want := range []string{"Token Plan", "native Xiaomi MiMo API key", "web_fetch"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want %q", message, want)
		}
	}
}

func TestSearchToolReturnsUnavailableFallbackForRetryWrappedProviderReason(t *testing.T) {
	t.Parallel()

	result, err := NewSearch().Call(context.Background(), tool.Call{
		Input: json.RawMessage(`{"query":"latest release"}`),
		RuntimeModel: model.WithRetry(unavailableReasonLLM{
			plainLLM: plainLLM{},
			reason:   "Xiaomi Token Plan endpoints do not support provider-native web_search. Use a native Xiaomi MiMo API key, or use web_fetch with a known URL.",
		}, model.RetryConfig{}),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := resultPayload(t, result)
	message, _ := payload["message"].(string)
	for _, want := range []string{"Token Plan", "native Xiaomi MiMo API key", "web_fetch"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want %q", message, want)
		}
	}
}

func TestSearchToolReturnsProviderResults(t *testing.T) {
	t.Parallel()

	llm := searchableLLM{resp: model.WebSearchResponse{
		Provider: "gemini",
		Model:    "gemini-2.5-flash",
		Answer:   "answer",
		Results: []model.WebSearchResult{{
			Title:  "Result",
			URL:    "https://example.com/result",
			Source: "example.com",
		}},
	}}
	result, err := NewSearch().Call(context.Background(), tool.Call{
		Input:        json.RawMessage(`{"query":"latest release","max_results":1}`),
		RuntimeModel: llm,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "completed" {
		t.Fatalf("status = %#v, want completed", got)
	}
	results, _ := payload["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1: %#v", len(results), payload["results"])
	}
}

func TestSearchToolReturnsErrorResultForProviderFailure(t *testing.T) {
	t.Parallel()

	llm := searchableLLM{err: errors.New("quota exhausted")}
	result, err := NewSearch().Call(context.Background(), tool.Call{
		Input:        json.RawMessage(`{"query":"latest release"}`),
		RuntimeModel: llm,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false, want provider failure marked as tool error")
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "failed" {
		t.Fatalf("status = %#v, want failed", got)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "quota exhausted") {
		t.Fatalf("message = %q, want provider error detail", message)
	}
}

func resultPayload(t *testing.T, result tool.Result) map[string]any {
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
