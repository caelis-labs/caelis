package windows

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestRuntimeRefreshCloseCancelsAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var refresh runtimeRefresh
		parent, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctx, started := refresh.beginRefresh(parent)
		if !started {
			t.Fatal("refresh was not admitted")
		}
		if _, started := refresh.beginRefresh(t.Context()); started {
			t.Fatal("overlapping refresh was admitted")
		}
		releaseUse := make(chan struct{})
		finishUse := sync.OnceFunc(func() { close(releaseUse) })
		defer finishUse()
		useReleased := false
		go func() {
			<-ctx.Done()
			<-releaseUse
			useReleased = true
			refresh.finishRefresh(ctx.Err())
		}()
		closed := make(chan struct{})
		go func() {
			refresh.stopRefresh()
			close(closed)
		}()
		synctest.Wait()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("refresh context = %v, want cancellation during Close", ctx.Err())
		}
		select {
		case <-closed:
			t.Fatal("Close returned before refresh released its Runtime use")
		default:
		}
		if _, started := refresh.beginRefresh(t.Context()); started {
			t.Fatal("refresh was admitted during Close")
		}
		finishUse()
		<-closed
		if !useReleased {
			t.Fatal("Close did not join the refresh")
		}
		if running, lastErr, at, _, _ := refresh.refreshSnapshot(); running || lastErr != "" || at.IsZero() {
			t.Fatalf("closed refresh snapshot = %v, %q, %v", running, lastErr, at)
		}
		refresh.stopRefresh()
		if _, started := refresh.beginRefresh(t.Context()); started {
			t.Fatal("refresh was admitted after Close")
		}
	})
}

func TestRuntimeRefreshCompletionAndIdleClose(t *testing.T) {
	var refresh runtimeRefresh
	for _, outcome := range []error{errors.New("refresh failed"), nil} {
		ctx, started := refresh.beginRefresh(t.Context())
		if !started {
			t.Fatal("refresh was not admitted after the previous completion")
		}
		refresh.finishRefresh(outcome)
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("completed refresh context = %v", ctx.Err())
		}
		want := ""
		if outcome != nil {
			want = outcome.Error()
		}
		if running, lastErr, at, _, _ := refresh.refreshSnapshot(); running || lastErr != want || at.IsZero() {
			t.Fatalf("completed refresh snapshot = %v, %q, %v", running, lastErr, at)
		}
	}
	refresh.stopRefresh()
	var unused runtimeRefresh
	unused.stopRefresh()
	if _, started := unused.beginRefresh(t.Context()); started {
		t.Fatal("first refresh was admitted after idle Close")
	}
}
