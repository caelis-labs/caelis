package providerusage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type queryResult struct {
	snapshot Snapshot
	found    bool
	err      error
}

type blockingTestReader struct {
	calls    atomic.Int32
	started  chan context.Context
	release  chan struct{}
	snapshot Snapshot
}

func (r *blockingTestReader) SubscriptionUsage(ctx context.Context) (Snapshot, error) {
	r.calls.Add(1)
	r.started <- ctx
	select {
	case <-r.release:
		return r.snapshot, nil
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

type scriptedRead struct {
	snapshot   Snapshot
	err        error
	panicValue any
}

type scriptedTestReader struct {
	calls   atomic.Int32
	results chan scriptedRead
}

func (r *scriptedTestReader) SubscriptionUsage(context.Context) (Snapshot, error) {
	r.calls.Add(1)
	select {
	case result := <-r.results:
		if result.panicValue != nil {
			panic(result.panicValue)
		}
		return result.snapshot, result.err
	default:
		return Snapshot{}, errors.New("unexpected provider usage read")
	}
}

func TestRegistryUnsupportedProviderIsAbsent(t *testing.T) {
	registry := NewRegistry(nil)
	if got, found, err := registry.Query(context.Background(), "gemini"); err != nil || found || len(got.Limits) != 0 {
		t.Fatalf("Query() = %#v, %v, %v; want absent", got, found, err)
	}
}

func TestRegistryQueryDoesNotWaitAndCachesRefresh(t *testing.T) {
	want := Snapshot{
		Provider: "openai-codex",
		Limits:   []Limit{{ID: "codex", Windows: []Window{{Duration: 5 * time.Hour}}}},
	}
	reader := &blockingTestReader{
		started:  make(chan context.Context, 1),
		release:  make(chan struct{}),
		snapshot: want,
	}
	registry := NewRegistry(map[string]Reader{"openai-codex": reader})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer release()

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan queryResult, 1)
	go func() {
		snapshot, found, err := registry.Query(parent, " openai-CODEX ")
		returned <- queryResult{snapshot: snapshot, found: found, err: err}
	}()

	select {
	case result := <-returned:
		if result.err != nil || !result.found || !reflect.DeepEqual(result.snapshot, Snapshot{}) {
			t.Fatalf("first Query() = %#v, %v, %v; want supported empty cache", result.snapshot, result.found, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Query blocked on provider refresh")
	}

	var refreshCtx context.Context
	select {
	case refreshCtx = <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("asynchronous provider refresh did not start")
	}
	cancel()
	if err := refreshCtx.Err(); err != nil {
		t.Fatalf("caller cancellation reached background refresh: %v", err)
	}

	if snapshot, found, err := registry.Query(context.Background(), "openai-codex"); err != nil || !found || !reflect.DeepEqual(snapshot, Snapshot{}) {
		t.Fatalf("in-flight Query() = %#v, %v, %v; want supported empty cache", snapshot, found, err)
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("in-flight provider refresh calls = %d, want 1", calls)
	}

	release()
	state := waitForRegistryState(t, registry, "openai-codex", func(state refreshState) bool {
		return !state.refreshing && state.hasSnapshot
	})
	if remaining := time.Until(state.nextRefresh); remaining <= 0 || remaining > asyncRefreshInterval {
		t.Fatalf("successful refresh TTL remaining = %v, want within (0, %v]", remaining, asyncRefreshInterval)
	}

	got, found, err := registry.Query(context.Background(), "openai-codex")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("cached Query() = %#v, %v, %v; want %#v, true, nil", got, found, err, want)
	}
	got.Limits[0].Windows[0].Duration = time.Hour
	again, _, err := registry.Query(context.Background(), "openai-codex")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("Query returned aliased snapshot: got %#v want %#v", again, want)
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("fresh cache provider calls = %d, want 1", calls)
	}
}

func TestRegistryFailedRefreshRetainsSuccessfulSnapshot(t *testing.T) {
	want := Snapshot{Provider: "xai", Plan: "pro", Limits: []Limit{{ID: "xai"}}}
	refreshErr := errors.New("temporary outage")
	reader := &scriptedTestReader{results: make(chan scriptedRead, 2)}
	reader.results <- scriptedRead{snapshot: want}
	reader.results <- scriptedRead{err: refreshErr}
	registry := NewRegistry(map[string]Reader{"xai": reader})

	_, _, _ = registry.Query(context.Background(), "xai")
	waitForRegistryState(t, registry, "xai", func(state refreshState) bool {
		return !state.refreshing && state.hasSnapshot
	})
	expireRegistryRefreshForTest(registry, "xai")

	got, found, err := registry.Query(context.Background(), "xai")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("stale Query() during refresh = %#v, %v, %v; want %#v, true, nil", got, found, err, want)
	}
	state := waitForRegistryState(t, registry, "xai", func(state refreshState) bool {
		return !state.refreshing && errors.Is(state.lastErr, refreshErr)
	})
	if !reflect.DeepEqual(state.snapshot, want) {
		t.Fatalf("failed refresh replaced cached snapshot: got %#v want %#v", state.snapshot, want)
	}
	if remaining := time.Until(state.nextRefresh); remaining <= 0 || remaining > asyncFailureRetryInterval {
		t.Fatalf("failure retry remaining = %v, want within (0, %v]", remaining, asyncFailureRetryInterval)
	}

	got, found, err = registry.Query(context.Background(), "xai")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("stale Query() after failure = %#v, %v, %v; want %#v, true, nil", got, found, err, want)
	}
	if calls := reader.calls.Load(); calls != 2 {
		t.Fatalf("provider calls inside failure backoff = %d, want 2", calls)
	}
}

func TestRegistryColdFailureReportsErrorAndRetries(t *testing.T) {
	refreshErr := errors.New("temporary outage")
	want := Snapshot{Provider: "openai-codex", Plan: "plus"}
	reader := &scriptedTestReader{results: make(chan scriptedRead, 2)}
	reader.results <- scriptedRead{err: refreshErr}
	reader.results <- scriptedRead{snapshot: want}
	registry := NewRegistry(map[string]Reader{"openai-codex": reader})

	first, found, err := registry.Query(context.Background(), "openai-codex")
	if err != nil || !found || !reflect.DeepEqual(first, Snapshot{}) {
		t.Fatalf("first Query() = %#v, %v, %v; want supported empty cache", first, found, err)
	}
	state := waitForRegistryState(t, registry, "openai-codex", func(state refreshState) bool {
		return !state.refreshing && state.lastErr != nil
	})
	if !errors.Is(state.lastErr, refreshErr) {
		t.Fatalf("stored refresh error = %v, want %v", state.lastErr, refreshErr)
	}
	if remaining := time.Until(state.nextRefresh); remaining <= 0 || remaining > asyncFailureRetryInterval {
		t.Fatalf("cold failure retry remaining = %v, want within (0, %v]", remaining, asyncFailureRetryInterval)
	}

	got, found, err := registry.Query(context.Background(), "openai-codex")
	if !errors.Is(err, refreshErr) || !found || !reflect.DeepEqual(got, Snapshot{}) {
		t.Fatalf("failed cold Query() = %#v, %v, %v; want empty, true, wrapped outage", got, found, err)
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("provider calls inside cold failure backoff = %d, want 1", calls)
	}

	expireRegistryRefreshForTest(registry, "openai-codex")
	_, _, err = registry.Query(context.Background(), "openai-codex")
	if !errors.Is(err, refreshErr) {
		t.Fatalf("retrying Query() error = %v, want previous refresh error", err)
	}
	waitForRegistryState(t, registry, "openai-codex", func(state refreshState) bool {
		return !state.refreshing && state.hasSnapshot && state.lastErr == nil
	})
	got, found, err = registry.Query(context.Background(), "openai-codex")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered Query() = %#v, %v, %v; want %#v, true, nil", got, found, err, want)
	}
	if calls := reader.calls.Load(); calls != 2 {
		t.Fatalf("provider calls after cold retry = %d, want 2", calls)
	}
}

func TestRegistryReaderPanicDoesNotDisableRefresh(t *testing.T) {
	want := Snapshot{Provider: "xai", Plan: "premium"}
	reader := &scriptedTestReader{results: make(chan scriptedRead, 2)}
	reader.results <- scriptedRead{panicValue: "boom"}
	reader.results <- scriptedRead{snapshot: want}
	registry := NewRegistry(map[string]Reader{"xai": reader})

	_, _, _ = registry.Query(context.Background(), "xai")
	state := waitForRegistryState(t, registry, "xai", func(state refreshState) bool {
		return !state.refreshing && state.lastErr != nil
	})
	if !strings.Contains(state.lastErr.Error(), "panicked: boom") {
		t.Fatalf("panic refresh error = %v", state.lastErr)
	}
	if _, _, err := registry.Query(context.Background(), "xai"); err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Fatalf("Query() panic error = %v", err)
	}

	expireRegistryRefreshForTest(registry, "xai")
	_, _, _ = registry.Query(context.Background(), "xai")
	waitForRegistryState(t, registry, "xai", func(state refreshState) bool {
		return !state.refreshing && state.hasSnapshot && state.lastErr == nil
	})
	got, found, err := registry.Query(context.Background(), "xai")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("post-panic Query() = %#v, %v, %v; want %#v, true, nil", got, found, err, want)
	}
}

func waitForRegistryState(t *testing.T, registry *Registry, provider string, ready func(refreshState) bool) refreshState {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		registry.mu.Lock()
		state := registry.states[normalizeProvider(provider)]
		var snapshot refreshState
		if state != nil {
			snapshot = *state
			snapshot.snapshot = CloneSnapshot(state.snapshot)
		}
		registry.mu.Unlock()
		if state != nil && ready(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider %q refresh state did not become ready: %#v", provider, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func expireRegistryRefreshForTest(registry *Registry, provider string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.states[normalizeProvider(provider)].nextRefresh = time.Time{}
}
