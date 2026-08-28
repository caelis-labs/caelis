package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
)

// serverMaxFrameSize accommodates the product's 32 MB decoded aggregate image
// limit after base64 expansion and JSON envelope overhead while keeping the
// formerly unbounded stdio reader finite.
const serverMaxFrameSize = 64 * 1024 * 1024

// New-session clients may bind their UI/session routing only after session/new
// resolves. Preserve the established compatibility window for ordinary ACP
// clients that do not provide a response hook.
const availableCommandsAfterSessionNewDelay = 100 * time.Millisecond

// methodSessionSteering is the interoperable ACP extension for steering a
// running Session after support is advertised in initialize _meta.steering.
const methodSessionSteering = "_session/steering"

// ServeStdio serves one agent-side ACP connection over NDJSON stdio.
func ServeStdio(ctx context.Context, agent Agent, in io.Reader, out io.Writer) error {
	if agent == nil {
		return errors.New("acp: agent is required")
	}
	if in == nil || out == nil {
		return errors.New("acp: stdio streams are required")
	}
	connectionInput, closeInputOnFailure, err := sdkOwnedServerInput(in)
	if err != nil {
		return err
	}
	connectionOutput, closeOutputOnFailure, err := sdkOwnedServerOutput(out)
	if err != nil {
		closeInputOnFailure()
		return err
	}
	peer, err := acpsdk.NewAgentSideConnectionWithOptions(
		&serverAgent{agent: agent},
		connectionOutput,
		connectionInput,
		acpsdk.ConnectionOptions{MaxFrameSize: serverMaxFrameSize},
	)
	if err != nil {
		closeInputOnFailure()
		closeOutputOnFailure()
		return err
	}
	defer peer.Close()
	if err := peer.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if errors.Is(err, acpsdk.ErrPeerClosed) || errors.Is(err, acpsdk.ErrConnectionClosed) {
			return nil
		}
		return err
	}
	return nil
}

// serverAgent adapts the callback-aware product Agent to the SDK's typed
// agent-side dispatcher. The SDK owns standard method decoding, validation,
// direction checks, request cancellation, and connection lifecycle.
type serverAgent struct {
	agent Agent
}

func (a *serverAgent) Initialize(ctx context.Context, req acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	resp, err := a.agent.Initialize(ctx, req)
	return resp, productMethodError(acpsdk.AgentMethodInitialize, err)
}

func (a *serverAgent) Authenticate(ctx context.Context, req acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	handler, ok := a.agent.(agentAuthenticator)
	if !ok {
		return acpsdk.AuthenticateResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodAuthenticate)
	}
	resp, err := handler.Authenticate(ctx, req)
	return resp, productMethodError(acpsdk.AgentMethodAuthenticate, err)
}

func (a *serverAgent) NewSession(ctx context.Context, req acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	resp, err := a.agent.NewSession(ctx, req)
	if err != nil {
		return resp, productMethodError(acpsdk.AgentMethodSessionNew, err)
	}
	if err := a.afterAvailableCommands(ctx, string(resp.SessionId), availableCommandsAfterSessionNewDelay); err != nil {
		return acpsdk.NewSessionResponse{}, err
	}
	return resp, nil
}

func (a *serverAgent) ListSessions(ctx context.Context, req acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	handler, ok := a.agent.(sessionLister)
	if !ok {
		return acpsdk.ListSessionsResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionList)
	}
	resp, err := handler.ListSessions(ctx, req)
	return resp, productMethodError(acpsdk.AgentMethodSessionList, err)
}

func (a *serverAgent) LoadSession(ctx context.Context, req acpsdk.LoadSessionRequest) (acpsdk.LoadSessionResponse, error) {
	handler, ok := a.agent.(sessionLoader)
	if !ok {
		return acpsdk.LoadSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionLoad)
	}
	callbacks, err := serverCallbacksFromContext(ctx)
	if err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	resp, err := handler.LoadSession(ctx, req, callbacks)
	if err != nil {
		return resp, productMethodError(acpsdk.AgentMethodSessionLoad, err)
	}
	if err := a.afterAvailableCommands(ctx, string(req.SessionId), 0); err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	return resp, nil
}

func (a *serverAgent) ResumeSession(ctx context.Context, req acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	handler, ok := a.agent.(sessionResumer)
	if !ok {
		return acpsdk.ResumeSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionResume)
	}
	resp, err := handler.ResumeSession(ctx, req)
	if err != nil {
		return resp, productMethodError(acpsdk.AgentMethodSessionResume, err)
	}
	if err := a.afterAvailableCommands(ctx, string(req.SessionId), 0); err != nil {
		return acpsdk.ResumeSessionResponse{}, err
	}
	return resp, nil
}

func (a *serverAgent) CloseSession(ctx context.Context, req acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	handler, ok := a.agent.(sessionCloser)
	if !ok {
		return acpsdk.CloseSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionClose)
	}
	resp, err := handler.CloseSession(ctx, req)
	return resp, productMethodError(acpsdk.AgentMethodSessionClose, err)
}

func (a *serverAgent) SetSessionMode(ctx context.Context, req acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	handler, ok := a.agent.(sessionModeSetter)
	if !ok {
		return acpsdk.SetSessionModeResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionSetMode)
	}
	resp, err := handler.SetSessionMode(ctx, req)
	return resp, productMethodError(acpsdk.AgentMethodSessionSetMode, err)
}

func (a *serverAgent) SetSessionConfigOption(ctx context.Context, req acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	handler, ok := a.agent.(sessionConfigSetter)
	if !ok {
		return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionSetConfigOption)
	}
	resp, err := handler.SetSessionConfigOption(ctx, req)
	return resp, productMethodError(acpsdk.AgentMethodSessionSetConfigOption, err)
}

func (a *serverAgent) Prompt(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	raw, ok := acpsdk.InboundParamsFromContext(ctx)
	if !ok {
		return acpsdk.PromptResponse{}, acpsdk.NewInternalError(map[string]any{"error": "ACP inbound params are unavailable"})
	}
	runtimeacp.PreserveLegacyPromptImageNames(raw, &req)
	callbacks, err := serverCallbacksFromContext(ctx)
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	resp, err := a.agent.Prompt(ctx, req, callbacks)
	return resp, productMethodError(acpsdk.AgentMethodSessionPrompt, err)
}

func (a *serverAgent) Cancel(ctx context.Context, req acpsdk.CancelNotification) error {
	return productMethodError(acpsdk.AgentMethodSessionCancel, a.agent.Cancel(ctx, req))
}

func (a *serverAgent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	inbound, ok := acpsdk.InboundInfoFromContext(ctx)
	if !ok {
		return nil, acpsdk.NewInternalError(map[string]any{"error": "ACP inbound message metadata is unavailable"})
	}
	if inbound.Kind != acpsdk.InboundRequest {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	if method != methodSessionSteering {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	var req SessionSteeringRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if err := validateSessionSteeringRequest(req); err != nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	handler, ok := a.agent.(sessionSteerer)
	if !ok {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	resp, err := handler.SteerSession(ctx, req)
	return resp, productMethodError(method, err)
}

func (a *serverAgent) afterAvailableCommands(ctx context.Context, sessionID string, delay time.Duration) error {
	handler, ok := a.agent.(commandProvider)
	sessionID = strings.TrimSpace(sessionID)
	if !ok || sessionID == "" {
		return nil
	}
	peer, ok := acpsdk.AgentSideConnectionFromContext(ctx)
	if !ok {
		return acpsdk.NewInternalError(map[string]any{"error": "ACP agent-side connection is unavailable"})
	}
	return acpsdk.AfterResponse(ctx, func(callbackCtx context.Context) error {
		emit := func() {
			a.emitAvailableCommands(callbackCtx, peer, handler, sessionID)
		}
		if delay <= 0 {
			emit()
			return nil
		}
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-callbackCtx.Done():
			case <-timer.C:
				emit()
			}
		}()
		return nil
	})
}

func (a *serverAgent) emitAvailableCommands(ctx context.Context, peer *acpsdk.AgentSideConnection, handler commandProvider, sessionID string) {
	cmds, err := handler.AvailableCommands(ctx, sessionID)
	if err != nil || len(cmds) == 0 {
		return
	}
	raw, err := json.Marshal(acpsdk.SessionAvailableCommandsUpdate{
		SessionUpdate:     eventstream.UpdateAvailableCmds,
		AvailableCommands: cmds,
	})
	if err != nil {
		return
	}
	_ = sendSessionUpdate(ctx, peer, eventstream.SessionNotification{
		SessionID: sessionID,
		Update: eventstream.RawUpdate{
			SessionUpdate: eventstream.UpdateAvailableCmds,
			Raw:           raw,
		},
	})
}

type serverPromptCallbacks struct {
	peer *acpsdk.AgentSideConnection
}

func (c serverPromptCallbacks) SessionUpdate(ctx context.Context, notification eventstream.SessionNotification) error {
	return sendSessionUpdate(ctx, c.peer, notification)
}

func (c serverPromptCallbacks) RequestPermission(ctx context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	if err := req.Validate(); err != nil {
		return acpsdk.RequestPermissionResponse{}, fmt.Errorf("acp surface: validate permission request: %w", err)
	}
	wireResponse, err := c.peer.RequestPermission(ctx, req)
	if err != nil {
		return acpsdk.RequestPermissionResponse{}, err
	}
	if err := wireResponse.Validate(); err != nil {
		return acpsdk.RequestPermissionResponse{}, fmt.Errorf("acp surface: validate permission response: %w", err)
	}
	return wireResponse, nil
}

func serverCallbacksFromContext(ctx context.Context) (serverPromptCallbacks, error) {
	peer, ok := acpsdk.AgentSideConnectionFromContext(ctx)
	if !ok {
		return serverPromptCallbacks{}, acpsdk.NewInternalError(map[string]any{"error": "ACP agent-side connection is unavailable"})
	}
	return serverPromptCallbacks{peer: peer}, nil
}

func sendSessionUpdate(ctx context.Context, peer *acpsdk.AgentSideConnection, notification eventstream.SessionNotification) error {
	wire, err := sessionNotificationForWire(notification)
	if err != nil {
		return err
	}
	switch typed := wire.(type) {
	case acpsdk.SessionNotification:
		return peer.SessionUpdate(ctx, typed)
	case eventstream.SessionNotification:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Errorf("acp surface: encode compatible session notification: %w", err)
		}
		return peer.SessionUpdateRaw(ctx, raw)
	default:
		return fmt.Errorf("acp surface: unsupported session notification wire type %T", wire)
	}
}

func validateSessionSteeringRequest(req SessionSteeringRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	if len(req.Prompt) == 0 {
		return fmt.Errorf("prompt must contain at least one content block")
	}
	_, err := decodeSessionSteeringOptions(req.Meta)
	return err
}

func productMethodError(method string, err error) error {
	if errors.Is(err, runtimeacp.ErrCapabilityUnsupported) {
		return acpsdk.NewMethodNotFound(method)
	}
	return err
}

var (
	_ acpsdk.Agent                  = (*serverAgent)(nil)
	_ acpsdk.AgentAuthenticator     = (*serverAgent)(nil)
	_ acpsdk.AgentSessionLister     = (*serverAgent)(nil)
	_ acpsdk.AgentLoader            = (*serverAgent)(nil)
	_ acpsdk.AgentSessionResumer    = (*serverAgent)(nil)
	_ acpsdk.AgentSessionCloser     = (*serverAgent)(nil)
	_ acpsdk.AgentSessionMode       = (*serverAgent)(nil)
	_ acpsdk.AgentSessionConfig     = (*serverAgent)(nil)
	_ acpsdk.ExtensionMethodHandler = (*serverAgent)(nil)
	_ PromptCallbacks               = serverPromptCallbacks{}
)
