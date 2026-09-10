package web

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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

func TestSearchToolIsParallelSafe(t *testing.T) {
	t.Parallel()

	if !NewSearch().Definition().Capabilities.ParallelSafe {
		t.Fatal("WebSearch ParallelSafe = false, want concurrent same-step searches")
	}
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
	if message, _ := payload["message"].(string); !strings.Contains(message, "WebFetch") {
		t.Fatalf("message = %q, want WebFetch fallback guidance", message)
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
		Usage:    model.Usage{TotalTokens: 15},
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
	for _, field := range []string{"query", "provider", "model", "usage", "citations"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("successful result repeated metadata or empty field %q: %#v", field, payload)
		}
	}
	meta := result.Metadata["caelis"].(map[string]any)["runtime"].(map[string]any)["tool"].(map[string]any)
	if meta["query"] != "latest release" || meta["model"] != llm.resp.Model || meta["usage"].(map[string]any)["total_tokens"] != 15 {
		t.Fatalf("tool metadata lost diagnostics: %#v", meta)
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

func TestSearchToolStoresSourcesOnceAndKeepsCitationOnlySources(t *testing.T) {
	t.Parallel()
	source := model.CitationSource{RefID: "ref-a", Title: "Primary", URL: "https://example.com/a", Snippet: "Evidence retained in full"}
	other := model.CitationSource{RefID: "ref-a", URL: "https://example.com/other"}
	noRef := model.CitationSource{URL: "https://example.com/no-ref"}
	llm := searchableLLM{resp: model.WebSearchResponse{
		Answer:  "answer",
		Results: []model.WebSearchResult{{}, {Title: " \t"}, model.WebSearchResult(source)},
		Citations: []model.Citation{
			{StartIndex: 0, EndIndex: 3, Sources: []model.CitationSource{source, other}},
			{StartIndex: 3, EndIndex: 6, Sources: []model.CitationSource{source, noRef}},
		},
	}}
	result, err := NewSearch().Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"evidence"}`), RuntimeModel: llm})
	if err != nil {
		t.Fatal(err)
	}
	payload := resultPayload(t, result)
	results := payload["results"].([]any)
	if len(results) != 1 || payload["answer"] != "answer" {
		t.Fatalf("search lost or repeated sources: %#v", payload)
	}
	citations := payload["citations"].([]any)
	for i, want := range [][]any{{float64(0)}, {float64(0)}} {
		citation := citations[i].(map[string]any)
		if len(citation["sources"].([]any)) != 1 || !reflect.DeepEqual(citation["result_indices"], want) {
			t.Fatalf("citation %d = %#v", i, citation)
		}
	}
	raw := string(result.Content[0].JSON.Value)
	if strings.Count(raw, source.Snippet) != 1 || !strings.Contains(raw, noRef.URL) || !strings.Contains(raw, other.URL) {
		t.Fatalf("source content changed: %s", raw)
	}
}

func TestSearchToolPreservesDuplicateResultPositions(t *testing.T) {
	t.Parallel()
	first := model.WebSearchResult{Title: "First", URL: "https://example.com/a"}
	last := model.WebSearchResult{Title: "Last", URL: "https://example.com/b"}
	llm := searchableLLM{resp: model.WebSearchResponse{Results: []model.WebSearchResult{first, first, last}}}
	result, err := NewSearch().Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"test"}`), RuntimeModel: llm})
	if err != nil {
		t.Fatal(err)
	}
	results := resultPayload(t, result)["results"].([]any)
	if len(results) != 3 || results[2].(map[string]any)["url"] != last.URL {
		t.Fatalf("legacy positional references changed: %#v", results)
	}
}
