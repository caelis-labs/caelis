package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	protocolacp "github.com/caelis-labs/caelis/protocol/acp/schema"
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

	agent := &permissionWireAgent{response: make(chan protocolacp.RequestPermissionResponse, 1)}
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
		if response.Outcome.Outcome != "selected" || response.Outcome.OptionID != "allow-once" {
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
	response chan protocolacp.RequestPermissionResponse
}

func (a *permissionWireAgent) Prompt(ctx context.Context, request acpsdk.PromptRequest, callbacks PromptCallbacks) (acpsdk.PromptResponse, error) {
	response, err := callbacks.RequestPermission(ctx, protocolacp.RequestPermissionRequest{
		SessionID: string(request.SessionId),
		ToolCall: protocolacp.ToolCallUpdate{
			ToolCallID: "call-1",
			Content: []protocolacp.ToolCallContent{{
				Type: "content", Content: protocolacp.TextContent{Type: "text", Text: "permission detail"},
			}},
		},
		Options: []protocolacp.PermissionOption{{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"}},
	})
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	a.response <- response
	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}

func TestPermissionRequestToSDKPreservesStandardWire(t *testing.T) {
	oldText := "before"
	title := "Run command"
	kind := protocolacp.ToolKindExecute
	status := protocolacp.ToolStatusPending
	request, err := permissionRequestToSDK(protocolacp.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         &title,
			Kind:          &kind,
			Status:        &status,
			RawInput:      map[string]any{"command": "go test ./..."},
			Content: []protocolacp.ToolCallContent{
				{Type: "content", Content: protocolacp.TextContent{Type: "text", Text: "detail"}},
				{Type: "diff", Path: "/tmp/file", OldText: &oldText, NewText: "after"},
				{Type: "terminal", TerminalID: "terminal-1"},
			},
			Meta: map[string]any{"tool": "kept"},
		},
		Options: []protocolacp.PermissionOption{{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"}},
		Meta:    map[string]any{"request": "kept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.SessionId != "session-1" || request.ToolCall.ToolCallId != "call-1" || len(request.Options) != 1 {
		t.Fatalf("SDK request = %#v", request)
	}
	if request.Options[0].OptionId != "allow-once" || request.Options[0].Kind != acpsdk.PermissionOptionKindAllowOnce {
		t.Fatalf("SDK options = %#v", request.Options)
	}
	if len(request.ToolCall.Content) != 3 || request.ToolCall.Content[0].Content == nil || request.ToolCall.Content[1].Diff == nil || request.ToolCall.Content[2].Terminal == nil {
		t.Fatalf("SDK content = %#v", request.ToolCall.Content)
	}
	if string(request.Meta["request"]) != `"kept"` || string(request.ToolCall.Meta["tool"]) != `"kept"` {
		t.Fatalf("SDK metadata = request %#v tool %#v", request.Meta, request.ToolCall.Meta)
	}
}

func TestPermissionRequestToSDKRejectsNonStandardContent(t *testing.T) {
	_, err := permissionRequestToSDK(protocolacp.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: protocolacp.ToolCallUpdate{
			ToolCallID: "call-1",
			Content:    []protocolacp.ToolCallContent{{Type: "vendor"}},
		},
		Options: []protocolacp.PermissionOption{},
	})
	if err == nil {
		t.Fatal("permissionRequestToSDK() error = nil")
	}
}

func TestPermissionRequestToSDKRejectsMissingOptions(t *testing.T) {
	_, err := permissionRequestToSDK(protocolacp.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall:  protocolacp.ToolCallUpdate{ToolCallID: "call-1"},
	})
	if err == nil {
		t.Fatal("permissionRequestToSDK() error = nil")
	}
}

func TestPermissionResponseFromSDKMapsSelectedAndCancelled(t *testing.T) {
	selected, err := permissionResponseFromSDK(acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{Selected: &acpsdk.RequestPermissionOutcomeSelected{
			Outcome: "selected", OptionId: "allow-once",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Outcome.Outcome != "selected" || selected.Outcome.OptionID != "allow-once" {
		t.Fatalf("selected response = %#v", selected)
	}
	cancelled, err := permissionResponseFromSDK(acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Outcome.Outcome != "cancelled" || cancelled.Outcome.OptionID != "" {
		t.Fatalf("cancelled response = %#v", cancelled)
	}
}

func TestPermissionResponseFromSDKRejectsMissingOutcome(t *testing.T) {
	if _, err := permissionResponseFromSDK(acpsdk.RequestPermissionResponse{}); err == nil {
		t.Fatal("permissionResponseFromSDK() error = nil")
	}
}
