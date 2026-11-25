package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerConnectionAndFirstListHaveIndependentBudgets(t *testing.T) {
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
			return asyncTestClient(ctx, spec, gate)
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
		const connectionDelay = 20 * time.Second
		failures := make(chan ServerFailure, 1)
		began := time.Now()
		mgr, err := newManager(t.Context(), []ServerSpec{{PluginID: "p", Name: "list"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			time.Sleep(connectionDelay)
			return asyncTestClient(ctx, spec, make(chan struct{}))
		}, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		<-mgr.Initialized()
		if elapsed := time.Since(began); elapsed != connectionDelay+DefaultToolListTimeout {
			t.Fatalf("connection plus listing took %s", elapsed)
		}
		synctest.Wait()
		select {
		case failure := <-failures:
			stage, ok := errors.AsType[*startupError](failure.Err)
			if !ok || stage.Stage != "tools/list" || !errors.Is(failure.Err, context.DeadlineExceeded) || failure.Summary() != "tools/list: deadline exceeded" {
				t.Fatalf("listing failure = %v (%s)", failure.Err, failure.Summary())
			}
		default:
			t.Fatal("missing timeout notification")
		}
		infos := mgr.GetServerInfos("p")
		if len(mgr.Tools()) != 0 || len(infos) != 1 || len(infos[0].Tools) != 0 || !strings.Contains(infos[0].Warning, "tools/list: deadline exceeded") {
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

func TestStdioStartupCancellationReapsUninitializedProcess(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "initialize-received")
	release := filepath.Join(root, "release-child")
	pool := NewConnectionPool(t.Context())
	_, err := pool.NewManager([]ServerSpec{{
		PluginID: "fixture", Name: "stalled", Transport: TransportStdio, Command: os.Args[0], WorkDir: root,
		Args: []string{"-test.run=^TestStalledStdioStartupHelper$"},
		Env:  map[string]string{"CAELIS_STALLED_STDIO_MARKER": marker},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = pool.Close()
	})
	startDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(startDeadline) {
			t.Fatal("stdio child did not receive initialize")
		}
		time.Sleep(10 * time.Millisecond)
	}
	done := make(chan error, 1)
	go func() { done <- pool.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pool cleanup waited on an uninitialized stdio child that ignored stdin EOF")
	}
}

func TestStalledStdioStartupHelper(t *testing.T) {
	marker := os.Getenv("CAELIS_STALLED_STDIO_MARKER")
	if marker == "" {
		return
	}
	if _, err := bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(filepath.Dir(marker), "release-child")
	for {
		if _, err := os.Stat(release); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
