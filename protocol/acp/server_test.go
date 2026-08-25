package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
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

	type observedUpdate struct {
		notification availableCommandsNotification
	}
	updates := make(chan observedUpdate, 1)
	conn, err := acpsdk.NewConnectionWithOptions(
		func(_ context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
			if method != MethodSessionUpdate {
				return nil, acpsdk.NewMethodNotFound(method)
			}
			var notification availableCommandsNotification
			if err := json.Unmarshal(params, &notification); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			updates <- observedUpdate{notification: notification}
			return nil, nil
		},
		clientToServerWriter,
		serverToClientReader,
		acpsdk.ConnectionOptions{},
	)
	if err != nil {
		t.Fatalf("new client connection error = %v", err)
	}
	defer conn.Close()

	resp, err := acpsdk.SendRequest[NewSessionResponse](conn, ctx, MethodSessionNew, NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	if resp.SessionID != "session-1" {
		t.Fatalf("SessionID = %q, want session-1", resp.SessionID)
	}
	select {
	case <-updates:
		t.Fatal("available_commands_update arrived inside the session/new compatibility window")
	case <-time.After(availableCommandsAfterSessionNewDelay / 2):
	}
	select {
	case observed := <-updates:
		got := observed.notification
		if got.SessionID != "session-1" {
			t.Fatalf("notification sessionId = %q, want session-1", got.SessionID)
		}
		if got.Update.SessionUpdate != UpdateAvailableCmds {
			t.Fatalf("sessionUpdate = %q, want %q", got.Update.SessionUpdate, UpdateAvailableCmds)
		}
		if len(got.Update.AvailableCommands) != 1 || got.Update.AvailableCommands[0].Name != "agent" {
			t.Fatalf("availableCommands = %#v, want agent command", got.Update.AvailableCommands)
		}
		input := got.Update.AvailableCommands[0].Input
		if input == nil || input.Hint != "use|add|install|list|remove" {
			t.Fatalf("available command input = %#v, want hint", input)
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

func TestServerFrameLimitCoversProductImagePayload(t *testing.T) {
	const maxDecodedImageBytes = 32_000_000
	base64Bytes := 4 * ((maxDecodedImageBytes + 2) / 3)
	if serverMaxFrameSize <= base64Bytes {
		t.Fatalf("serverMaxFrameSize = %d, want more than %d bytes for base64 payload plus JSON", serverMaxFrameSize, base64Bytes)
	}
}

func TestResponseErrorPreservesProtocolErrorsAndSeparatesInternalFailures(t *testing.T) {
	t.Parallel()

	authRequired := acpsdk.NewAuthRequired(map[string]any{"method": "oauth"})
	if got := responseError(authRequired); got != authRequired {
		t.Fatalf("responseError(auth_required) = %#v, want original structured error", got)
	}
	if got := responseError(errors.New("runtime failed")); got.Code != -32603 {
		t.Fatalf("responseError(runtime failure) code = %d, want -32603", got.Code)
	}
	if got := responseError(context.Canceled); got.Code != -32800 {
		t.Fatalf("responseError(context.Canceled) code = %d, want -32800", got.Code)
	}
}

func TestServeStdioKeepsCallerProcessFilesOpen(t *testing.T) {
	input, inputPeer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := inputPeer.Close(); err != nil {
		t.Fatal(err)
	}
	outputPeer, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outputPeer.Close()
	defer output.Close()

	if err := ServeStdio(context.Background(), commandAgent{}, input, output); err != nil {
		t.Fatalf("ServeStdio() error = %v", err)
	}
	if _, err := input.Stat(); err != nil {
		t.Fatalf("caller input was closed: %v", err)
	}
	if _, err := output.Stat(); err != nil {
		t.Fatalf("caller output was closed: %v", err)
	}
}

func TestSDKOwnedServerOutputDuplicatesCallerFile(t *testing.T) {
	outputPeer, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outputPeer.Close()
	defer output.Close()

	owned, closeOnFailure, err := sdkOwnedServerOutput(output)
	if err != nil {
		t.Fatalf("sdkOwnedServerOutput() error = %v", err)
	}
	ownedFile, ok := owned.(*os.File)
	if !ok {
		t.Fatalf("sdkOwnedServerOutput() type = %T, want *os.File", owned)
	}
	if ownedFile.Fd() == output.Fd() {
		t.Fatal("SDK output reused the caller's file descriptor")
	}

	closeOnFailure()
	if _, err := ownedFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("SDK-owned output remains open after cleanup: %v", err)
	}
	if _, err := output.Stat(); err != nil {
		t.Fatalf("caller output was closed with SDK-owned duplicate: %v", err)
	}
}

func TestServeStdioCancelsPromptFromJSONRPCCancelRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	agent := &cancelAwareAgent{
		started:  make(chan struct{}),
		canceled: make(chan error, 1),
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, agent, clientToServerReader, serverToClientWriter)
	}()

	conn, err := acpsdk.NewConnectionWithOptions(nil, clientToServerWriter, serverToClientReader, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatalf("new client connection error = %v", err)
	}
	defer conn.Close()

	callCtx, cancelCall := context.WithCancel(ctx)
	callErr := make(chan error, 1)
	go func() {
		_, err := acpsdk.SendRequest[PromptResponse](conn, callCtx, MethodSessionPrompt, PromptRequest{SessionID: "session-1"})
		callErr <- err
	}()
	select {
	case <-agent.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for prompt handler")
	}
	cancelCall()
	select {
	case err := <-callErr:
		if err == nil {
			t.Fatal("canceled session/prompt call returned nil error")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for canceled session/prompt call")
	}
	select {
	case err := <-agent.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prompt handler cancellation = %v, want context.Canceled", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for $/cancel_request to cancel prompt handler")
	}

	cancel()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}

func TestServeStdioCancelsPromptFromSessionCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	agent := &sessionCancelAgent{
		started:  make(chan struct{}),
		canceled: make(chan CancelNotification, 1),
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, agent, clientToServerReader, serverToClientWriter)
	}()

	conn, err := acpsdk.NewConnectionWithOptions(nil, clientToServerWriter, serverToClientReader, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatalf("new client connection error = %v", err)
	}
	defer conn.Close()

	promptResult := make(chan PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := acpsdk.SendRequest[PromptResponse](conn, ctx, MethodSessionPrompt, PromptRequest{SessionID: "session-1"})
		if err != nil {
			promptErr <- err
			return
		}
		promptResult <- resp
	}()
	select {
	case <-agent.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for prompt handler")
	}
	if err := conn.SendNotification(ctx, MethodSessionCancel, CancelNotification{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/cancel notification error = %v", err)
	}
	select {
	case req := <-agent.canceled:
		if req.SessionID != "session-1" {
			t.Fatalf("cancel sessionId = %q, want session-1", req.SessionID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Agent.Cancel")
	}
	select {
	case err := <-promptErr:
		t.Fatalf("session/prompt error = %v, want cancelled response", err)
	case resp := <-promptResult:
		if resp.StopReason != StopReasonCancelled {
			t.Fatalf("stopReason = %q, want %q", resp.StopReason, StopReasonCancelled)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for cancelled session/prompt response")
	}

	cancel()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}

func TestServeStdioRejectsWrongACPMessageDirection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	agent := &messageDirectionAgent{cancelObserved: make(chan struct{}, 1)}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, agent, clientToServerReader, serverToClientWriter)
	}()

	conn, err := acpsdk.NewConnectionWithOptions(nil, clientToServerWriter, serverToClientReader, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.SendNotification(ctx, MethodSessionPrompt, PromptRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/prompt notification error = %v", err)
	}
	if err := conn.SendNotification(ctx, MethodSessionClose, CloseSessionRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/close notification error = %v", err)
	}
	if err := conn.SendNotification(ctx, MethodSessionCancel, CancelNotification{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/cancel notification error = %v", err)
	}
	select {
	case <-agent.cancelObserved:
	case <-ctx.Done():
		t.Fatal("timed out waiting for valid session/cancel notification")
	}
	if got := agent.promptCalls.Load(); got != 0 {
		t.Fatalf("prompt calls after notification = %d, want 0", got)
	}
	if got := agent.closeCalls.Load(); got != 0 {
		t.Fatalf("close calls after notification = %d, want 0", got)
	}

	_, err = acpsdk.SendRequest[struct{}](conn, ctx, MethodSessionCancel, CancelNotification{SessionID: "session-1"})
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
		t.Fatalf("session/cancel request error = %v, want method not found", err)
	}
	if got := agent.cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel calls after request = %d, want only the notification", got)
	}

	if _, err := acpsdk.SendRequest[PromptResponse](conn, ctx, MethodSessionPrompt, PromptRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/prompt request error = %v", err)
	}
	if _, err := acpsdk.SendRequest[CloseSessionResponse](conn, ctx, MethodSessionClose, CloseSessionRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/close request error = %v", err)
	}
	if got := agent.promptCalls.Load(); got != 1 {
		t.Fatalf("prompt calls after request = %d, want 1", got)
	}
	if got := agent.closeCalls.Load(); got != 1 {
		t.Fatalf("close calls after request = %d, want 1", got)
	}

	cancel()
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
	conn, err := acpsdk.NewConnectionWithOptions(
		func(_ context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
			methods <- method
			switch method {
			case MethodTerminalCreate:
				var req CreateTerminalRequest
				if err := json.Unmarshal(params, &req); err != nil {
					return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
				}
				if req.SessionID != "session-1" || req.Command != "npm" || len(req.Args) != 1 || req.Args[0] != "test" {
					return nil, acpsdk.NewInvalidParams(map[string]any{"error": fmt.Sprintf("unexpected terminal/create request: %#v", req)})
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
				return nil, acpsdk.NewMethodNotFound(method)
			}
		},
		clientToServerWriter,
		serverToClientReader,
		acpsdk.ConnectionOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := acpsdk.SendRequest[InitializeResponse](conn, ctx, MethodInitialize, InitializeRequest{
		ClientCapabilities: map[string]any{"terminal": true},
	}); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	resp, err := acpsdk.SendRequest[PromptResponse](conn, ctx, MethodSessionPrompt, PromptRequest{SessionID: "session-1"})
	if err != nil {
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

func TestServeStdioHandlesStableSessionLifecycleMethods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	agent := &stableLifecycleAgent{}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, agent, clientToServerReader, serverToClientWriter)
	}()

	conn, err := acpsdk.NewConnectionWithOptions(nil, clientToServerWriter, serverToClientReader, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	listResp, err := acpsdk.SendRequest[SessionListResponse](conn, ctx, MethodSessionList, SessionListRequest{CWD: "/tmp/project"})
	if err != nil {
		t.Fatalf("session/list call error = %v", err)
	}
	if len(listResp.Sessions) != 1 || listResp.Sessions[0].SessionID != "session-1" {
		t.Fatalf("session/list response = %#v, want session-1", listResp)
	}
	if agent.listCWD != "/tmp/project" {
		t.Fatalf("session/list cwd = %q, want /tmp/project", agent.listCWD)
	}

	if _, err := acpsdk.SendRequest[ResumeSessionResponse](conn, ctx, MethodSessionResume, ResumeSessionRequest{SessionID: "session-1", CWD: "/tmp/project"}); err != nil {
		t.Fatalf("session/resume call error = %v", err)
	}
	if agent.resumeSessionID != "session-1" {
		t.Fatalf("session/resume id = %q, want session-1", agent.resumeSessionID)
	}

	if _, err := acpsdk.SendRequest[CloseSessionResponse](conn, ctx, MethodSessionClose, CloseSessionRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/close call error = %v", err)
	}
	if agent.closeSessionID != "session-1" {
		t.Fatalf("session/close id = %q, want session-1", agent.closeSessionID)
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

type cancelAwareAgent struct {
	commandAgent
	started  chan struct{}
	canceled chan error
}

func (a *cancelAwareAgent) Prompt(ctx context.Context, _ PromptRequest, _ PromptCallbacks) (PromptResponse, error) {
	close(a.started)
	<-ctx.Done()
	err := context.Cause(ctx)
	a.canceled <- err
	return PromptResponse{}, err
}

type sessionCancelAgent struct {
	commandAgent
	started  chan struct{}
	canceled chan CancelNotification

	mu     sync.Mutex
	cancel context.CancelFunc
}

type messageDirectionAgent struct {
	stableLifecycleAgent
	promptCalls    atomic.Int32
	closeCalls     atomic.Int32
	cancelCalls    atomic.Int32
	cancelObserved chan struct{}
}

func (a *messageDirectionAgent) Prompt(context.Context, PromptRequest, PromptCallbacks) (PromptResponse, error) {
	a.promptCalls.Add(1)
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

func (a *messageDirectionAgent) CloseSession(_ context.Context, _ CloseSessionRequest) (CloseSessionResponse, error) {
	a.closeCalls.Add(1)
	return CloseSessionResponse{}, nil
}

func (a *messageDirectionAgent) Cancel(context.Context, CancelNotification) error {
	a.cancelCalls.Add(1)
	select {
	case a.cancelObserved <- struct{}{}:
	default:
	}
	return nil
}

func (a *sessionCancelAgent) Prompt(ctx context.Context, _ PromptRequest, _ PromptCallbacks) (PromptResponse, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	close(a.started)
	<-runCtx.Done()
	return PromptResponse{StopReason: StopReasonCancelled}, nil
}

func (a *sessionCancelAgent) Cancel(_ context.Context, req CancelNotification) error {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.canceled <- req
	return nil
}

type terminalClientAgent struct {
	commandAgent
}

func (terminalClientAgent) Prompt(ctx context.Context, req PromptRequest, cb PromptCallbacks) (PromptResponse, error) {
	terminal, ok := cb.(TerminalClientCallbacks)
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

type stableLifecycleAgent struct {
	commandAgent
	listCWD         string
	resumeSessionID string
	closeSessionID  string
}

func (a *stableLifecycleAgent) ListSessions(_ context.Context, req SessionListRequest) (SessionListResponse, error) {
	a.listCWD = req.CWD
	return SessionListResponse{
		Sessions: []SessionSummary{{
			SessionID: "session-1",
			CWD:       "/tmp/project",
			Title:     "Existing session",
			UpdatedAt: "2026-05-04T00:00:00Z",
		}},
	}, nil
}

func (a *stableLifecycleAgent) ResumeSession(_ context.Context, req ResumeSessionRequest) (ResumeSessionResponse, error) {
	a.resumeSessionID = req.SessionID
	return ResumeSessionResponse{}, nil
}

func (a *stableLifecycleAgent) CloseSession(_ context.Context, req CloseSessionRequest) (CloseSessionResponse, error) {
	a.closeSessionID = req.SessionID
	return CloseSessionResponse{}, nil
}

func intPtr(v int) *int {
	return &v
}
