package controladapter

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestCompleteModelSpeedModesOffersFastOnlyForSupportedGPT(t *testing.T) {
	t.Parallel()

	configs := map[string]ModelConfig{
		"openai/gpt-5.6-sol": {Provider: "openai", Model: "gpt-5.6-sol"},
		"openai/gpt-6-astra": {Provider: "openai", Model: "gpt-6-astra"},
		"openai/gpt-5.5-pro": {Provider: "openai", Model: "gpt-5.5-pro"},
	}
	driver := &assembler{deps: &runtimeDeps{Model: ModelRuntimeDeps{
		ConfigFn: func(alias string) (ModelConfig, bool) {
			config, ok := configs[alias]
			return config, ok
		},
		ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) {
			return []ModelChoice{
				{ID: "openai/gpt-5.6-sol", Alias: "openai/gpt-5.6-sol", ReasoningLevels: []string{"xhigh"}},
				{ID: "openai/gpt-6-astra", Alias: "openai/gpt-6-astra", ReasoningLevels: []string{"xhigh"}},
				{ID: "openai/gpt-5.5-pro", Alias: "openai/gpt-5.5-pro", ReasoningLevels: []string{"xhigh"}},
			}, nil
		},
	}}}

	fast, err := driver.CompleteSlashArg(context.Background(), "model openai/gpt-5.6-sol xhigh", "", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(fast) != 2 || fast[0].Value != "default" || fast[1].Value != "fast" || fast[1].Detail != "1.5x faster, more usage" {
		t.Fatalf("supported GPT speed candidates = %#v, want default and model-specific Fast hint", fast)
	}
	astra, err := driver.CompleteSlashArg(context.Background(), "model openai/gpt-6-astra xhigh", "", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(astra) != 2 || astra[1].Value != "fast" || astra[1].Detail != "2x faster, more usage" {
		t.Fatalf("Astra speed candidates = %#v, want 2x Fast hint", astra)
	}
	standard, err := driver.CompleteSlashArg(context.Background(), "model openai/gpt-5.5-pro xhigh", "", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(standard) != 0 {
		t.Fatalf("unsupported model speed candidates = %#v, want no speed picker", standard)
	}
}

func TestNormalizeCompletionLimitAllowsPagedCompletion(t *testing.T) {
	t.Parallel()

	if got := normalizeCompletionLimit(0); got != defaultCompletionLimit {
		t.Fatalf("normalizeCompletionLimit(0) = %d, want %d", got, defaultCompletionLimit)
	}
	if got := normalizeCompletionLimit(120); got != 120 {
		t.Fatalf("normalizeCompletionLimit(120) = %d, want 120", got)
	}
	if got := normalizeCompletionLimit(maxCompletionLimit + 1); got != maxCompletionLimit {
		t.Fatalf("normalizeCompletionLimit(max+1) = %d, want %d", got, maxCompletionLimit)
	}
}

type pagedResumeCompletionGateway struct {
	requests []kernel.ListSessionsRequest
	pages    map[string]session.SessionList
	errors   map[string]error
}

func (g *pagedResumeCompletionGateway) ResumeSession(context.Context, kernel.ResumeSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{}, nil
}

func (g *pagedResumeCompletionGateway) ListSessions(_ context.Context, req kernel.ListSessionsRequest) (session.SessionList, error) {
	g.requests = append(g.requests, req)
	if err := g.errors[req.Cursor]; err != nil {
		return session.SessionList{}, err
	}
	return g.pages[req.Cursor], nil
}

func TestCompleteResumeSearchesBeyondFirstTwoHundredSessions(t *testing.T) {
	t.Parallel()

	first := make([]session.SessionSummary, 0, 200)
	for i := 0; i < 200; i++ {
		first = append(first, session.SessionSummary{
			SessionRef: session.SessionRef{SessionID: fmt.Sprintf("recent-%03d", i)},
			Title:      fmt.Sprintf("ordinary task %03d", i), UpdatedAt: time.Unix(int64(1000-i), 0),
		})
	}
	gw := &pagedResumeCompletionGateway{pages: map[string]session.SessionList{
		"": {Sessions: first, NextCursor: "page-2"},
		"page-2": {Sessions: []session.SessionSummary{{
			SessionRef: session.SessionRef{SessionID: "old-matching-session"},
			Title:      "needle target", UpdatedAt: time.Unix(1, 0),
		}}},
	}}
	driver := &assembler{deps: &runtimeDeps{
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws"}, ListSessionsFn: gw.ListSessions,
		},
	}}
	candidates, err := driver.CompleteResume(context.Background(), "needle", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].SessionID != "old-matching-session" {
		t.Fatalf("CompleteResume() = %#v, want old second-page match", candidates)
	}
	if len(gw.requests) != 2 || gw.requests[1].Cursor != "page-2" {
		t.Fatalf("ListSessions() requests = %#v, want exhaustive pagination", gw.requests)
	}
	if gw.requests[0].UserID != "" || gw.requests[1].UserID != "" {
		t.Fatalf("ListSessions() requests = %#v, want principal-bound calls without Runtime user partition", gw.requests)
	}
}

func TestCompleteResumeRequiresPrincipalBoundSessionList(t *testing.T) {
	t.Parallel()

	driver := &assembler{deps: &runtimeDeps{
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "legacy-user", Workspace: session.WorkspaceRef{Key: "ws"},
		},
	}}
	if _, err := driver.CompleteResume(context.Background(), "", 8); err == nil {
		t.Fatal("CompleteResume() error = nil, want missing principal-bound Session list capability")
	}
}

func TestCompleteResumeKeepsOrdinarySessionWithoutTitle(t *testing.T) {
	t.Parallel()

	gw := &pagedResumeCompletionGateway{pages: map[string]session.SessionList{
		"": {Sessions: []session.SessionSummary{{
			SessionRef: session.SessionRef{SessionID: "ordinary-untitled-session"},
			UpdatedAt:  time.Unix(1000, 0),
		}}},
	}}
	driver := &assembler{deps: &runtimeDeps{
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws"}, ListSessionsFn: gw.ListSessions,
		},
	}}

	for _, query := range []string{"", "ordinary"} {
		candidates, err := driver.CompleteResume(context.Background(), query, 8)
		if err != nil {
			t.Fatalf("CompleteResume(%q) error = %v", query, err)
		}
		if len(candidates) != 1 || candidates[0].SessionID != "ordinary-untitled-session" {
			t.Fatalf("CompleteResume(%q) = %#v, want ordinary untitled Session", query, candidates)
		}
	}
}

func TestCompleteResumeStopsWhenFirstPageSatisfiesLimit(t *testing.T) {
	t.Parallel()

	first := make([]session.SessionSummary, 0, 8)
	for i := 0; i < 8; i++ {
		first = append(first, session.SessionSummary{
			SessionRef: session.SessionRef{SessionID: fmt.Sprintf("matching-%03d", i)},
			Title:      fmt.Sprintf("needle task %03d", i),
			UpdatedAt:  time.Unix(int64(1000-i), 0),
		})
	}
	gw := &pagedResumeCompletionGateway{
		pages: map[string]session.SessionList{
			"": {Sessions: first, NextCursor: "page-2"},
		},
		errors: map[string]error{"page-2": errors.New("later page unavailable")},
	}
	driver := &assembler{deps: &runtimeDeps{
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws"}, ListSessionsFn: gw.ListSessions,
		},
	}}

	candidates, err := driver.CompleteResume(context.Background(), "needle", 8)
	if err != nil {
		t.Fatalf("CompleteResume() error = %v, want first-page candidates", err)
	}
	if len(candidates) != 8 {
		t.Fatalf("CompleteResume() returned %d candidates, want 8", len(candidates))
	}
	if len(gw.requests) != 1 {
		t.Fatalf("ListSessions() requests = %#v, want no access to failing later page", gw.requests)
	}
}

func TestResumeCandidateEnrichmentUsesIndexSummaryVerbatim(t *testing.T) {
	t.Parallel()

	tests := []session.SessionSummary{
		{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "user-1", WorkspaceKey: "workspace-1", SessionID: "healthy-session",
			},
			CWD:       "/tmp/workspace-1",
			Title:     "healthy title",
			UpdatedAt: time.Unix(1, 0),
		},
		{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "user-1", WorkspaceKey: "workspace-1", SessionID: "explicit-replacement-rune",
			},
			CWD:       "/tmp/workspace-1",
			Title:     "explicit title \uFFFD",
			UpdatedAt: time.Unix(2, 0),
		},
	}
	for _, summary := range tests {
		candidate := enrichResumeCandidate(summary)
		if candidate.SessionID != summary.SessionID || candidate.Title != summary.Title || candidate.Prompt != summary.Title || candidate.Workspace != summary.CWD {
			t.Fatalf("enrichResumeCandidate(%q) = %#v, want index summary fields", summary.SessionID, candidate)
		}
	}
}
