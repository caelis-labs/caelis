package mcp

import (
	"context"
	"errors"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCatalogSnapshotGuardOrdersIdentityChanges(t *testing.T) {
	for _, change := range []string{"notification", "revocation", "connection_loss"} {
		t.Run(change, func(t *testing.T) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "guard", Version: "1"}, nil)
			catalogTool(server, "hidden", "before", nil)
			mgr := catalogManager(t, server, true)
			revision := mgr.CatalogRevision()
			release, err := mgr.ValidateCatalog(t.Context(), revision)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if unlock, err := mgr.ValidateCatalog(t.Context(), revision); unlock != nil || !errors.Is(err, tool.ErrCatalogSnapshotStale) {
				t.Fatalf("contended snapshot admitted: %v", err)
			}
			started, changed := make(chan struct{}), make(chan struct{})
			go func() {
				close(started)
				switch change {
				case "notification":
					mgr.servers[0].client.catalog.invalidate()
				case "revocation":
					_ = mgr.RevokeServer("p", "local")
				case "connection_loss":
					mgr.servers[0].client.markFailed(errors.New("injected connection loss"))
				}
				close(changed)
			}()
			<-started
			select {
			case <-changed:
				t.Fatal("identity update crossed checkpoint guard")
			case <-time.After(20 * time.Millisecond):
			}
			release()
			release() // The owner can safely defer an already-released guard.
			select {
			case <-changed:
			case <-time.After(10 * time.Second):
				t.Fatal("identity update remained blocked after checkpoint")
			}
			if unlock, err := mgr.ValidateCatalog(t.Context(), revision); unlock != nil || !errors.Is(err, tool.ErrCatalogSnapshotStale) {
				t.Fatalf("old candidate survived %s: %v", change, err)
			}
		})
	}
}

func TestCatalogSnapshotGuardRejectsSchemaChangesDirtyAndCanceled(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "guard", Version: "1"}, nil)
	catalogTool(server, "hidden", "before", nil)
	mgr := catalogManager(t, server, true)
	revision := mgr.CatalogRevision()
	catalogTool(server, "hidden", "after", nil)
	awaitCatalog(t, func() bool { return revision != mgr.CatalogRevision() && catalogClean(mgr) })
	if unlock, err := mgr.ValidateCatalog(t.Context(), revision); unlock != nil || !errors.Is(err, tool.ErrCatalogSnapshotStale) {
		t.Fatalf("hidden schema change did not invalidate candidate: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if unlock, err := mgr.ValidateCatalog(ctx, mgr.CatalogRevision()); unlock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled guard: %v", err)
	}
	c := mgr.servers[0].client.catalog
	c.mu.Lock()
	c.dirty = true // A refresh is in progress without a settled failure.
	c.mu.Unlock()
	if unlock, err := mgr.ValidateCatalog(t.Context(), mgr.CatalogRevision()); unlock != nil || !errors.Is(err, tool.ErrCatalogSnapshotStale) {
		t.Fatalf("uncertain directory admitted: %v", err)
	}
}

func TestCatalogSnapshotGuardWithoutRefresh(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "static", Version: "1"}, nil)
	catalogTool(server, "read", "static schema", nil)
	mgr := catalogManager(t, server, false)
	revision := mgr.CatalogRevision()
	release, err := mgr.ValidateCatalog(t.Context(), revision)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() { changed <- mgr.RevokeServer("p", "local") }()
	select {
	case err := <-changed:
		release()
		t.Fatalf("revocation crossed static checkpoint: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	if unlock, err := mgr.ValidateCatalog(t.Context(), revision); err == nil {
		unlock()
		t.Fatal("revocation retained static revision")
	}
	if err := mgr.RefreshTools(t.Context(), "p", "local"); err == nil {
		t.Fatal("validation silently enabled refresh")
	}
}

func TestCatalogSnapshotGuardAcceptsSettledFailureWithoutAdmittingTools(t *testing.T) {
	for _, failure := range []string{"refresh", "connection_loss", "static_connection_loss"} {
		t.Run(failure, func(t *testing.T) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "failed", Version: "1"}, nil)
			catalogTool(server, "read", "before", nil)
			var fail atomic.Bool
			server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
				return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
					if method == "tools/list" && fail.Load() {
						return nil, errors.New("listing unavailable")
					}
					return next(ctx, method, req)
				}
			})
			mgr := catalogManager(t, server, failure != "static_connection_loss")
			original := mgr.CatalogRevision()
			pinned := mgr.Tools()[0]
			if failure == "refresh" {
				fail.Store(true)
				if err := mgr.RefreshTools(t.Context(), "p", "local"); err == nil {
					t.Fatal("failure was ignored")
				}
			} else {
				mgr.servers[0].client.markFailed(errors.New("connection lost"))
			}
			if release, err := mgr.ValidateCatalog(t.Context(), original); err == nil {
				release()
				t.Fatal("old authority survived failure")
			}
			release, err := mgr.ValidateCatalog(t.Context(), mgr.CatalogRevision())
			if err != nil {
				t.Fatal("settled unavailable directory blocks unrelated checkpoint:", err)
			}
			release()
			if result, err := pinned.Call(t.Context(), tool.Call{ID: "blocked", Input: []byte(`{}`)}); err == nil && !result.IsError {
				t.Fatal("unavailable tool admitted")
			}
		})
	}
}
