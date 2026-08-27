package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	sdkstdio "github.com/caelis-labs/acp-go-sdk/transport/stdio"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const maxFrameSize = 64 * 1024 * 1024

const processShutdownGrace = 2 * time.Second

type PermissionHandler func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error)

type Config struct {
	HostedAdapterID     string
	ConnectionID        string
	EndpointResolver    endpoint.Resolver
	Command             string
	Args                []string
	Env                 map[string]string
	WorkDir             string
	ClientInfo          *acpsdk.Implementation
	OnUpdate            func(UpdateEnvelope)
	OnPermissionRequest PermissionHandler
	TerminalAuth        bool
}

// Client owns the Caelis policy around one SDK connection and optional SDK
// subprocess. JSON-RPC ordering, request lifecycle, and process waiting stay
// owned by acp-go-sdk.
type Client struct {
	conn *acpsdk.Connection
	proc *sdkstdio.Process
	cfg  Config

	stderrMu    sync.Mutex
	stderrBuf   bytes.Buffer
	releaseOnce sync.Once
	release     func()
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("acp client context is required")
	}
	if adapterID := strings.TrimSpace(cfg.HostedAdapterID); adapterID != "" {
		if strings.TrimSpace(cfg.Command) != "" || len(cfg.Args) != 0 || len(cfg.Env) != 0 {
			return nil, errors.New("acp client: hosted endpoint cannot include a process declaration")
		}
		process, err := endpoint.Resolve(ctx, cfg.EndpointResolver, endpoint.Request{
			AdapterID: adapterID, ConnectionID: firstNonEmpty(cfg.ConnectionID, adapterID), CWD: cfg.WorkDir,
		})
		if err != nil {
			return nil, err
		}
		cfg.Command = process.Command
		cfg.Args = process.Args
		cfg.Env = process.Env
		cfg.WorkDir = firstNonEmpty(process.WorkDir, cfg.WorkDir)
		client := &Client{cfg: cfg, release: process.Release}
		return client.start(ctx)
	}
	client := &Client{cfg: cfg}
	return client.start(ctx)
}

func (c *Client) start(ctx context.Context) (*Client, error) {
	workDir, err := absoluteWorkDir(c.cfg.WorkDir)
	if err != nil {
		c.releaseEndpoint()
		return nil, err
	}
	proc, err := sdkstdio.Start(ctx, sdkstdio.Command{
		Executable: c.cfg.Command,
		Args:       append([]string(nil), c.cfg.Args...),
		Dir:        workDir,
		Env:        mergedEnv(c.cfg.Env),
		Stderr:     stderrBufferWriter{client: c},
	})
	if err != nil {
		c.releaseEndpoint()
		return nil, err
	}
	c.proc = proc
	if err := c.bind(proc.Input(), proc.Output()); err != nil {
		_ = proc.Close()
		c.releaseEndpoint()
		return nil, err
	}
	return c, nil
}

// NewStreamClient binds an already-owned stream pair. It exists for focused
// bridge tests and does not assume process ownership.
func NewStreamClient(peerInput io.Writer, peerOutput io.Reader, cfg Config) (*Client, error) {
	client := &Client{cfg: cfg}
	if err := client.bind(peerInput, peerOutput); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) bind(peerInput io.Writer, peerOutput io.Reader) error {
	conn, err := acpsdk.NewConnectionWithOptions(
		c.handleMethod,
		peerInput,
		peerOutput,
		acpsdk.ConnectionOptions{MaxFrameSize: maxFrameSize},
	)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *Client) Initialize(ctx context.Context) (InitializeResponse, error) {
	clientCapabilities := acpsdk.ClientCapabilities{
		Meta: map[string]json.RawMessage{
			metautil.TerminalOutputKey: json.RawMessage("true"),
		},
	}
	if c.cfg.TerminalAuth {
		clientCapabilities.Auth.Terminal = true
	}
	return sendRequest[InitializeResponse](c, ctx, MethodInitialize, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: clientCapabilities,
		ClientInfo:         c.cfg.ClientInfo,
	})
}

// Authenticate invokes the stable ACP v1 agent-managed authentication flow.
func (c *Client) Authenticate(ctx context.Context, methodID string) error {
	_, err := sendRequest[AuthenticateResponse](c, ctx, MethodAuthenticate, AuthenticateRequest{
		MethodId: acpsdk.AuthMethodId(strings.TrimSpace(methodID)),
	})
	return err
}

func (c *Client) NewSession(ctx context.Context, cwd string, meta map[string]any) (NewSessionResponse, error) {
	return sendRequest[NewSessionResponse](c, ctx, MethodSessionNew, NewSessionRequest{
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
		Meta:       metautil.CloneMap(meta),
	})
}

func (c *Client) ListSessions(ctx context.Context, req acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return sendRequest[acpsdk.ListSessionsResponse](c, ctx, MethodSessionList, req)
}

func (c *Client) LoadSession(ctx context.Context, sessionID string, cwd string, meta map[string]any) (LoadSessionResponse, error) {
	return sendRequest[LoadSessionResponse](c, ctx, MethodSessionLoad, LoadSessionRequest{
		SessionID:  sessionID,
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
		Meta:       metautil.CloneMap(meta),
	})
}

func (c *Client) ResumeSession(ctx context.Context, sessionID string, cwd string, meta map[string]any) (ResumeSessionResponse, error) {
	return sendRequest[ResumeSessionResponse](c, ctx, MethodSessionResume, ResumeSessionRequest{
		SessionID:  sessionID,
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
		Meta:       metautil.CloneMap(meta),
	})
}

func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	_, err := sendRequest[CloseSessionResponse](c, ctx, MethodSessionClose, CloseSessionRequest{
		SessionId: acpsdk.SessionId(strings.TrimSpace(sessionID)),
	})
	return err
}

func (c *Client) SetMode(ctx context.Context, sessionID string, modeID string) error {
	_, err := sendRequest[SetSessionModeResponse](c, ctx, MethodSessionSetMode, SetSessionModeRequest{
		SessionId: acpsdk.SessionId(sessionID),
		ModeId:    acpsdk.SessionModeId(modeID),
	})
	return err
}

func (c *Client) SetConfigOption(ctx context.Context, sessionID string, configID string, value any) (SetSessionConfigOptionResponse, error) {
	return sendRequest[SetSessionConfigOptionResponse](c, ctx, MethodSessionSetConfig, SetSessionConfigOptionRequest{
		SessionID: sessionID,
		ConfigID:  configID,
		Value:     value,
	})
}

func (c *Client) SetModel(ctx context.Context, sessionID string, modelID string) error {
	_, err := sendRequest[SetSessionModelResponse](c, ctx, MethodSessionSetModel, SetSessionModelRequest{
		SessionID: sessionID,
		ModelID:   modelID,
	})
	return err
}

func sendRequest[T any](c *Client, ctx context.Context, method string, params any) (T, error) {
	var zero T
	if c == nil || c.conn == nil {
		return zero, errors.New("acp client is unavailable")
	}
	return acpsdk.SendRequest[T](c.conn, ctx, method, params)
}

func (c *Client) Prompt(ctx context.Context, sessionID string, text string, meta map[string]any) (PromptResponse, error) {
	return c.PromptParts(ctx, sessionID, []json.RawMessage{
		mustMarshalRaw(TextContent{Type: "text", Text: text}),
	}, meta)
}

func (c *Client) PromptParts(ctx context.Context, sessionID string, prompt []json.RawMessage, meta map[string]any) (PromptResponse, error) {
	call, err := c.PreparePromptParts(sessionID, prompt, meta)
	if err != nil {
		return PromptResponse{}, err
	}
	if err := call.Dispatch(ctx); err != nil {
		call.Abandon()
		return PromptResponse{}, err
	}
	response, err := call.Wait(ctx)
	if err != nil {
		call.Abandon()
	}
	return response, err
}

// PromptCall transfers response ownership from request admission to the Turn
// producer without transferring transport ownership.
type PromptCall struct {
	inner *acpsdk.PreparedRequest[PromptResponse]
}

func (c *Client) PreparePromptParts(sessionID string, prompt []json.RawMessage, meta map[string]any) (*PromptCall, error) {
	if c == nil || c.conn == nil {
		return nil, errors.New("acp client is unavailable")
	}
	_ = meta
	call, err := acpsdk.PrepareRequest[PromptResponse](c.conn, MethodSessionPrompt, PromptRequest{
		SessionID: sessionID,
		Prompt:    append([]json.RawMessage(nil), prompt...),
	})
	if err != nil {
		return nil, err
	}
	return &PromptCall{inner: call}, nil
}

func (c *PromptCall) Dispatch(ctx context.Context) error {
	if c == nil || c.inner == nil {
		return errors.New("acp prompt call is unavailable")
	}
	return c.inner.Dispatch(ctx, acpsdk.DispatchOptions{})
}

func (c *PromptCall) DispatchWithAbort(ctx context.Context, abort func()) error {
	if c == nil || c.inner == nil {
		return errors.New("acp prompt call is unavailable")
	}
	return c.inner.Dispatch(ctx, acpsdk.DispatchOptions{Abort: adaptAbort(abort)})
}

func (c *PromptCall) ObserveAuthRequired(observer func() error) error {
	if c == nil || c.inner == nil {
		return errors.New("acp prompt call is unavailable")
	}
	return c.inner.ObserveResponse(func(_ context.Context, response acpsdk.RPCResponse) error {
		if response.Error == nil || response.Error.Code != ErrorCodeAuthRequired || observer == nil {
			return nil
		}
		return observer()
	})
}

func (c *PromptCall) Wait(ctx context.Context) (PromptResponse, error) {
	if c == nil || c.inner == nil {
		return PromptResponse{}, errors.New("acp prompt call is unavailable")
	}
	return c.inner.Wait(ctx)
}

func (c *PromptCall) Abandon() {
	if c != nil && c.inner != nil {
		c.inner.Abandon()
	}
}

func (c *Client) SteerPartsWithAbort(
	ctx context.Context,
	sessionID string,
	prompt []json.RawMessage,
	meta map[string]json.RawMessage,
	abort func(),
) (SessionSteeringResponse, error) {
	if c == nil || c.conn == nil {
		return SessionSteeringResponse{}, errors.New("acp client is unavailable")
	}
	call, err := acpsdk.PrepareRequest[SessionSteeringResponse](c.conn, MethodSessionSteering, SessionSteeringRequest{
		SessionID: sessionID,
		Prompt:    append([]json.RawMessage(nil), prompt...),
		Meta:      cloneRawMessages(meta),
	})
	if err != nil {
		return SessionSteeringResponse{}, err
	}
	if err := call.Dispatch(ctx, acpsdk.DispatchOptions{Abort: adaptAbort(abort)}); err != nil {
		call.Abandon()
		return SessionSteeringResponse{}, err
	}
	response, err := call.Wait(ctx)
	if err != nil {
		call.Abandon()
	}
	return response, err
}

func adaptAbort(abort func()) func(error) error {
	if abort == nil {
		return nil
	}
	return func(error) error {
		abort()
		return nil
	}
}

func cloneRawMessages(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	if c == nil || c.conn == nil {
		return errors.New("acp client is unavailable")
	}
	return c.conn.SendNotification(ctx, MethodSessionCancel, CancelRequest{SessionId: acpsdk.SessionId(sessionID)})
}

func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			errs = append(errs, err)
		}
		if err := c.conn.Wait(ctx); err != nil &&
			!errors.Is(err, acpsdk.ErrConnectionClosed) && !errors.Is(err, acpsdk.ErrPeerClosed) {
			errs = append(errs, err)
		}
	}
	if c.proc != nil {
		graceCtx, cancel := context.WithTimeout(ctx, processShutdownGrace)
		waitErr := c.proc.Wait(graceCtx)
		graceExpired := graceCtx.Err() != nil
		cancel()
		if !graceExpired {
			errs = append(errs, waitErr)
		} else {
			closeErr := c.proc.Close()
			if closeErr != nil {
				errs = append(errs, closeErr)
			}
			if ctx.Err() != nil {
				errs = append(errs, context.Cause(ctx))
			} else {
				_ = c.proc.Wait(ctx)
			}
		}
	}
	c.releaseEndpoint()
	return errors.Join(errs...)
}

func (c *Client) releaseEndpoint() {
	if c == nil {
		return
	}
	c.releaseOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (c *Client) StderrTail(limit int) string {
	if c == nil || limit <= 0 {
		return ""
	}
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	data := c.stderrBuf.Bytes()
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return strings.TrimSpace(string(data))
}

// handleMethod restores the ACP request/notification split that the SDK's
// shared MethodHandler leaves to the adapter. AfterResponse availability is the
// SDK's public request-context signal; this client does not otherwise need the
// registered no-op hook.
func (c *Client) handleMethod(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	err := acpsdk.AfterResponse(ctx, func(context.Context) error { return nil })
	switch {
	case err == nil:
		return c.handleRequest(ctx, method, params)
	case errors.Is(err, acpsdk.ErrAfterResponseUnavailable):
		return c.handleNotification(method, params)
	default:
		return nil, acpsdk.NewInternalError(map[string]any{"error": err.Error()})
	}
}

func (c *Client) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	switch method {
	case MethodSessionReqPermission:
		var req RequestPermissionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if c.cfg.OnPermissionRequest != nil {
			resp, err := c.cfg.OnPermissionRequest(ctx, req)
			if err != nil {
				return nil, acpsdk.NewInternalError(map[string]any{"error": err.Error()})
			}
			return resp, nil
		}
		return PermissionSelectedOutcome("reject_once"), nil
	default:
		return nil, acpsdk.NewMethodNotFound(method)
	}
}

func (c *Client) handleNotification(method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	switch method {
	case MethodSessionUpdate:
		c.handleUpdate(params)
		return nil, nil
	default:
		return nil, acpsdk.NewMethodNotFound(method)
	}
}

func (c *Client) handleUpdate(params json.RawMessage) {
	if c == nil || c.cfg.OnUpdate == nil {
		return
	}
	var note SessionNotification
	if err := decodeParams(params, &note); err != nil {
		return
	}
	update, err := decodeUpdate(note.Update)
	if err != nil || update == nil {
		return
	}
	c.cfg.OnUpdate(UpdateEnvelope{
		SessionID: strings.TrimSpace(note.SessionID),
		Update:    NormalizeInboundUpdate(update),
		Raw:       append(json.RawMessage(nil), note.Update...),
	})
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func decodeUpdate(raw json.RawMessage) (Update, error) {
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.SessionUpdate {
	case UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
		var update ContentChunk
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateAvailableCmds, UpdateConfigOption, UpdateCurrentMode, UpdateSessionInfo:
		return decodeStandardSessionStateUpdate(raw, probe.SessionUpdate)
	default:
		return schema.DecodeUpdateJSON(raw)
	}
}

func SupportsSessionSteering(response InitializeResponse) (bool, error) {
	capability, err := schema.DecodeSessionSteeringCapability(response.Meta)
	return capability.Supported, err
}

func ErrorCode(err error) (int, bool) {
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr == nil {
		return 0, false
	}
	return requestErr.Code, true
}

// DispatchMayHaveCommitted retains Caelis's product recovery distinction. A
// peer RequestError is a completed rejection, while transport ambiguity or a
// successful response that cannot be decoded must never be retried blindly.
func DispatchMayHaveCommitted(err error) bool {
	if err == nil {
		return false
	}
	var requestErr *acpsdk.RequestError
	if errors.As(err, &requestErr) {
		return false
	}
	var decodeErr *acpsdk.ResponseDecodeError
	if errors.As(err, &decodeErr) {
		return true
	}
	state, ok := acpsdk.RequestSubmissionStateOf(err)
	return ok && state != acpsdk.RequestSubmissionNotStarted
}

// SubmissionProvenNotStarted reports the SDK's positive proof that the
// transport writer was never invoked and can no longer be invoked. A false
// result is not retry-safe: it includes Possible, Pending, and unknown errors.
func SubmissionProvenNotStarted(err error) bool {
	state, ok := acpsdk.RequestSubmissionStateOf(err)
	return ok && state == acpsdk.RequestSubmissionNotStarted
}

// IsConnectionError identifies transport loss without parsing peer RequestError
// text or assigning it a protocol error code.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, acpsdk.ErrConnectionClosed) || errors.Is(err, acpsdk.ErrPeerClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection closed before response") ||
		strings.Contains(text, "file already closed") ||
		strings.Contains(text, "use of closed file")
}

func mustMarshalRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func absoluteWorkDir(workDir string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" || filepath.IsAbs(workDir) {
		return workDir, nil
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve ACP work directory: %w", err)
	}
	return abs, nil
}

func mergedEnv(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+values[name])
	}
	return out
}

type stderrBufferWriter struct{ client *Client }

func (w stderrBufferWriter) Write(p []byte) (int, error) {
	if w.client == nil || len(p) == 0 {
		return len(p), nil
	}
	w.client.stderrMu.Lock()
	defer w.client.stderrMu.Unlock()
	const limit = 32 * 1024
	if w.client.stderrBuf.Len()+len(p) > limit {
		trim := w.client.stderrBuf.Len() + len(p) - limit
		if trim >= w.client.stderrBuf.Len() {
			w.client.stderrBuf.Reset()
		} else {
			rest := append([]byte(nil), w.client.stderrBuf.Bytes()[trim:]...)
			w.client.stderrBuf.Reset()
			_, _ = w.client.stderrBuf.Write(rest)
		}
	}
	_, err := w.client.stderrBuf.Write(p)
	return len(p), err
}
