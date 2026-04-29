package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp/jsonrpc"
)

func TestServeStdioSendsAvailableCommandsAfterNewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, commandAgent{}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan availableCommandsNotification, 1)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != MethodSessionUpdate {
				return
			}
			var notification availableCommandsNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification
			}
		})
	}()

	var resp NewSessionResponse
	if err := conn.Call(ctx, MethodSessionNew, NewSessionRequest{CWD: t.TempDir()}, &resp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	if resp.SessionID != "session-1" {
		t.Fatalf("SessionID = %q, want session-1", resp.SessionID)
	}
	select {
	case got := <-updates:
		if got.SessionID != "session-1" {
			t.Fatalf("notification sessionId = %q, want session-1", got.SessionID)
		}
		if got.Update.SessionUpdate != UpdateAvailableCmds {
			t.Fatalf("sessionUpdate = %q, want %q", got.Update.SessionUpdate, UpdateAvailableCmds)
		}
		if len(got.Update.AvailableCommands) != 1 || got.Update.AvailableCommands[0].Name != "agent" {
			t.Fatalf("availableCommands = %#v, want agent command", got.Update.AvailableCommands)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for available_commands_update")
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}

func TestPromptCallbacksCallClientTerminalMethods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, terminalClientAgent{}, clientToServerReader, serverToClientWriter)
	}()

	methods := make(chan string, 8)
	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	go func() {
		_ = conn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			methods <- msg.Method
			switch msg.Method {
			case MethodTerminalCreate:
				var req CreateTerminalRequest
				if err := json.Unmarshal(msg.Params, &req); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				if req.SessionID != "session-1" || req.Command != "npm" || len(req.Args) != 1 || req.Args[0] != "test" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: fmt.Sprintf("unexpected terminal/create request: %#v", req)}
				}
				return CreateTerminalResponse{TerminalID: "term-1"}, nil
			case MethodTerminalOutput:
				return TerminalOutputResponse{
					Output:     "ok\n",
					Truncated:  false,
					ExitStatus: &TerminalExitStatus{ExitCode: intPtr(0)},
				}, nil
			case MethodTerminalWaitForExit:
				return TerminalWaitForExitResponse{ExitCode: intPtr(0)}, nil
			case MethodTerminalRelease:
				return struct{}{}, nil
			default:
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
		}, nil)
	}()

	var initResp InitializeResponse
	if err := conn.Call(ctx, MethodInitialize, InitializeRequest{
		ClientCapabilities: map[string]any{"terminal": true},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	var resp PromptResponse
	if err := conn.Call(ctx, MethodSessionPrompt, PromptRequest{SessionID: "session-1"}, &resp); err != nil {
		t.Fatalf("session/prompt call error = %v", err)
	}
	if resp.StopReason != StopReasonEndTurn {
		t.Fatalf("stopReason = %q, want %q", resp.StopReason, StopReasonEndTurn)
	}
	for _, want := range []string{MethodTerminalCreate, MethodTerminalOutput, MethodTerminalWaitForExit, MethodTerminalRelease} {
		select {
		case got := <-methods:
			if got != want {
				t.Fatalf("client callback method = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", want)
		}
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}

type availableCommandsNotification struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate     string             `json:"sessionUpdate"`
		AvailableCommands []AvailableCommand `json:"availableCommands"`
	} `json:"update"`
}

type commandAgent struct{}

func (commandAgent) Initialize(context.Context, InitializeRequest) (InitializeResponse, error) {
	return InitializeResponse{}, nil
}

func (commandAgent) Authenticate(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
	return AuthenticateResponse{}, nil
}

func (commandAgent) NewSession(context.Context, NewSessionRequest) (NewSessionResponse, error) {
	return NewSessionResponse{SessionID: "session-1"}, nil
}

func (commandAgent) Prompt(context.Context, PromptRequest, PromptCallbacks) (PromptResponse, error) {
	return PromptResponse{}, nil
}

func (commandAgent) Cancel(context.Context, CancelNotification) error {
	return nil
}

func (commandAgent) AvailableCommands(context.Context, string) ([]AvailableCommand, error) {
	return []AvailableCommand{{
		Name:        "agent",
		Description: "Manage ACP agents",
		Input:       &AvailableCommandInput{Hint: "use|add|install|list|remove"},
	}}, nil
}

type terminalClientAgent struct {
	commandAgent
}

func (terminalClientAgent) Prompt(ctx context.Context, req PromptRequest, cb PromptCallbacks) (PromptResponse, error) {
	terminal, ok := AsTerminalClientCallbacks(cb)
	if !ok {
		return PromptResponse{}, fmt.Errorf("terminal client callbacks unavailable")
	}
	created, err := terminal.CreateTerminal(ctx, CreateTerminalRequest{
		SessionID: req.SessionID,
		Command:   "npm",
		Args:      []string{"test"},
		CWD:       "/tmp/project",
	})
	if err != nil {
		return PromptResponse{}, err
	}
	output, err := terminal.TerminalOutput(ctx, TerminalOutputRequest{
		SessionID:  req.SessionID,
		TerminalID: created.TerminalID,
	})
	if err != nil {
		return PromptResponse{}, err
	}
	if output.Output != "ok\n" {
		return PromptResponse{}, fmt.Errorf("terminal output = %q, want ok", output.Output)
	}
	wait, err := terminal.TerminalWaitForExit(ctx, TerminalWaitForExitRequest{
		SessionID:  req.SessionID,
		TerminalID: created.TerminalID,
	})
	if err != nil {
		return PromptResponse{}, err
	}
	if wait.ExitCode == nil || *wait.ExitCode != 0 {
		return PromptResponse{}, fmt.Errorf("terminal wait = %#v, want exit code 0", wait)
	}
	if err := terminal.TerminalRelease(ctx, TerminalReleaseRequest{
		SessionID:  req.SessionID,
		TerminalID: created.TerminalID,
	}); err != nil {
		return PromptResponse{}, err
	}
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

func intPtr(v int) *int {
	return &v
}
