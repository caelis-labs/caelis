package client

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

type testMCPGrant struct {
	mu      sync.Mutex
	session string
	closed  bool
}

func (g *testMCPGrant) Bind(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.session = id
	return nil
}
func (g *testMCPGrant) Close() { g.mu.Lock(); defer g.mu.Unlock(); g.closed = true }
func (g *testMCPGrant) Valid() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.session != "" && !g.closed
}

func TestMCPGrantBindsToReturnedSessionAndDiesWithConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	local, peer := net.Pipe()
	defer peer.Close()
	grant := &testMCPGrant{}
	c, err := NewStreamClient(local, local, Config{MCPGrant: grant})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(peer)
		if !scanner.Scan() {
			done <- scanner.Err()
			return
		}
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			done <- err
			return
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"sessionId": "remote-session"}})
		_, err := peer.Write(append(body, '\n'))
		done <- err
	}()
	if c.CollaborationReady() {
		t.Fatal("unbound grant ready")
	}
	result, err := c.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "remote-session" || !c.CollaborationReady() {
		t.Fatal("grant did not bind")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err = c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if c.CollaborationReady() {
		t.Fatal("closed connection authorized")
	}
}
