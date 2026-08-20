package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
)

func TestServerRoutesSessionSteeringWithoutPromptCallbacks(t *testing.T) {
	t.Parallel()

	agent := &steeringWireAgent{}
	conn := &serverConn{agent: agent}
	request := SessionSteeringRequest{
		SessionID: "session-1",
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(TextContent{Type: "text", Text: "adjust the plan"}),
		},
		Meta: map[string]json.RawMessage{
			"steering": json.RawMessage(`{"idleBehavior":"promptRequired"}`),
		},
	}
	result, rpcErr := conn.handleRequest(context.Background(), jsonrpc.Message{
		Method: MethodSessionSteering,
		Params: jsonrpc.MustMarshalRaw(request),
	})
	if rpcErr != nil {
		t.Fatalf("steering RPC error = %#v", rpcErr)
	}
	response, ok := result.(SessionSteeringResponse)
	if !ok || response.Outcome != SessionSteeringPromptRequired || response.Reason != "noRunningTurn" {
		t.Fatalf("steering response = %#v", result)
	}
	if agent.request.SessionID != request.SessionID || len(agent.request.Prompt) != 1 {
		t.Fatalf("adapter request = %#v", agent.request)
	}
	if string(agent.request.Meta["steering"]) != `{"idleBehavior":"promptRequired"}` {
		t.Fatalf("adapter request _meta = %#v", agent.request.Meta)
	}
}

func TestServerRejectsSessionSteeringWithoutAdapter(t *testing.T) {
	t.Parallel()

	conn := &serverConn{agent: commandAgent{}}
	_, rpcErr := conn.handleRequest(context.Background(), jsonrpc.Message{
		Method: MethodSessionSteering,
		Params: jsonrpc.MustMarshalRaw(SessionSteeringRequest{
			SessionID: "session-1",
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)},
		}),
	})
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("steering RPC error = %#v, want method not found", rpcErr)
	}
}

func TestServerRejectsMalformedSessionSteeringParams(t *testing.T) {
	t.Parallel()

	conn := &serverConn{agent: &steeringWireAgent{}}
	tests := []struct {
		name   string
		params json.RawMessage
	}{
		{name: "wrong prompt type", params: json.RawMessage(`{"sessionId":"session-1","prompt":"hello"}`)},
		{name: "missing session", params: jsonrpc.MustMarshalRaw(SessionSteeringRequest{Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)}})},
		{name: "empty prompt", params: jsonrpc.MustMarshalRaw(SessionSteeringRequest{SessionID: "session-1"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, rpcErr := conn.handleRequest(context.Background(), jsonrpc.Message{
				Method: MethodSessionSteering,
				Params: tt.params,
			})
			if rpcErr == nil || rpcErr.Code != -32602 {
				t.Fatalf("steering RPC error = %#v, want invalid params", rpcErr)
			}
		})
	}
}

type steeringWireAgent struct {
	commandAgent
	request SessionSteeringRequest
}

func (a *steeringWireAgent) SteerSession(_ context.Context, request SessionSteeringRequest) (SessionSteeringResponse, error) {
	a.request = request
	return SessionSteeringResponse{
		Outcome: SessionSteeringPromptRequired,
		Reason:  "noRunningTurn",
	}, nil
}
