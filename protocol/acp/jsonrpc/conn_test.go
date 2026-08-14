package jsonrpc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFormatRPCErrorPreservesStructuredCode(t *testing.T) {
	err := FormatRPCError(&RPCError{
		Code:    -32000,
		Message: "Authentication required",
		Data:    map[string]any{"reason": "auth_required"},
	})
	if code, ok := ErrorCode(err); !ok || code != -32000 {
		t.Fatalf("ErrorCode() = %d, %v; want -32000, true", code, ok)
	}
	if got, want := err.Error(), `acp rpc error -32000: Authentication required (data: {"reason":"auth_required"})`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCallKeepsLocalConnectionFailureOutOfPeerErrorCodes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agentToClientReader, agentToClientWriter := io.Pipe()
	clientToAgentReader, clientToAgentWriter := io.Pipe()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()

	conn := New(agentToClientReader, clientToAgentWriter)
	go func() {
		_ = conn.Serve(ctx, nil, nil)
	}()
	result := make(chan error, 1)
	go func() {
		result <- conn.Call(ctx, "session/new", map[string]any{}, nil)
	}()
	if _, err := bufio.NewReader(clientToAgentReader).ReadBytes('\n'); err != nil {
		t.Fatalf("read request: %v", err)
	}
	if err := agentToClientWriter.Close(); err != nil {
		t.Fatalf("close agent output: %v", err)
	}
	err := <-result
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Call() error = %v, want EOF transport cause", err)
	}
	if code, ok := ErrorCode(err); ok {
		t.Fatalf("ErrorCode(local failure) = %d, true; want no peer code", code)
	}
	if !strings.Contains(err.Error(), "connection closed before response") {
		t.Fatalf("Call() error = %q, want transport context", err)
	}
}
