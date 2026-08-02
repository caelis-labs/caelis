// Package providerusage owns provider-neutral subscription usage semantics.
// Provider adapters translate their private account APIs into this model;
// presentation surfaces consume only the normalized projection exposed by
// Control.
package providerusage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Window describes one provider-enforced rolling usage window.
type Window struct {
	Kind        string
	Label       string
	UsedPercent float64
	Duration    time.Duration
	ResetsAt    time.Time
}

// Limit groups one or more windows that meter the same provider feature.
type Limit struct {
	ID      string
	Name    string
	Windows []Window
}

// Snapshot is one non-secret provider subscription usage observation.
type Snapshot struct {
	Provider   string
	Plan       string
	CapturedAt time.Time
	Limits     []Limit
}

// Reader obtains a provider's current subscription usage.
type Reader interface {
	SubscriptionUsage(context.Context) (Snapshot, error)
}

const (
	asyncRefreshInterval      = time.Minute
	asyncFailureRetryInterval = 5 * time.Second
	asyncRefreshTimeout       = 20 * time.Second
)

// Registry routes provider usage queries without exposing provider-specific
// response types to product orchestration or presentation surfaces.
type Registry struct {
	mu      sync.Mutex
	readers map[string]Reader
	states  map[string]*refreshState
}

type refreshState struct {
	snapshot    Snapshot
	hasSnapshot bool
	refreshing  bool
	nextRefresh time.Time
	lastErr     error
}

// NewRegistry constructs a registry from normalized provider names.
func NewRegistry(readers map[string]Reader) *Registry {
	registry := &Registry{
		readers: make(map[string]Reader, len(readers)),
		states:  make(map[string]*refreshState, len(readers)),
	}
	for provider, reader := range readers {
		provider = normalizeProvider(provider)
		if provider != "" && reader != nil {
			registry.readers[provider] = reader
		}
	}
	return registry
}

// Query returns the last successful snapshot immediately and schedules a
// bounded refresh when that snapshot is absent or stale. found=false means the
// provider has no usage reader. The first supported query can return an empty
// snapshot while its refresh is in flight. Refresh failures retain a previous
// snapshot; before the first success, Query reports the last refresh failure
// without waiting for a new attempt.
func (r *Registry) Query(ctx context.Context, provider string) (Snapshot, bool, error) {
	if r == nil {
		return Snapshot{}, false, nil
	}
	provider = normalizeProvider(provider)
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	reader := r.readers[provider]
	if reader == nil {
		r.mu.Unlock()
		return Snapshot{}, false, nil
	}
	state := r.states[provider]
	if state == nil {
		state = &refreshState{}
		r.states[provider] = state
	}
	now := time.Now()
	if !state.refreshing && (state.nextRefresh.IsZero() || !now.Before(state.nextRefresh)) {
		state.refreshing = true
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncRefreshTimeout)
		go func() {
			defer cancel()
			r.refresh(refreshCtx, provider, reader)
		}()
	}
	if !state.hasSnapshot {
		err := state.lastErr
		r.mu.Unlock()
		return Snapshot{}, true, err
	}
	snapshot := CloneSnapshot(state.snapshot)
	r.mu.Unlock()
	return snapshot, true, nil
}

func (r *Registry) refresh(ctx context.Context, provider string, reader Reader) {
	var (
		snapshot Snapshot
		err      error
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("providerusage: refresh %q panicked: %v", provider, recovered)
		}
		r.finishRefresh(provider, snapshot, err)
	}()

	snapshot, err = reader.SubscriptionUsage(ctx)
	if err != nil {
		err = fmt.Errorf("providerusage: refresh %q: %w", provider, err)
	}
}

func (r *Registry) finishRefresh(provider string, snapshot Snapshot, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.states[provider]
	if state == nil {
		return
	}
	state.refreshing = false
	state.lastErr = err
	if err != nil {
		state.nextRefresh = time.Now().Add(asyncFailureRetryInterval)
		return
	}
	state.snapshot = CloneSnapshot(snapshot)
	state.hasSnapshot = true
	state.nextRefresh = time.Now().Add(asyncRefreshInterval)
}

// CloneSnapshot returns an independent copy of one usage observation.
func CloneSnapshot(in Snapshot) Snapshot {
	out := in
	if len(in.Limits) == 0 {
		out.Limits = nil
		return out
	}
	out.Limits = make([]Limit, len(in.Limits))
	for i, limit := range in.Limits {
		out.Limits[i] = limit
		out.Limits[i].Windows = append([]Window(nil), limit.Windows...)
	}
	return out
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
