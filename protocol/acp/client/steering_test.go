package client

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
)

func TestSteerPartsUsesInteroperableWireContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()

	requests := make(chan SessionSteeringRequest, 1)
	agentConn := jsonrpc.New(clientToAgentReader, agentToClientWriter)
	go func() {
		_ = agentConn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			if msg.Method != MethodSessionSteering {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var request SessionSteeringRequest
			if err := json.Unmarshal(msg.Params, &request); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			requests <- request
			return SessionSteeringResponse{
				Outcome: SessionSteeringPromptRequired,
				Reason:  "noRunningTurn",
			}, nil
		}, nil)
	}()

	acpClient := &Client{conn: jsonrpc.New(agentToClientReader, clientToAgentWriter)}
	go func() {
		_ = acpClient.conn.Serve(ctx, acpClient.handleRequest, acpClient.handleNotification)
	}()
	prompt := []json.RawMessage{
		jsonrpc.MustMarshalRaw(TextContent{Type: "text", Text: "adjust the plan"}),
		jsonrpc.MustMarshalRaw(ImageContent{Type: "image", MimeType: "image/png", Data: "aGVsbG8="}),
	}
	response, err := acpClient.SteerParts(ctx, "session-1", prompt, map[string]json.RawMessage{
		"steering":       json.RawMessage(`{"idleBehavior":"promptRequired"}`),
		"vendor.example": json.RawMessage(`{"delivery":"urgent","sequence":9007199254740993}`),
	})
	if err != nil {
		t.Fatalf("SteerParts() error = %v", err)
	}
	if response.Outcome != SessionSteeringPromptRequired || response.Reason != "noRunningTurn" {
		t.Fatalf("SteerParts() response = %#v", response)
	}
	select {
	case request := <-requests:
		if request.SessionID != "session-1" || len(request.Prompt) != 2 {
			t.Fatalf("steering request = %#v", request)
		}
		if string(request.Meta["steering"]) != `{"idleBehavior":"promptRequired"}` {
			t.Fatalf("steering request _meta = %#v", request.Meta)
		}
		wantVendor := `{"delivery":"urgent","sequence":9007199254740993}`
		if string(request.Meta["vendor.example"]) != wantVendor {
			t.Fatalf("steering request vendor _meta = %s, want %s", request.Meta["vendor.example"], wantVendor)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for steering request")
	}
}

func TestSteerReturnsStructuredRPCError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()

	agentConn := jsonrpc.New(clientToAgentReader, agentToClientWriter)
	go func() {
		_ = agentConn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			return nil, &jsonrpc.RPCError{Code: -32042, Message: "steering rejected", Data: map[string]any{"retryable": false}}
		}, nil)
	}()
	acpClient := &Client{conn: jsonrpc.New(agentToClientReader, clientToAgentWriter)}
	go func() {
		_ = acpClient.conn.Serve(ctx, acpClient.handleRequest, acpClient.handleNotification)
	}()

	_, err := acpClient.Steer(ctx, "session-1", "adjust the plan", nil)
	if code, ok := jsonrpc.ErrorCode(err); !ok || code != -32042 {
		t.Fatalf("Steer() error = %v, code = %d, %v; want -32042", err, code, ok)
	}
}
