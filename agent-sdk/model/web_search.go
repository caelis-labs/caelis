package model

import (
	"context"
	"strings"
)

// WebSearchRequest is one explicit, user-visible web discovery request.
type WebSearchRequest struct {
	Query string `json:"query,omitempty"`
	// MaxResults is a best-effort hint for search backends that support
	// caller-side result limits. Provider-native server search adapters return
	// every valid result supplied by the provider in its original order.
	MaxResults int `json:"max_results,omitempty"`
}

// WebSearchResult is one cited search source returned by a provider-native
// search backend.
type WebSearchResult struct {
	RefID       string `json:"ref_id,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	Source      string `json:"source,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// WebSearchResponse is the normalized result of one explicit web search tool
// call. Answer is optional provider-generated context; Results are the cited
// sources the model should inspect or cite.
type WebSearchResponse struct {
	Query     string            `json:"query,omitempty"`
	Provider  string            `json:"provider,omitempty"`
	Model     string            `json:"model,omitempty"`
	Answer    string            `json:"answer,omitempty"`
	Results   []WebSearchResult `json:"results,omitempty"`
	Citations []Citation        `json:"citations,omitempty"`
	Usage     Usage             `json:"usage,omitempty"`
}

// WebSearcher is an optional model capability for provider-native web search.
// It must only be used from an explicit local tool call, never as an implicit
// default on ordinary model turns. SearchWeb may be called concurrently when
// one model step emits multiple searches; implementations must synchronize any
// shared mutable state. Implementations must preserve the caller context and
// must not impose a provider-independent whole-request deadline. An explicitly
// configured provider request timeout may still bound the call.
type WebSearcher interface {
	SearchWeb(context.Context, WebSearchRequest) (WebSearchResponse, error)
}

// WebSearchAvailability is an optional model capability for explaining why a
// provider-native web search tool is unavailable even though the provider is
// otherwise usable for normal model turns.
type WebSearchAvailability interface {
	WebSearchUnavailableReason() string
}

func NormalizeWebSearchRequest(req WebSearchRequest) WebSearchRequest {
	req.Query = strings.TrimSpace(req.Query)
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}
	if req.MaxResults > 10 {
		req.MaxResults = 10
	}
	return req
}
