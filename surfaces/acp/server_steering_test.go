package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestServerRoutesSessionSteeringWithoutPromptCallbacks(t *testing.T) {
	t.Parallel()

	agent := &steeringWireAgent{}
	ctx, conn := newTestServerConnection(t, agent)
	request := SessionSteeringRequest{
		SessionID: "session-1",
		Prompt: []json.RawMessage{
			mustMarshalTestRaw(eventstream.TextContent{Type: "text", Text: "adjust the plan"}),
		},
		Meta: map[string]json.RawMessage{
			"steering": json.RawMessage(`{"idleBehavior":"promptRequired","future":42}`),
		},
	}
	response, err := acpsdk.SendRequest[SessionSteeringResponse](conn, ctx, methodSessionSteering, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != SessionSteeringPromptRequired || response.Reason != "noRunningTurn" {
		t.Fatalf("steering response = %#v", response)
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

	ctx, conn := newTestServerConnection(t, commandAgent{})
	_, err := acpsdk.SendRequest[SessionSteeringResponse](
		conn,
		ctx,
		methodSessionSteering,
		SessionSteeringRequest{
			SessionID: "session-1",
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)},
		},
	)
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
		t.Fatalf("steering error = %v, want method not found", err)
	}
}

func TestServerRejectsMalformedSessionSteeringParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params json.RawMessage
	}{
		{name: "wrong prompt type", params: json.RawMessage(`{"sessionId":"session-1","prompt":"hello"}`)},
		{name: "missing session", params: mustMarshalTestRaw(SessionSteeringRequest{Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)}})},
		{name: "empty prompt", params: mustMarshalTestRaw(SessionSteeringRequest{SessionID: "session-1"})},
		{name: "steering options are null", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":null}}`)},
		{name: "idle behavior is not a string", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":true}}}`)},
		{name: "idle behavior has surrounding whitespace", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":" promptRequired "}}}`)},
		{name: "unsupported idle behavior", params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":"startNewTurn"}}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, conn := newTestServerConnection(t, &steeringWireAgent{})
			_, err := acpsdk.SendRequest[SessionSteeringResponse](conn, ctx, methodSessionSteering, tt.params)
			var requestErr *acpsdk.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != -32602 {
				t.Fatalf("steering error = %v, want invalid params", err)
			}
		})
	}
}

func TestServerRejectsSessionSteeringNotification(t *testing.T) {
	t.Parallel()

	agent := &directionSteeringAgent{cancelObserved: make(chan struct{}, 1)}
	ctx, conn := newTestServerConnection(t, agent)
	request := SessionSteeringRequest{
		SessionID: "session-1",
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)},
	}
	if err := conn.SendNotification(ctx, methodSessionSteering, request); err != nil {
		t.Fatal(err)
	}
	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionCancel, acpsdk.CancelNotification{SessionId: "session-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.cancelObserved:
	case <-ctx.Done():
		t.Fatal("timed out waiting for notification barrier")
	}
	if got := agent.steerCalls.Load(); got != 0 {
		t.Fatalf("steering calls after notification = %d, want 0", got)
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
	request SessionSteeringRequest
}

func (a *steeringWireAgent) SteerSession(_ context.Context, request SessionSteeringRequest) (SessionSteeringResponse, error) {
	a.request = request
	return SessionSteeringResponse{
		Outcome: SessionSteeringPromptRequired,
		Reason:  "noRunningTurn",
	}, nil
}

type directionSteeringAgent struct {
	commandAgent
	steerCalls     atomic.Int32
	cancelObserved chan struct{}
}

func (a *directionSteeringAgent) SteerSession(context.Context, SessionSteeringRequest) (SessionSteeringResponse, error) {
	a.steerCalls.Add(1)
	return SessionSteeringResponse{}, nil
}

func (a *directionSteeringAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	a.cancelObserved <- struct{}{}
	return nil
}
