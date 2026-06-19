package controladapter

import "testing"

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
