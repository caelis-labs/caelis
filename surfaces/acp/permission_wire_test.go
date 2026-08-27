package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestServeStdioUsesSDKPermissionWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	agent := &permissionWireAgent{response: make(chan acpsdk.RequestPermissionResponse, 1)}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, agent, clientToServerReader, serverToClientWriter)
	}()
	wireRequest := make(chan acpsdk.RequestPermissionRequest, 1)
	conn, err := acpsdk.NewConnectionWithOptions(
		func(_ context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
			if method != acpsdk.ClientMethodSessionRequestPermission {
				return nil, acpsdk.NewMethodNotFound(method)
			}
			var request acpsdk.RequestPermissionRequest
			if err := json.Unmarshal(params, &request); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			wireRequest <- request
			return acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{Selected: &acpsdk.RequestPermissionOutcomeSelected{
					Outcome: "selected", OptionId: "allow-once",
				}},
			}, nil
		},
		clientToServerWriter,
		serverToClientReader,
		acpsdk.ConnectionOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := acpsdk.SendRequest[acpsdk.PromptResponse](conn, ctx, acpsdk.AgentMethodSessionPrompt, testPromptRequest("session-1")); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-wireRequest:
		if request.SessionId != "session-1" || request.ToolCall.ToolCallId != "call-1" || len(request.Options) != 1 || request.Options[0].Kind != acpsdk.PermissionOptionKindAllowOnce {
			t.Fatalf("wire request = %#v", request)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for SDK permission request")
	}
	select {
	case response := <-agent.response:
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow-once" {
			t.Fatalf("normalized response = %#v", response)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for normalized permission response")
	}

	cancel()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("ServeStdio did not stop")
	}
}

type permissionWireAgent struct {
	commandAgent
	response chan acpsdk.RequestPermissionResponse
}

func (a *permissionWireAgent) Prompt(ctx context.Context, request acpsdk.PromptRequest, callbacks PromptCallbacks) (acpsdk.PromptResponse, error) {
	response, err := callbacks.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
		SessionId: request.SessionId,
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Content:    []acpsdk.ToolCallContent{acpsdk.ToolContent(acpsdk.TextBlock("permission detail"))},
		},
		Options: []acpsdk.PermissionOption{{OptionId: "allow-once", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce}},
	})
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	a.response <- response
	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}
