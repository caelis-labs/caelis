package acp

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

// Exercise the product ServeStdio adapter: its AfterResponse command updates
// must follow the complete batch, including the zero-delay resume path.
func TestServeStdioBatchResumesBeforeAvailableCommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	server, peer := net.Pipe()
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- ServeStdio(ctx, batchResumeAgent{}, server, server)
	}()
	t.Cleanup(func() {
		cancel()
		_ = peer.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("ServeStdio did not stop")
		}
	})

	cwd := t.TempDir()
	requests := []map[string]any{
		{"jsonrpc": "2.0", "id": "resume-1", "method": "session/resume", "params": acpsdk.ResumeSessionRequest{SessionId: "session-1", Cwd: cwd, McpServers: []acpsdk.McpServer{}}},
		{"jsonrpc": "2.0", "id": "resume-2", "method": "session/resume", "params": acpsdk.ResumeSessionRequest{SessionId: "session-2", Cwd: cwd, McpServers: []acpsdk.McpServer{}}},
	}
	if err := json.NewEncoder(peer).Encode(requests); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(peer)
	var responses []struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := decoder.Decode(&responses); err != nil {
		t.Fatalf("first frame must be the complete response array: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("batch responses = %#v", responses)
	}
	seen := make(map[string]bool)
	for _, response := range responses {
		if response.JSONRPC != "2.0" || len(response.Error) != 0 || len(response.Result) == 0 || (response.ID != "resume-1" && response.ID != "resume-2") || seen[response.ID] {
			t.Fatalf("unexpected batch response: %#v", response)
		}
		seen[response.ID] = true
	}
	seen = make(map[string]bool)
	for range 2 {
		var notification struct {
			Method string                     `json:"method"`
			Params acpsdk.SessionNotification `json:"params"`
		}
		if err := decoder.Decode(&notification); err != nil {
			t.Fatal(err)
		}
		id := string(notification.Params.SessionId)
		update := notification.Params.Update.AvailableCommandsUpdate
		if notification.Method != acpsdk.ClientMethodSessionUpdate || (id != "session-1" && id != "session-2") || seen[id] || update == nil || len(update.AvailableCommands) != 1 || update.AvailableCommands[0].Name != "agent" {
			t.Fatalf("unexpected command update: %#v", notification)
		}
		seen[id] = true
	}
}

type batchResumeAgent struct{ commandAgent }

func (batchResumeAgent) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	return acpsdk.ResumeSessionResponse{}, nil
}
