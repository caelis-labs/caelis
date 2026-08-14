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
	driver := &assembler{stack: &RuntimeStack{
		Gateway: GatewayRuntimeDeps{SessionServiceFn: func() GatewaySessionService { return gw }},
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws"},
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
}

func TestCompleteResumeKeepsOrdinarySessionWithoutTitle(t *testing.T) {
	t.Parallel()

	gw := &pagedResumeCompletionGateway{pages: map[string]session.SessionList{
		"": {Sessions: []session.SessionSummary{{
			SessionRef: session.SessionRef{SessionID: "ordinary-untitled-session"},
			UpdatedAt:  time.Unix(1000, 0),
		}}},
	}}
	driver := &assembler{stack: &RuntimeStack{
		Gateway: GatewayRuntimeDeps{SessionServiceFn: func() GatewaySessionService { return gw }},
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws"},
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
	driver := &assembler{stack: &RuntimeStack{
		Gateway: GatewayRuntimeDeps{SessionServiceFn: func() GatewaySessionService { return gw }},
		Session: SessionRuntimeDeps{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws"},
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
