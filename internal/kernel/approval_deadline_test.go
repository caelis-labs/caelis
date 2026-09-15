package kernel

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestAutoReviewBudgetPreservesAdmissionAndCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithTimeout(t.Context(), 75*time.Second)
		defer cancel()
		ctx, closeBudget := WithAutoReviewBudget(parent)
		defer closeBudget()
		start := AutoReviewStarted(ctx)
		time.Sleep(60 * time.Second)
		child, closeChild := WithAutoReviewBudget(ctx)
		defer closeChild()
		deadline, _ := child.Deadline()
		if AutoReviewStarted(child) != start || time.Until(deadline) != 15*time.Second {
			t.Fatal("continuation reset approval clock")
		}
	})
}
