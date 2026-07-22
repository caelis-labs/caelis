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
	driver := &Adapter{stack: &RuntimeStack{
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
	driver := &Adapter{stack: &RuntimeStack{
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

type countingResumeSessionLoader struct {
	loads int
}

func (l *countingResumeSessionLoader) LoadSession(context.Context, session.LoadSessionRequest) (session.LoadedSession, error) {
	l.loads++
	return session.LoadedSession{State: map[string]any{"gateway.current_model_alias": "model-from-history"}}, nil
}

func TestResumeCandidateEnrichmentUsesIndexSummaryWithoutLoadingHistory(t *testing.T) {
	t.Parallel()

	loader := &countingResumeSessionLoader{}
	for i := 0; i < 200; i++ {
		summary := session.SessionSummary{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "user-1", WorkspaceKey: "workspace-1",
				SessionID: fmt.Sprintf("session-%03d", i),
			},
			CWD:       "/tmp/workspace-1",
			Title:     fmt.Sprintf("task %03d", i),
			UpdatedAt: time.Unix(int64(i), 0),
		}
		candidate := enrichResumeCandidate(context.Background(), loader, summary)
		if candidate.SessionID != summary.SessionID || candidate.Title != summary.Title || candidate.Workspace != summary.CWD {
			t.Fatalf("candidate %d = %#v, want summary fields", i, candidate)
		}
	}
	if loader.loads != 0 {
		t.Fatalf("LoadSession() calls = %d, want 0 for 200 completion summaries", loader.loads)
	}
}
