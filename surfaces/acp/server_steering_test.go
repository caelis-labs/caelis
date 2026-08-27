package acp

import (
	"context"
	"encoding/json"
	"testing"

	protocolacp "github.com/caelis-labs/caelis/protocol/acp"
)

func TestServerRoutesSessionSteeringWithoutPromptCallbacks(t *testing.T) {
	t.Parallel()

	agent := &steeringWireAgent{}
	conn := &serverConn{agent: agent}
	request := protocolacp.SessionSteeringRequest{
		SessionID: "session-1",
		Prompt: []json.RawMessage{
			mustMarshalTestRaw(protocolacp.TextContent{Type: "text", Text: "adjust the plan"}),
		},
		Meta: map[string]json.RawMessage{
			"steering": json.RawMessage(`{"idleBehavior":"promptRequired","future":42}`),
		},
	}
	result, rpcErr := conn.handleRequest(context.Background(), nil, protocolacp.MethodSessionSteering, mustMarshalTestRaw(request))
	if rpcErr != nil {
		t.Fatalf("steering RPC error = %#v", rpcErr)
	}
	response, ok := result.(protocolacp.SessionSteeringResponse)
	if !ok || response.Outcome != protocolacp.SessionSteeringPromptRequired || response.Reason != "noRunningTurn" {
		t.Fatalf("steering response = %#v", result)
	}
	if agent.request.SessionID != request.SessionID || len(agent.request.Prompt) != 1 {
		t.Fatalf("adapter request = %#v", agent.request)
	}
	if string(agent.request.Meta["steering"]) != `{"idleBehavior":"promptRequired","future":42}` {
		t.Fatalf("adapter request _meta = %#v", agent.request.Meta)
	}
}

func TestServerRejectsSessionSteeringWithoutAdapter(t *testing.T) {
	t.Parallel()

	conn := &serverConn{agent: commandAgent{}}
	_, rpcErr := conn.handleRequest(
		context.Background(),
		nil,
		protocolacp.MethodSessionSteering,
		mustMarshalTestRaw(protocolacp.SessionSteeringRequest{
			SessionID: "session-1",
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)},
		}),
	)
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
		{name: "missing session", params: mustMarshalTestRaw(protocolacp.SessionSteeringRequest{Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)}})},
		{name: "empty prompt", params: mustMarshalTestRaw(protocolacp.SessionSteeringRequest{SessionID: "session-1"})},
		{name: "steering options are null", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":null}}`)},
		{name: "idle behavior is not a string", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":true}}}`)},
		{name: "idle behavior has surrounding whitespace", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":" promptRequired "}}}`)},
		{name: "unsupported idle behavior", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":"startNewTurn"}}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, rpcErr := conn.handleRequest(context.Background(), nil, protocolacp.MethodSessionSteering, tt.params)
			if rpcErr == nil || rpcErr.Code != -32602 {
				t.Fatalf("steering RPC error = %#v, want invalid params", rpcErr)
			}
		})
	}
}

func mustMarshalTestRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

type steeringWireAgent struct {
	commandAgent
	request protocolacp.SessionSteeringRequest
}

func (a *steeringWireAgent) SteerSession(_ context.Context, request protocolacp.SessionSteeringRequest) (protocolacp.SessionSteeringResponse, error) {
	a.request = request
	return protocolacp.SessionSteeringResponse{
		Outcome: protocolacp.SessionSteeringPromptRequired,
		Reason:  "noRunningTurn",
	}, nil
}
