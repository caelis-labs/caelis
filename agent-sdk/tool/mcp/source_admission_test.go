package mcp

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSourceAdmissionOrdersCapabilityReplacement(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "source", Version: "1"}, &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}}})
	var effects atomic.Int32
	catalogTool(server, "look", "stable", &effects)
	catalogTool(server, "click", "v1", nil)
	mgr := catalogManager(t, server, true)
	current, found, err := mgr.Lookup(t.Context(), "local__look")
	if err != nil || !found {
		t.Fatalf("lookup: %v", err)
	}
	frozen := current.(*MCPTool)
	if err := mgr.RefreshTools(t.Context(), "p", "local"); err != nil {
		t.Fatal(err)
	}
	release, err := frozen.AdmitSource(t.Context())
	if err != nil {
		t.Fatalf("unchanged catalog rejected: %v", err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	catalogTool(server, "click", "v2", nil)
	started, done := make(chan struct{}), make(chan error, 1)
	go func() { close(started); done <- mgr.RefreshTools(t.Context(), "p", "local") }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("catalog replacement crossed source admission: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	release = nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog replacement did not resume after permission consumption")
	}
	fresh, found, err := mgr.Lookup(t.Context(), "local__look")
	if err != nil || !found {
		t.Fatalf("fresh lookup: %v", err)
	}
	if tool.BindingForDefinition(fresh.Definition()) != tool.BindingForDefinition(frozen.Definition()) {
		t.Fatal("fixture changed exact tool binding")
	}
	if guard, err := frozen.AdmitSource(t.Context()); err == nil {
		guard()
		t.Fatal("changed group admitted the frozen source")
	}
	if _, err := frozen.Call(t.Context(), tool.Call{Input: []byte(`{}`)}); err == nil {
		t.Fatal("direct call used the old capability revision")
	}
	if effects.Load() != 0 {
		t.Fatal("stale source reached remote I/O")
	}
	if _, err := fresh.Call(t.Context(), tool.Call{Input: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 {
		t.Fatal("fresh source did not dispatch exactly once")
	}
}
