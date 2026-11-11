package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerConnectionAndFirstListHaveIndependentBudgets(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "catalog"}[refresh], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				failures := make(chan ServerFailure, 1)
				mgr, err := newManager(t.Context(), []ServerSpec{{PluginID: "p", Name: "slow-directory"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
					time.Sleep(2 * time.Second)
					gate := make(chan struct{})
					go func() {
						select {
						case <-time.After(29 * time.Second):
							close(gate)
						case <-t.Context().Done():
						}
					}()
					client, err := asyncTestClient(ctx, spec, gate)
					if err == nil && refresh {
						client.catalog = newClientCatalog()
					}
					return client, err
				}, func(f ServerFailure) { failures <- f })
				if err != nil {
					t.Fatal(err)
				}
				defer mgr.Close()
				<-mgr.Initialized()
				if len(failures) != 0 || len(mgr.Tools()) != 1 {
					t.Fatalf("directory lost its own budget: failures=%d tools=%d elapsed=%s", len(failures), len(mgr.Tools()), time.Since(began))
				}
				if got := time.Since(began); got != 31*time.Second {
					t.Fatalf("elapsed=%s", got)
				}
			})
		})
	}
}

func TestManagerParentDeadlineStillBoundsBothStages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var client *Client
		failures := make(chan ServerFailure, 1)
		mgr, err := newManager(parent, []ServerSpec{{PluginID: "p", Name: "cancel-list"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			var err error
			client, err = asyncTestClient(ctx, spec, make(chan struct{}))
			return client, err
		}, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		<-mgr.Initialized()
		if len(failures) != 0 || len(mgr.Tools()) != 0 {
			t.Fatal("parent cancellation published a notice or tools")
		}
		if !errors.Is(parent.Err(), context.DeadlineExceeded) {
			t.Fatal(parent.Err())
		}
		select {
		case <-client.closed:
		default:
			t.Fatal("parent cancellation did not close client")
		}
	})
}

func TestManagerFirstCatalogListTimeoutHasIndependentPhase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failures := make(chan ServerFailure, 1)
		began := time.Now()
		mgr, err := newManager(t.Context(), []ServerSpec{{PluginID: "p", Name: "list"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			client, err := asyncTestClient(ctx, spec, make(chan struct{}))
			if err == nil {
				client.catalog = newClientCatalog()
			}
			return client, err
		}, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		<-mgr.Initialized()
		failure := <-failures
		if got := time.Since(began); got != DefaultToolListTimeout {
			t.Fatal(got)
		}
		if !errors.Is(failure.Err, context.DeadlineExceeded) || failure.Summary() != "tools/list: deadline exceeded" {
			t.Fatal(failure.Summary())
		}
		infos := mgr.GetServerInfos("p")
		if len(mgr.Tools()) != 0 || len(infos) != 1 || !strings.Contains(infos[0].Warning, "tools/list: deadline exceeded") {
			t.Fatalf("status=%#v", infos)
		}
	})
}

func TestStartupInitializeDeadlineClosesHTTPConnection(t *testing.T) {
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "notifications/cancelled" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer func() { close(stop); server.Close() }()
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	sdk := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	startup := &startupTransport{Transport: &mcpsdk.StreamableClientTransport{Endpoint: server.URL, DisableStandaloneSSE: true}}
	_, err := connectWithTimeout(t.Context(), sdk, lifetime, cancel, startup, 200*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !startup.connected.Load() {
		t.Fatal("initialize deadline did not cancel connection")
	}
	if got := startupFailureSummary(&startupError{Stage: "initialize", Err: err}); got != "initialize: deadline exceeded" {
		t.Fatal(got)
	}
}
