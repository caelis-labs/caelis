package client

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestReplayBoundaryOrdersLiveOutputAcrossIgnoredExtensionBurst(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clientSide, peerSide := net.Pipe()
	defer clientSide.Close()
	defer peerSide.Close()
	updates := make(chan string, 4)
	c, err := NewStreamClient(clientSide, clientSide, Config{OnUpdate: func(env UpdateEnvelope) { updates <- env.SessionID }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		decoder, encoder := json.NewDecoder(peerSide), json.NewEncoder(peerSide)
		if err := decoder.Decode(&request); err != nil {
			written <- err
			return
		}
		update := func(id string) error {
			return encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": MethodSessionUpdate, "params": map[string]any{"sessionId": id, "update": map[string]any{"sessionUpdate": UpdateAgentMessage, "content": map[string]any{"type": "text", "text": "delta"}}}})
		}
		if err := update("history"); err != nil {
			written <- err
			return
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}); err != nil {
			written <- err
			return
		}
		for range 10000 {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "_unused/session/update", "params": map[string]any{"ignored": true}}); err != nil {
				written <- err
				return
			}
		}
		written <- update("live")
	}()
	done := make(chan error, 1)
	go func() {
		_, err := c.LoadSessionWithReplayEnd(ctx, "child", "/tmp", nil, func(context.Context) error { close(entered); <-release; return nil })
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case id := <-updates:
		if id != "history" {
			t.Fatalf("before replay end=%s", id)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case id := <-updates:
		t.Fatalf("later notification crossed replay boundary: %s", id)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case id := <-updates:
		if id != "live" {
			t.Fatalf("after replay end=%s", id)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
