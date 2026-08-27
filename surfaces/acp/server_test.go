package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	protocolacp "github.com/caelis-labs/caelis/protocol/acp/schema"
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
			if method != acpsdk.ClientMethodSessionUpdate {
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

	resp, err := acpsdk.SendRequest[protocolacp.NewSessionResponse](conn, ctx, acpsdk.AgentMethodSessionNew, protocolacp.NewSessionRequest{CWD: t.TempDir()})
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
		if got.Update.SessionUpdate != protocolacp.UpdateAvailableCmds {
			t.Fatalf("sessionUpdate = %q, want %q", got.Update.SessionUpdate, protocolacp.UpdateAvailableCmds)
		}
		if len(got.Update.AvailableCommands) != 1 || got.Update.AvailableCommands[0].Name != "agent" {
			t.Fatalf("availableCommands = %#v, want agent command", got.Update.AvailableCommands)
		}
		input := got.Update.AvailableCommands[0].Input
		if input == nil || input.Unstructured == nil || input.Unstructured.Hint != "use|add|install|list|remove" {
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

func TestAuthenticateRequiresOptionalAgentCapability(t *testing.T) {
	t.Parallel()

	conn := &serverConn{agent: noAuthAgent{}}
	_, rpcErr := conn.handleRequest(
		context.Background(),
		nil,
		acpsdk.AgentMethodAuthenticate,
		json.RawMessage(`{"methodId":"agent"}`),
	)
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("authenticate error = %#v, want method not found", rpcErr)
	}
}

func TestDecodeRequiredStandardParamsUsesSDKValidation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw    json.RawMessage
		target any
	}{
		"authenticate missing method": {
			raw:    json.RawMessage(`{}`),
			target: &acpsdk.AuthenticateRequest{},
		},
		"close null params": {
			raw:    json.RawMessage(`null`),
			target: &acpsdk.CloseSessionRequest{},
		},
		"set mode missing mode": {
			raw:    json.RawMessage(`{"sessionId":"session-1"}`),
			target: &acpsdk.SetSessionModeRequest{},
		},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := decodeRequiredParams(test.raw, test.target); err == nil {
				t.Fatal("decodeRequiredParams() error = nil, want SDK validation failure")
			}
		})
	}

	var closeRequest acpsdk.CloseSessionRequest
	if err := decodeRequiredParams(json.RawMessage(`{"sessionId":"session-1"}`), &closeRequest); err != nil {
		t.Fatalf("valid close params: %v", err)
	}
	if closeRequest.SessionId != "session-1" {
		t.Fatalf("close session id = %q, want session-1", closeRequest.SessionId)
	}
}

func TestRetiredSetModelMethodIsNotDispatched(t *testing.T) {
	t.Parallel()

	conn := &serverConn{agent: commandAgent{}}
	_, rpcErr := conn.handleRequest(
		context.Background(),
		nil,
		"session/set_model",
		json.RawMessage(`{"sessionId":"session-1","modelId":"model-1"}`),
	)
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("session/set_model error = %#v, want method not found", rpcErr)
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
		_, err := acpsdk.SendRequest[protocolacp.PromptResponse](conn, callCtx, acpsdk.AgentMethodSessionPrompt, protocolacp.PromptRequest{SessionID: "session-1"})
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
		canceled: make(chan acpsdk.CancelNotification, 1),
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

	promptResult := make(chan protocolacp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := acpsdk.SendRequest[protocolacp.PromptResponse](conn, ctx, acpsdk.AgentMethodSessionPrompt, protocolacp.PromptRequest{SessionID: "session-1"})
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
	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionCancel, acpsdk.CancelNotification{SessionId: "session-1"}); err != nil {
		t.Fatalf("session/cancel notification error = %v", err)
	}
	select {
	case req := <-agent.canceled:
		if req.SessionId != "session-1" {
			t.Fatalf("cancel sessionId = %q, want session-1", req.SessionId)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Agent.Cancel")
	}
	select {
	case err := <-promptErr:
		t.Fatalf("session/prompt error = %v, want cancelled response", err)
	case resp := <-promptResult:
		if resp.StopReason != protocolacp.StopReasonCancelled {
			t.Fatalf("stopReason = %q, want %q", resp.StopReason, protocolacp.StopReasonCancelled)
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

	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionPrompt, protocolacp.PromptRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/prompt notification error = %v", err)
	}
	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionClose, acpsdk.CloseSessionRequest{SessionId: "session-1"}); err != nil {
		t.Fatalf("session/close notification error = %v", err)
	}
	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionCancel, nil); err != nil {
		t.Fatalf("session/cancel missing params error = %v", err)
	}
	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionCancel, json.RawMessage("null")); err != nil {
		t.Fatalf("session/cancel null params error = %v", err)
	}
	if err := conn.SendNotification(ctx, acpsdk.AgentMethodSessionCancel, acpsdk.CancelNotification{SessionId: "session-1"}); err != nil {
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

	_, err = acpsdk.SendRequest[struct{}](conn, ctx, acpsdk.AgentMethodSessionCancel, acpsdk.CancelNotification{SessionId: "session-1"})
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
		t.Fatalf("session/cancel request error = %v, want method not found", err)
	}
	if got := agent.cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel calls after request = %d, want only the notification", got)
	}

	if _, err := acpsdk.SendRequest[protocolacp.PromptResponse](conn, ctx, acpsdk.AgentMethodSessionPrompt, protocolacp.PromptRequest{SessionID: "session-1"}); err != nil {
		t.Fatalf("session/prompt request error = %v", err)
	}
	if _, err := acpsdk.SendRequest[acpsdk.CloseSessionResponse](conn, ctx, acpsdk.AgentMethodSessionClose, acpsdk.CloseSessionRequest{SessionId: "session-1"}); err != nil {
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

	listResp, err := acpsdk.SendRequest[acpsdk.ListSessionsResponse](conn, ctx, acpsdk.AgentMethodSessionList, acpsdk.ListSessionsRequest{Cwd: testStringPointer("/tmp/project")})
	if err != nil {
		t.Fatalf("session/list call error = %v", err)
	}
	if len(listResp.Sessions) != 1 || listResp.Sessions[0].SessionId != "session-1" {
		t.Fatalf("session/list response = %#v, want session-1", listResp)
	}
	if agent.listCWD != "/tmp/project" {
		t.Fatalf("session/list cwd = %q, want /tmp/project", agent.listCWD)
	}

	if _, err := acpsdk.SendRequest[protocolacp.ResumeSessionResponse](conn, ctx, acpsdk.AgentMethodSessionResume, protocolacp.ResumeSessionRequest{SessionID: "session-1", CWD: "/tmp/project"}); err != nil {
		t.Fatalf("session/resume call error = %v", err)
	}
	if agent.resumeSessionID != "session-1" {
		t.Fatalf("session/resume id = %q, want session-1", agent.resumeSessionID)
	}

	if _, err := acpsdk.SendRequest[acpsdk.CloseSessionResponse](conn, ctx, acpsdk.AgentMethodSessionClose, acpsdk.CloseSessionRequest{SessionId: "session-1"}); err != nil {
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
		SessionUpdate     string                    `json:"sessionUpdate"`
		AvailableCommands []acpsdk.AvailableCommand `json:"availableCommands"`
	} `json:"update"`
}

type commandAgent struct{}

type noAuthAgent struct{}

func (noAuthAgent) Initialize(context.Context, protocolacp.InitializeRequest) (protocolacp.InitializeResponse, error) {
	return protocolacp.InitializeResponse{}, nil
}

func (noAuthAgent) NewSession(context.Context, protocolacp.NewSessionRequest) (protocolacp.NewSessionResponse, error) {
	return protocolacp.NewSessionResponse{}, nil
}

func (noAuthAgent) Prompt(context.Context, protocolacp.PromptRequest, PromptCallbacks) (protocolacp.PromptResponse, error) {
	return protocolacp.PromptResponse{}, nil
}

func (noAuthAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	return nil
}

func (commandAgent) Initialize(context.Context, protocolacp.InitializeRequest) (protocolacp.InitializeResponse, error) {
	return protocolacp.InitializeResponse{}, nil
}

func (commandAgent) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}

func (commandAgent) NewSession(context.Context, protocolacp.NewSessionRequest) (protocolacp.NewSessionResponse, error) {
	return protocolacp.NewSessionResponse{SessionID: "session-1"}, nil
}

func (commandAgent) Prompt(context.Context, protocolacp.PromptRequest, PromptCallbacks) (protocolacp.PromptResponse, error) {
	return protocolacp.PromptResponse{}, nil
}

func (commandAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	return nil
}

func (commandAgent) AvailableCommands(context.Context, string) ([]acpsdk.AvailableCommand, error) {
	return []acpsdk.AvailableCommand{{
		Name:        "agent",
		Description: "Manage ACP agents",
		Input: &acpsdk.AvailableCommandInput{Unstructured: &acpsdk.UnstructuredCommandInput{
			Hint: "use|add|install|list|remove",
		}},
	}}, nil
}

type cancelAwareAgent struct {
	commandAgent
	started  chan struct{}
	canceled chan error
}

func (a *cancelAwareAgent) Prompt(ctx context.Context, _ protocolacp.PromptRequest, _ PromptCallbacks) (protocolacp.PromptResponse, error) {
	close(a.started)
	<-ctx.Done()
	err := context.Cause(ctx)
	a.canceled <- err
	return protocolacp.PromptResponse{}, err
}

type sessionCancelAgent struct {
	commandAgent
	started  chan struct{}
	canceled chan acpsdk.CancelNotification

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

func (a *messageDirectionAgent) Prompt(context.Context, protocolacp.PromptRequest, PromptCallbacks) (protocolacp.PromptResponse, error) {
	a.promptCalls.Add(1)
	return protocolacp.PromptResponse{StopReason: protocolacp.StopReasonEndTurn}, nil
}

func (a *messageDirectionAgent) CloseSession(_ context.Context, _ acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	a.closeCalls.Add(1)
	return acpsdk.CloseSessionResponse{}, nil
}

func (a *messageDirectionAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	a.cancelCalls.Add(1)
	select {
	case a.cancelObserved <- struct{}{}:
	default:
	}
	return nil
}

func (a *sessionCancelAgent) Prompt(ctx context.Context, _ protocolacp.PromptRequest, _ PromptCallbacks) (protocolacp.PromptResponse, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	close(a.started)
	<-runCtx.Done()
	return protocolacp.PromptResponse{StopReason: protocolacp.StopReasonCancelled}, nil
}

func (a *sessionCancelAgent) Cancel(_ context.Context, req acpsdk.CancelNotification) error {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.canceled <- req
	return nil
}

type stableLifecycleAgent struct {
	commandAgent
	listCWD         string
	resumeSessionID string
	closeSessionID  string
}

func (a *stableLifecycleAgent) ListSessions(_ context.Context, req acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	a.listCWD = testOptionalStringValue(req.Cwd)
	return acpsdk.ListSessionsResponse{
		Sessions: []acpsdk.SessionInfo{{
			SessionId: "session-1",
			Cwd:       "/tmp/project",
			Title:     testStringPointer("Existing session"),
			UpdatedAt: testStringPointer("2026-05-04T00:00:00Z"),
		}},
	}, nil
}

func (a *stableLifecycleAgent) ResumeSession(_ context.Context, req protocolacp.ResumeSessionRequest) (protocolacp.ResumeSessionResponse, error) {
	a.resumeSessionID = req.SessionID
	return protocolacp.ResumeSessionResponse{}, nil
}

func (a *stableLifecycleAgent) CloseSession(_ context.Context, req acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	a.closeSessionID = string(req.SessionId)
	return acpsdk.CloseSessionResponse{}, nil
}

func testStringPointer(value string) *string {
	return &value
}

func testOptionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
