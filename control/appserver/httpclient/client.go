// Package httpclient implements the principal-bound Control Session client
// over the versioned HTTP/SSE wire protocol.
package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

const (
	defaultRemoteEventBuffer = 128
	defaultRemoteMaxEvent    = 8 << 20
	maxRemoteResponseBody    = 4 << 20
)

// Config configures one authenticated, principal-bound Control
// client. BaseURL is the server origin, without the Control API prefix.
type Config struct {
	BaseURL       string
	BearerToken   string
	HTTPClient    *http.Client
	EventBuffer   int
	MaxEventBytes int
	Compatibility appserver.CompatibilityPolicy
}

// Client is the typed HTTP/SSE implementation of the bounded Control Session
// client contract.
type Client struct {
	baseURL       *url.URL
	token         string
	httpClient    *http.Client
	eventBuffer   int
	maxEventBytes int
	compatibility appserver.CompatibilityPolicy
}

// RemoteError is an HTTP failure that did not carry a Control CommandResult.
type RemoteError struct {
	StatusCode int
	Detail     string
	Code       errorcode.Code
	Kind       appserver.ErrorKind
}

func (e *RemoteError) Error() string {
	if e == nil {
		return "control http client: remote request failed"
	}
	if strings.TrimSpace(e.Detail) == "" {
		return fmt.Sprintf("control http client: remote request failed with HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("control http client: remote request failed with HTTP %d: %s", e.StatusCode, e.Detail)
}

// ErrorCode preserves the transport-neutral category carried by the Control
// wire. Status mapping remains a compatibility fallback for older servers.
func (e *RemoteError) ErrorCode() errorcode.Code {
	if e == nil {
		return errorcode.Unknown
	}
	return normalizeRemoteErrorCode(e.Code, e.StatusCode)
}

// Is restores exact Control error identities used by client recovery logic.
func (e *RemoteError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case appserver.ErrorKindSessionClosed:
		return target == appserver.ErrSessionClosed
	case appserver.ErrorKindUnauthorized:
		return target == appserver.ErrUnauthorized
	case appserver.ErrorKindOperationConflict:
		return target == appserver.ErrOperationConflict
	case appserver.ErrorKindStateRevisionConflict:
		return target == appserver.ErrStateRevisionConflict
	default:
		return false
	}
}

// New constructs one authenticated Control HTTP client.
func New(config Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("control http client: parse remote base URL: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("control http client: remote base URL must use http or https")
	}
	if baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("control http client: remote base URL must be an origin without credentials, query, or fragment")
	}
	host := strings.TrimSpace(baseURL.Hostname())
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if baseURL.Scheme == "http" && !loopback {
		return nil, errors.New("control http client: non-loopback remote base URL requires https")
	}
	if path := strings.TrimSpace(baseURL.EscapedPath()); path != "" && path != "/" {
		return nil, errors.New("control http client: remote base URL must not include a path")
	}
	token := strings.TrimSpace(config.BearerToken)
	if token == "" {
		return nil, errors.New("control http client: remote bearer token is required")
	}
	eventBuffer := config.EventBuffer
	if eventBuffer <= 0 {
		eventBuffer = defaultRemoteEventBuffer
	}
	maxEventBytes := config.MaxEventBytes
	if maxEventBytes <= 0 {
		maxEventBytes = defaultRemoteMaxEvent
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	baseURL.Path = wirev1.APIPrefix
	baseURL.RawPath = ""
	compatibility := config.Compatibility
	if err := compatibility.Validate(); err != nil {
		return nil, fmt.Errorf("control http client: %w", err)
	}
	return &Client{
		baseURL:       baseURL,
		token:         token,
		httpClient:    client,
		eventBuffer:   eventBuffer,
		maxEventBytes: maxEventBytes,
		compatibility: compatibility,
	}, nil
}

func (c *Client) Initialize(ctx context.Context) (appserver.ServerInfo, error) {
	response, err := c.do(ctx, http.MethodGet, "/initialize", nil, nil, nil)
	if err != nil {
		return appserver.ServerInfo{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return appserver.ServerInfo{}, err
	}
	var result appserver.ServerInfo
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return appserver.ServerInfo{}, fmt.Errorf("control http client: decode server compatibility: %w", err)
	}
	if err := c.compatibility.Accept(result); err != nil {
		return appserver.ServerInfo{}, err
	}
	return result, nil
}

// Health reports process liveness without selecting a Session.
func (c *Client) Health(ctx context.Context) (appserver.HostStatus, error) {
	return c.hostStatus(ctx, "/healthz", http.StatusOK)
}

// Readiness reports whether recovery and listener publication have completed.
// A not-ready Host returns a decoded status with Ready=false and no transport
// error so launchers can wait without probing Session operations.
func (c *Client) Readiness(ctx context.Context) (appserver.HostStatus, error) {
	return c.hostStatus(ctx, "/readyz", http.StatusOK, http.StatusServiceUnavailable)
}

// ShutdownHost requests graceful quiesce of the authenticated Control Host.
// The response acknowledges the exact instance before listener shutdown starts.
func (c *Client) ShutdownHost(ctx context.Context) (appserver.HostStatus, error) {
	response, err := c.do(ctx, http.MethodPost, "/host/shutdown", nil, nil, nil)
	if err != nil {
		return appserver.HostStatus{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return appserver.HostStatus{}, err
	}
	var status appserver.HostStatus
	if err := wirev1.Unmarshal(raw, &status); err != nil {
		return appserver.HostStatus{}, fmt.Errorf("control http client: decode Host shutdown response: %w", err)
	}
	return status, nil
}

func (c *Client) hostStatus(ctx context.Context, path string, accepted ...int) (appserver.HostStatus, error) {
	if c == nil || c.baseURL == nil {
		return appserver.HostStatus{}, errors.New("control http client: client is unavailable")
	}
	endpoint := *c.baseURL
	endpoint.Path = path
	endpoint.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return appserver.HostStatus{}, fmt.Errorf("control http client: build Host status request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return appserver.HostStatus{}, fmt.Errorf("control http client: Host status request: %w", err)
	}
	defer response.Body.Close()
	allowed := false
	for _, status := range accepted {
		if response.StatusCode == status {
			allowed = true
			break
		}
	}
	if !allowed {
		raw, readErr := readRemoteResponse(response)
		if readErr != nil {
			return appserver.HostStatus{}, readErr
		}
		return appserver.HostStatus{}, fmt.Errorf("control http client: unexpected Host status HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBody+1))
	if err != nil {
		return appserver.HostStatus{}, fmt.Errorf("control http client: read Host status: %w", err)
	}
	if len(raw) > maxRemoteResponseBody {
		return appserver.HostStatus{}, errors.New("control http client: Host status response is too large")
	}
	var status appserver.HostStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return appserver.HostStatus{}, fmt.Errorf("control http client: decode Host status: %w", err)
	}
	return status, nil
}

func (c *Client) ListSessions(ctx context.Context, request appserver.ListSessionsRequest) (session.SessionList, error) {
	query := make(url.Values)
	if request.WorkspaceKey != "" {
		query.Set("workspace_key", request.WorkspaceKey)
	}
	if request.CWD != "" {
		query.Set("cwd", request.CWD)
	}
	if request.Cursor != "" {
		query.Set("cursor", request.Cursor)
	}
	if request.Limit > 0 {
		query.Set("limit", strconv.Itoa(request.Limit))
	}
	response, err := c.do(ctx, http.MethodGet, "/sessions", query, nil, nil)
	if err != nil {
		return session.SessionList{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return session.SessionList{}, err
	}
	var result session.SessionList
	if err := json.Unmarshal(raw, &result); err != nil {
		return session.SessionList{}, fmt.Errorf("control http client: decode Session list: %w", err)
	}
	return result, nil
}

func (c *Client) CreateSession(ctx context.Context, request appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions", request.WriteBase, request)
}

func (c *Client) CloseSession(ctx context.Context, request appserver.CloseSessionRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodDelete, "/sessions/"+sessionID, request.WriteBase, request)
}

func (c *Client) CompactSession(ctx context.Context, request appserver.CompactSessionRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/compact", request.WriteBase, request)
}

func (c *Client) InspectSession(ctx context.Context, request appserver.StateRequest) (appserver.SessionState, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.SessionState{}, err
	}
	response, err := c.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/state", nil, nil, nil)
	if err != nil {
		return appserver.SessionState{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return appserver.SessionState{}, err
	}
	var result appserver.SessionState
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return appserver.SessionState{}, fmt.Errorf("control http client: decode Session state: %w", err)
	}
	if err := c.validateRemoteState(result); err != nil {
		return appserver.SessionState{}, err
	}
	return result, nil
}

func (c *Client) SessionStatus(ctx context.Context, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	path := "/status"
	if strings.TrimSpace(request.SessionID) != "" {
		sessionID, err := remotePathID("session", request.SessionID)
		if err != nil {
			return controlstatus.StatusSnapshot{}, err
		}
		path = "/sessions/" + sessionID + "/status"
	}
	query := make(url.Values)
	if request.WorkspaceKey != "" {
		query.Set("workspace_key", request.WorkspaceKey)
	}
	if request.CWD != "" {
		query.Set("cwd", request.CWD)
	}
	if request.Surface != "" {
		query.Set("surface", request.Surface)
	}
	if request.IncludeDiagnostics {
		query.Set("diagnostics", "true")
	}
	if request.IncludeWorkspaceTrustRequirement {
		query.Set("workspace_trust_requirement", "true")
	}
	response, err := c.do(ctx, http.MethodGet, path, query, nil, nil)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	var result controlstatus.StatusSnapshot
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return controlstatus.StatusSnapshot{}, fmt.Errorf("control http client: decode Session status: %w", err)
	}
	return result, nil
}

func (c *Client) Reconnect(ctx context.Context, request appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.ReconnectResult{}, err
	}
	query := make(url.Values)
	if request.Cursor != "" {
		query.Set("after", request.Cursor)
	}
	response, err := c.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/reconnect", query, nil, nil)
	if err != nil {
		return appserver.ReconnectResult{}, err
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "text/event-stream" {
		response.Body.Close()
		return appserver.ReconnectResult{}, errors.New("control http client: reconnect response is not an SSE stream")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), c.maxEventBytes)
	frame, err := readRemoteSSEFrame(scanner)
	if err != nil {
		response.Body.Close()
		return appserver.ReconnectResult{}, fmt.Errorf("control http client: read reconnect bootstrap: %w", err)
	}
	if frame.event != wirev1.BootstrapEventName {
		response.Body.Close()
		return appserver.ReconnectResult{}, fmt.Errorf("control http client: first reconnect event is %q, want %q", frame.event, wirev1.BootstrapEventName)
	}
	var state appserver.SessionState
	if err := wirev1.Unmarshal(frame.data, &state); err != nil {
		response.Body.Close()
		return appserver.ReconnectResult{}, fmt.Errorf("control http client: decode reconnect bootstrap: %w", err)
	}
	if state.SessionID != strings.TrimSpace(request.SessionID) {
		response.Body.Close()
		return appserver.ReconnectResult{}, errors.New("control http client: reconnect bootstrap Session does not match the request")
	}
	if err := c.validateRemoteState(state); err != nil {
		response.Body.Close()
		return appserver.ReconnectResult{}, err
	}
	subscription := newRemoteSubscription(response, scanner)
	return appserver.ReconnectResult{State: state, Subscription: subscription}, nil
}

func (c *Client) Prompt(ctx context.Context, request appserver.PromptRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/prompt", request.WriteBase, request)
}

func (c *Client) Steer(ctx context.Context, request appserver.SteerRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/steer", request.WriteBase, request)
}

func (c *Client) Cancel(ctx context.Context, request appserver.CancelRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/cancel", request.WriteBase, request)
}

func (c *Client) ResolveApproval(ctx context.Context, request appserver.ResolveApprovalRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	requestID, err := remotePathID("approval request", request.ApprovalRequestID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(
		ctx,
		http.MethodPost,
		"/sessions/"+sessionID+"/approvals/"+requestID+"/resolve",
		request.WriteBase,
		request,
	)
}

func (c *Client) doCommand(
	ctx context.Context,
	method string,
	path string,
	write appserver.WriteBase,
	body any,
) (appserver.CommandResult, error) {
	if strings.TrimSpace(write.OperationID) == "" {
		return appserver.CommandResult{}, errors.New("control http client: remote command operation ID is required")
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", strings.TrimSpace(write.OperationID))
	if write.ExpectedRevision != nil {
		headers.Set("If-Match", strconv.Quote(strconv.FormatUint(*write.ExpectedRevision, 10)))
	}
	response, err := c.doRequest(ctx, method, path, nil, body, headers)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBody+1))
	if readErr != nil {
		return appserver.CommandResult{}, readErr
	}
	if len(raw) > maxRemoteResponseBody {
		return appserver.CommandResult{}, errors.New("control http client: remote response exceeds the size limit")
	}
	var result appserver.CommandResult
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return appserver.CommandResult{}, remoteError(response.StatusCode, raw)
		}
		return appserver.CommandResult{}, fmt.Errorf("control http client: decode Command result: %w", err)
	}
	expectedStatus, knownOutcome := commandOutcomeStatus(result.Outcome)
	if !knownOutcome || response.StatusCode != expectedStatus {
		return result, &RemoteError{
			StatusCode: response.StatusCode,
			Detail:     "HTTP status contradicts the Control command outcome",
		}
	}
	switch result.Outcome {
	case appserver.OutcomeCommitted, appserver.OutcomeAccepted:
		return result, nil
	case appserver.OutcomeConflicted, appserver.OutcomeRejected, appserver.OutcomeUnknown:
		detail := strings.TrimSpace(result.Detail)
		if detail == "" {
			detail = string(result.Outcome)
		}
		return result, appserver.NewOutcomeError(result.Outcome, &RemoteError{
			StatusCode: response.StatusCode,
			Detail:     detail,
			Code:       result.ErrorCode,
			Kind:       result.ErrorKind,
		})
	}
	return result, errors.New("control http client: unsupported Control command outcome")
}

func (c *Client) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	headers http.Header,
) (*http.Response, error) {
	response, err := c.doRequest(ctx, method, path, query, body, headers)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBody+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		return nil, remoteError(response.StatusCode, raw)
	}
	return response, nil
}

func (c *Client) doRequest(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	headers http.Header,
) (*http.Response, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return nil, errors.New("control http client: nil remote client")
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := wirev1.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("control http client: encode remote request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func readRemoteResponse(response *http.Response) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRemoteResponseBody {
		return nil, errors.New("control http client: remote response exceeds the size limit")
	}
	return raw, nil
}

func remoteError(status int, raw []byte) error {
	var response struct {
		Error string              `json:"error"`
		Code  errorcode.Code      `json:"code"`
		Kind  appserver.ErrorKind `json:"kind"`
	}
	_ = json.Unmarshal(raw, &response)
	return &RemoteError{
		StatusCode: status,
		Detail:     strings.TrimSpace(response.Error),
		Code:       response.Code,
		Kind:       response.Kind,
	}
}

func normalizeRemoteErrorCode(code errorcode.Code, status int) errorcode.Code {
	switch code {
	case errorcode.InvalidArgument,
		errorcode.NotFound,
		errorcode.AlreadyExists,
		errorcode.Conflict,
		errorcode.PermissionDenied,
		errorcode.Unauthenticated,
		errorcode.FailedPrecondition,
		errorcode.ResourceExhausted,
		errorcode.RateLimited,
		errorcode.Overloaded,
		errorcode.Timeout,
		errorcode.Cancelled,
		errorcode.Interrupted,
		errorcode.Unavailable,
		errorcode.Unsupported,
		errorcode.UnknownOutcome,
		errorcode.Internal:
		return code
	}
	switch status {
	case http.StatusBadRequest:
		return errorcode.InvalidArgument
	case http.StatusUnauthorized:
		return errorcode.Unauthenticated
	case http.StatusForbidden:
		return errorcode.PermissionDenied
	case http.StatusNotFound:
		return errorcode.NotFound
	case http.StatusConflict:
		return errorcode.Conflict
	case http.StatusServiceUnavailable:
		return errorcode.Unavailable
	case http.StatusInternalServerError:
		return errorcode.Internal
	default:
		return errorcode.Unknown
	}
}

func remotePathID(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("control http client: remote %s ID is required", kind)
	}
	if strings.ContainsAny(value, "/?#") {
		return "", fmt.Errorf("control http client: remote %s ID contains a path delimiter", kind)
	}
	return value, nil
}

func (c *Client) validateRemoteState(state appserver.SessionState) error {
	// Session snapshots repeat wire-version metadata but do not repeat
	// Host-scoped capability advertisement. Required capabilities are fenced by
	// Initialize; snapshot validation checks only the carried versions.
	policy := c.compatibility
	policy.RequiredCaps = nil
	return policy.Accept(appserver.ServerInfo{
		ProtocolVersion: state.ProtocolVersion,
		EnvelopeVersion: state.EnvelopeVersion,
		APIVersion:      state.APIVersion,
	})
}

func commandOutcomeStatus(outcome appserver.Outcome) (int, bool) {
	switch outcome {
	case appserver.OutcomeCommitted:
		return http.StatusOK, true
	case appserver.OutcomeAccepted, appserver.OutcomeUnknown:
		return http.StatusAccepted, true
	case appserver.OutcomeConflicted:
		return http.StatusConflict, true
	case appserver.OutcomeRejected:
		return http.StatusBadRequest, true
	default:
		return 0, false
	}
}

type remoteSSEFrame struct {
	event string
	id    string
	data  []byte
}

func readRemoteSSEFrame(scanner *bufio.Scanner) (remoteSSEFrame, error) {
	for {
		var frame remoteSSEFrame
		var data []string
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if frame.event == "" && frame.id == "" && len(data) == 0 {
					continue
				}
				frame.data = []byte(strings.Join(data, "\n"))
				return frame, nil
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				frame.event = value
			case "id":
				frame.id = value
			case "data":
				data = append(data, value)
			}
		}
		if err := scanner.Err(); err != nil {
			return remoteSSEFrame{}, err
		}
		return remoteSSEFrame{}, io.EOF
	}
}

type remoteSubscription struct {
	response *http.Response
	scanner  *bufio.Scanner

	deliveries chan appserver.FeedDelivery
	stop       chan struct{}
	done       chan struct{}

	closeOnce sync.Once
	mu        sync.RWMutex
	err       error
	closed    bool
}

func newRemoteSubscription(response *http.Response, scanner *bufio.Scanner) *remoteSubscription {
	subscription := &remoteSubscription{
		response: response, scanner: scanner,
		deliveries: make(chan appserver.FeedDelivery), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go subscription.read()
	return subscription
}

func (s *remoteSubscription) Deliveries() <-chan appserver.FeedDelivery { return s.deliveries }

func (s *remoteSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.stop)
		_ = s.response.Body.Close()
	})
	<-s.done
	return nil
}

func (s *remoteSubscription) Err() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

func (s *remoteSubscription) read() {
	defer close(s.done)
	defer close(s.deliveries)
	defer s.response.Body.Close()
	for {
		frame, err := readRemoteSSEFrame(s.scanner)
		if err != nil {
			if s.wasClosed() {
				return
			}
			if errors.Is(err, io.EOF) {
				s.setError(errorcode.New(errorcode.Unavailable, "control http client: Session feed ended without a done event"))
			} else {
				s.setError(err)
			}
			return
		}
		switch frame.event {
		case wirev1.FeedDeliveryEventName:
			var wire wirev1.FeedDelivery
			if err := wirev1.Unmarshal(frame.data, &wire); err != nil {
				s.setError(fmt.Errorf("control http client: decode Session delivery: %w", err))
				return
			}
			delivery, err := decodeFeedDelivery(wire)
			if err != nil {
				s.setError(err)
				return
			}
			if frame.id != "" && delivery.NextCursor != frame.id {
				s.setError(errors.New("control http client: SSE id does not match Session delivery cursor"))
				return
			}
			if !s.publish(delivery) {
				return
			}
		case wirev1.FeedDoneEventName:
			return
		case wirev1.FeedErrorEventName:
			var wireErr wirev1.TaskStreamError
			if err := json.Unmarshal(frame.data, &wireErr); err != nil {
				s.setError(fmt.Errorf("control http client: decode Session feed error: %w", err))
				return
			}
			s.setError(wirev1.DecodeTaskStreamError(wireErr))
			return
		default:
			s.setError(fmt.Errorf("control http client: unsupported reconnect SSE event %q", frame.event))
			return
		}
	}
}

func (s *remoteSubscription) publish(delivery appserver.FeedDelivery) bool {
	select {
	case s.deliveries <- delivery:
		return true
	case <-s.stop:
		return false
	}
}

func decodeFeedDelivery(wire wirev1.FeedDelivery) (appserver.FeedDelivery, error) {
	delivery := appserver.FeedDelivery{
		Kind: appserver.FeedDeliveryKind(wire.Kind), Source: appserver.FeedSourceClass(wire.Source),
		SnapshotID: wire.SnapshotID, Page: wire.Page, NextCursor: wire.NextCursor,
		Events: make([]eventstream.Envelope, 0, len(wire.Events)),
	}
	for _, raw := range wire.Events {
		envelope, err := wirev1.UnmarshalEnvelope(raw)
		if err != nil {
			return appserver.FeedDelivery{}, fmt.Errorf("control http client: decode Session Envelope: %w", err)
		}
		delivery.Events = append(delivery.Events, envelope)
	}
	return delivery, nil
}

func (s *remoteSubscription) setError(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *remoteSubscription) wasClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

var _ appserver.SessionClient = (*Client)(nil)
var _ appserver.StatusClient = (*Client)(nil)
var _ appserver.FeedSubscription = (*remoteSubscription)(nil)
