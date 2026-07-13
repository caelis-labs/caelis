package controladapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
