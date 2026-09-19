package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCollaborationStdioDiscoversConfiguredRolesBeforeGrantBinding(t *testing.T) {
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "grant is not bound", http.StatusUnauthorized)
	}))
	defer host.Close()
	definitions := append(collaboration.Definitions(true), spawn.New([]delegation.Agent{{Name: "research", Description: "Investigate unfamiliar systems."}}).Definition())
	raw, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAELIS_COLLABORATION_URL", host.URL)
	t.Setenv("CAELIS_COLLABORATION_TOKEN", "pending-test-grant")
	t.Setenv("CAELIS_COLLABORATION_TOOLS", string(raw))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	defer serverIn.Close()
	defer clientOut.Close()
	defer clientIn.Close()
	defer serverOut.Close()
	done := make(chan error, 1)
	go func() { done <- runCollaboration(ctx, []string{"mcp", "--stdio"}, serverIn, serverOut) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range tools.Tools {
		if definition.Name == spawn.ToolName {
			found = true
			schema, err := json.Marshal(definition.InputSchema)
			if err != nil || !strings.Contains(string(schema), `"enum":["research"]`) || !strings.Contains(string(schema), "research: Investigate unfamiliar systems.") {
				t.Fatalf("configured roles missing from MCP tools/list: %s (%v)", schema, err)
			}
		}
	}
	if !found || calls.Load() != 0 {
		t.Fatalf("discovery required live authority: StartThread=%v, HTTP calls=%d", found, calls.Load())
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: spawn.ToolName, Arguments: map[string]any{"agent": "research", "prompt": "work"}})
	if err != nil || !result.IsError || calls.Load() != 1 {
		t.Fatalf("schema bypassed grant authorization: %#v %v, HTTP calls=%d", result, err, calls.Load())
	}
	_ = session.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
