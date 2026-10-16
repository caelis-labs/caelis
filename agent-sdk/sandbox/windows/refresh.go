package windows

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// runtimeRefresh owns refresh admission and shutdown independently of native
// Windows calls. Completion includes releasing the refresh's Runtime use.
type runtimeRefresh struct {
	refreshMu          sync.RWMutex
	refreshRunning     bool
	refreshClosed      bool
	refreshCancel      context.CancelFunc
	refreshDone        chan struct{}
	lastRefreshError   string
	lastRefreshAt      time.Time
	lastCacheCleanupAt time.Time
	lastCacheBytes     int64
}

func (r *runtimeRefresh) beginRefresh(ctx context.Context) (context.Context, bool) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.refreshClosed || r.refreshRunning {
		return nil, false
	}
	ctx, r.refreshCancel = context.WithCancel(ctx)
	r.refreshDone = make(chan struct{})
	r.refreshRunning = true
	return ctx, true
}

func (r *runtimeRefresh) finishRefresh(err error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.refreshCancel()
	r.refreshCancel = nil
	r.refreshRunning = false
	r.lastRefreshAt = time.Now().UTC()
	r.lastRefreshError = ""
	if err != nil && !errors.Is(err, context.Canceled) {
		r.lastRefreshError = strings.TrimSpace(err.Error())
	}
	close(r.refreshDone)
}

func (r *runtimeRefresh) stopRefresh() {
	r.refreshMu.Lock()
	r.refreshClosed = true
	cancel, done := r.refreshCancel, r.refreshDone
	r.refreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (r *runtimeRefresh) refreshSnapshot() (running bool, lastErr string, lastAt time.Time, lastCacheCleanup time.Time, lastCacheBytes int64) {
	r.refreshMu.RLock()
	defer r.refreshMu.RUnlock()
	return r.refreshRunning, strings.TrimSpace(r.lastRefreshError), r.lastRefreshAt, r.lastCacheCleanupAt, r.lastCacheBytes
}
