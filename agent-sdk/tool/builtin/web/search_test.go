package web

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/internal/toolpresentation"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type searchStub struct {
	resp model.WebSearchResponse
	err  error
}

func (s searchStub) SearchWeb(_ context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	if s.err != nil {
		return model.WebSearchResponse{}, s.err
	}
	resp := s.resp
	resp.Query = req.Query
	return resp, nil
}

func TestSearchToolReturnsUnavailableFallbackForUnsupportedProvider(t *testing.T) {
	t.Parallel()

	result, err := NewSearch().CallWithBackend(context.Background(), tool.Call{
		Input: json.RawMessage(`{"query":"latest release"}`),
	}, SearchBackend{Provider: "plain"})
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

func TestSearchToolReturnsUnavailableFallbackForProviderReason(t *testing.T) {
	t.Parallel()

	result, err := NewSearch().CallWithBackend(context.Background(), tool.Call{
		Input: json.RawMessage(`{"query":"latest release"}`),
	}, SearchBackend{
		Provider:          "plain",
		UnavailableReason: "Xiaomi Token Plan endpoints do not support provider-native web search.",
		SearchWeb: func(context.Context, model.WebSearchRequest) (model.WebSearchResponse, error) {
			t.Fatal("provider search ran despite the explicit availability reason")
			return model.WebSearchResponse{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "unavailable" {
		t.Fatalf("status = %#v, want unavailable", got)
	}
	message, _ := payload["message"].(string)
	for _, want := range []string{"Token Plan", "Use " + FetchToolName + " with a known URL"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want %q", message, want)
		}
	}
}

func TestSearchToolReturnsProviderResults(t *testing.T) {
	t.Parallel()

	backend := searchStub{resp: model.WebSearchResponse{
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
	var meta map[string]any
	ctx := toolpresentation.With(t.Context(), func(fields map[string]any) { meta = fields })
	result, err := NewSearch().CallWithBackend(ctx, tool.Call{
		Input: json.RawMessage(`{"query":"latest release","max_results":1}`),
	}, SearchBackend{SearchWeb: backend.SearchWeb})
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
	if meta["query"] != "latest release" || meta["model"] != backend.resp.Model || meta["usage"].(map[string]any)["total_tokens"] != 15 {
		t.Fatalf("tool metadata lost diagnostics: %#v", meta)
	}

}

func TestSearchToolReturnsErrorResultForProviderFailure(t *testing.T) {
	t.Parallel()

	backend := searchStub{err: errors.New("quota exhausted")}
	result, err := NewSearch().CallWithBackend(context.Background(), tool.Call{
		Input: json.RawMessage(`{"query":"latest release"}`),
	}, SearchBackend{SearchWeb: backend.SearchWeb})
	if !tool.HasFailedReply(err) {
		t.Fatalf("Call() error = %v, want failed reply", err)
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "failed" {
		t.Fatalf("status = %#v, want failed", got)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "quota exhausted") || err.Error() != message {
		t.Fatalf("message = %q, error = %v; want matching provider error detail", message, err)
	}
}

func resultPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	structured, ok := result.Structured()
	if len(result.Content) != 1 || !ok {
		t.Fatalf("result content = %#v, want one JSON part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(structured, &payload); err != nil {
		t.Fatalf("decode result JSON: %v", err)
	}
	return payload
}

func TestSearchToolStoresSourcesOnceAndKeepsCitationOnlySources(t *testing.T) {
	t.Parallel()
	source := model.CitationSource{RefID: "ref-a", Title: "Primary", URL: "https://example.com/a", Snippet: "Evidence retained in full"}
	other := model.CitationSource{RefID: "ref-a", URL: "https://example.com/other"}
	noRef := model.CitationSource{URL: "https://example.com/no-ref"}
	backend := searchStub{resp: model.WebSearchResponse{
		Answer:  "answer",
		Results: []model.WebSearchResult{{}, {Title: " \t"}, model.WebSearchResult(source)},
		Citations: []model.Citation{
			{StartIndex: 0, EndIndex: 3, Sources: []model.CitationSource{source, other}},
			{StartIndex: 3, EndIndex: 6, Sources: []model.CitationSource{source, noRef}},
		},
	}}
	result, err := NewSearch().CallWithBackend(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"evidence"}`)}, SearchBackend{SearchWeb: backend.SearchWeb})
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
	structured, _ := result.Structured()
	raw := string(structured)
	if strings.Count(raw, source.Snippet) != 1 || !strings.Contains(raw, noRef.URL) || !strings.Contains(raw, other.URL) {
		t.Fatalf("source content changed: %s", raw)
	}
}

func TestSearchToolPreservesDuplicateResultPositions(t *testing.T) {
	t.Parallel()
	first := model.WebSearchResult{Title: "First", URL: "https://example.com/a"}
	last := model.WebSearchResult{Title: "Last", URL: "https://example.com/b"}
	backend := searchStub{resp: model.WebSearchResponse{Results: []model.WebSearchResult{first, first, last}}}
	result, err := NewSearch().CallWithBackend(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"test"}`)}, SearchBackend{SearchWeb: backend.SearchWeb})
	if err != nil {
		t.Fatal(err)
	}
	results := resultPayload(t, result)["results"].([]any)
	if len(results) != 3 || results[2].(map[string]any)["url"] != last.URL {
		t.Fatalf("legacy positional references changed: %#v", results)
	}
}
