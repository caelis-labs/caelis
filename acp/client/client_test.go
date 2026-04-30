package client

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/acp/jsonrpc"
)

func TestCancelSendsNotification(t *testing.T) {
	var out bytes.Buffer
	client := &Client{conn: jsonrpc.New(nil, &out)}

	if err := client.Cancel(context.Background(), "session-1"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	var msg jsonrpc.Message
	if err := json.Unmarshal(out.Bytes(), &msg); err != nil {
		t.Fatalf("Unmarshal(cancel message) error = %v; payload=%q", err, out.String())
	}
	if msg.ID != nil {
		t.Fatalf("cancel message id = %#v, want notification without id", msg.ID)
	}
	if msg.Method != MethodSessionCancel {
		t.Fatalf("cancel method = %q, want %q", msg.Method, MethodSessionCancel)
	}
	var req CancelRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		t.Fatalf("Unmarshal(cancel params) error = %v", err)
	}
	if req.SessionID != "session-1" {
		t.Fatalf("cancel session id = %q, want session-1", req.SessionID)
	}
}
