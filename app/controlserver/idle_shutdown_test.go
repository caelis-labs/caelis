package controlserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestIdleShutdownFencesMutationsButNotObservers(t *testing.T) {
	var prepared atomic.Int32
	var busy atomic.Bool
	stopped := make(chan struct{}, 1)
	server, err := New(HandlerConfig{
		Services:      testAppServerServices(&fakeService{}, staticStatusService{}),
		Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"},
		ServerInfo: appserver.ServerInfo{InstanceID: "original-host"},
		Shutdown:   func() { stopped <- struct{}{} },
		PrepareIdleShutdown: func(context.Context) error {
			prepared.Add(1)
			if busy.Load() {
				return errorcode.New(errorcode.Conflict, "busy")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mutationEntered, releaseMutation := make(chan struct{}), make(chan struct{})
	observerEntered, releaseObserver := make(chan struct{}), make(chan struct{})
	server.mux.HandleFunc("POST /test-mutation", func(w http.ResponseWriter, r *http.Request) { close(mutationEntered); <-releaseMutation })
	server.mux.HandleFunc("GET /test-observer", func(w http.ResponseWriter, r *http.Request) { close(observerEntered); <-releaseObserver })
	request := func(method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		authorizeTestRequest(r)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	shutdown := func(instance string) int {
		return request("POST", apiPrefix+"/host/shutdown", `{"expected_instance_id":"`+instance+`","if_idle":true}`)
	}
	doneMutation := make(chan struct{})
	go func() { request("POST", "/test-mutation", ""); close(doneMutation) }()
	<-mutationEntered
	if got := shutdown("original-host"); got != http.StatusConflict {
		t.Fatalf("mutation not fenced: %d", got)
	}
	if prepared.Load() != 0 {
		t.Fatal("Runtime admission closed with mutation in flight")
	}
	close(releaseMutation)
	<-doneMutation
	busy.Store(true)
	if got := shutdown("original-host"); got != http.StatusConflict {
		t.Fatalf("busy Host stopped: %d", got)
	}
	busy.Store(false)
	if got := shutdown("different-host"); got != http.StatusConflict {
		t.Fatalf("wrong instance stopped: %d", got)
	}
	if got := request("POST", apiPrefix+"/host/shutdown", `{"if_idle":true}`); got != http.StatusBadRequest {
		t.Fatalf("unfenced idle request: %d", got)
	}
	doneObserver := make(chan struct{})
	go func() { request("GET", "/test-observer", ""); close(doneObserver) }()
	<-observerEntered
	if got := shutdown("original-host"); got != http.StatusOK {
		t.Fatalf("idle observer pinned Host: %d", got)
	}
	<-stopped
	if got := request("POST", "/test-mutation", ""); got != http.StatusServiceUnavailable {
		t.Fatalf("mutation entered draining Host: %d", got)
	}
	close(releaseObserver)
	<-doneObserver
}

func TestIdleShutdownRequiresLifecycleCapability(t *testing.T) {
	server, err := New(HandlerConfig{Services: testAppServerServices(&fakeService{}, staticStatusService{}), Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"}, ServerInfo: appserver.ServerInfo{InstanceID: "host"}, Shutdown: func() { t.Error("unsupported conditional shutdown became force stop") }})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", apiPrefix+"/host/shutdown", strings.NewReader(`{"expected_instance_id":"host","if_idle":true}`))
	authorizeTestRequest(r)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("conditional shutdown without lifecycle: %d", w.Code)
	}
}
