package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Keep the notification consumer outside this fixture so admission tests can
// observe every queued signal, including one an ordinary watcher would consume.
func refreshAdmissionManager(t *testing.T, handler func(context.Context) error) (*Manager, *atomic.Int32) {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "admission", Version: "1"}, nil)
	catalogTool(server, "read", "stable", nil)
	lists := &atomic.Int32{}
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" {
				lists.Add(1)
				if err := handler(ctx); err != nil {
					return nil, err
				}
			}
			return next(ctx, method, req)
		}
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	t.Cleanup(endpoint.Close)
	spec := ServerSpec{PluginID: "p", Name: "local", Transport: TransportStreamableHTTP, URL: endpoint.URL}
	client, err := startClientWithCatalog(t.Context(), spec, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	mgr := &Manager{servers: []*managedServer{{spec: spec, client: client}}}
	if err := mgr.RefreshTools(t.Context(), "p", "local"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.catalog.changed:
	default:
	}
	return mgr, lists
}

type refreshState struct {
	generation uint64
	dirty      bool
	refreshErr error
	changed    int
}

func captureRefreshState(mgr *Manager) refreshState {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	s := mgr.servers[0]
	c := s.client.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	return refreshState{c.generation, c.dirty, s.refreshErr, len(c.changed)}
}

type refreshWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *refreshWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCatalogRefreshCanceledWaiterHasNoEffects(t *testing.T) {
	var block atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	mgr, lists := refreshAdmissionManager(t, func(ctx context.Context) error {
		if block.Load() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	block.Store(true)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- mgr.RefreshTools(t.Context(), "p", "local") }()
	<-entered
	before, count := captureRefreshState(mgr), lists.Load()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &refreshWaitingContext{Context: parent, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- mgr.RefreshTools(ctx, "p", "local") }()
	select {
	case <-ctx.waiting:
	case <-time.After(100 * time.Millisecond): // The old mutex never observes Done while waiting.
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter error = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("canceled waiter did not return while the owner remained blocked")
	}
	if after := captureRefreshState(mgr); after != before {
		t.Errorf("canceled waiter changed catalog: before=%+v after=%+v", before, after)
	}
	if lists.Load() != count {
		t.Error("canceled waiter started tools/list")
	}
	block.Store(false)
	releaseOnce.Do(func() { close(release) })
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
}

// Cancel during select operand evaluation: the gate and Done are both ready.
// Err was nil at the entry check, so only the ownership recheck closes this race.
type refreshSimultaneousContext struct {
	context.Context
	cancel context.CancelFunc
}

func (c refreshSimultaneousContext) Done() <-chan struct{} {
	c.cancel()
	return c.Context.Done()
}

func TestCatalogRefreshRechecksCancellationAfterAdmission(t *testing.T) {
	mgr, lists := refreshAdmissionManager(t, func(context.Context) error { return nil })
	before, count := captureRefreshState(mgr), lists.Load()
	for range 64 {
		parent, cancel := context.WithCancel(t.Context())
		ctx := refreshSimultaneousContext{Context: parent, cancel: cancel}
		err := mgr.RefreshTools(ctx, "p", "local")
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("simultaneous cancellation error = %v", err)
		}
		if after := captureRefreshState(mgr); after != before {
			t.Fatalf("canceled admission changed catalog: before=%+v after=%+v", before, after)
		}
		if lists.Load() != count {
			t.Fatal("canceled admission started tools/list")
		}
	}
}

func TestCatalogRefreshFailureDoesNotSettleNewGeneration(t *testing.T) {
	var fail atomic.Bool
	mgr, _ := refreshAdmissionManager(t, func(context.Context) error {
		if fail.Load() {
			return errors.New("temporary listing failure")
		}
		return nil
	})
	fail.Store(true)
	if err := mgr.RefreshTools(t.Context(), "p", "local"); err == nil {
		t.Fatal("listing failure was lost")
	}
	if release, err := mgr.ValidateCatalog(t.Context(), mgr.CatalogRevision()); err != nil {
		t.Fatal("settled failure blocked checkpoint:", err)
	} else {
		release()
	}
	mgr.servers[0].client.catalog.invalidate()
	if release, err := mgr.ValidateCatalog(t.Context(), mgr.CatalogRevision()); !errors.Is(err, tool.ErrCatalogSnapshotStale) {
		if release != nil {
			release()
		}
		t.Fatalf("previous failure settled a new generation: %v", err)
	}
	fail.Store(false)
	if err := mgr.RefreshTools(t.Context(), "p", "local"); err != nil {
		t.Fatal("manual retry failed:", err)
	}
	state := captureRefreshState(mgr)
	if state.dirty || state.refreshErr != nil {
		t.Fatalf("successful retry retained failure: %+v", state)
	}
}
