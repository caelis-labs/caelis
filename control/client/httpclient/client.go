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
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/wirev1"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
}

// Client is the typed HTTP/SSE implementation of the bounded Control Session
// client contract.
type Client struct {
	baseURL       *url.URL
	token         string
	httpClient    *http.Client
	eventBuffer   int
	maxEventBytes int
}

// RemoteError is an HTTP failure that did not carry a Control CommandResult.
type RemoteError struct {
	StatusCode int
	Detail     string
	Code       errorcode.Code
	Kind       controlclient.ErrorKind
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
	case controlclient.ErrorKindSessionClosed:
		return target == controlclient.ErrSessionClosed
	case controlclient.ErrorKindUnauthorized:
		return target == controlclient.ErrUnauthorized
	case controlclient.ErrorKindOperationConflict:
		return target == controlclient.ErrOperationConflict
	case controlclient.ErrorKindStateRevisionConflict:
		return target == controlclient.ErrStateRevisionConflict
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
	return &Client{
		baseURL:       baseURL,
		token:         token,
		httpClient:    client,
		eventBuffer:   eventBuffer,
		maxEventBytes: maxEventBytes,
	}, nil
}

func (c *Client) Initialize(ctx context.Context) (controlclient.ServerInfo, error) {
	response, err := c.do(ctx, http.MethodGet, "/initialize", nil, nil, nil)
	if err != nil {
		return controlclient.ServerInfo{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return controlclient.ServerInfo{}, err
	}
	var result controlclient.ServerInfo
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return controlclient.ServerInfo{}, fmt.Errorf("control http client: decode server compatibility: %w", err)
	}
	if err := validateRemoteVersions(result.ProtocolVersion, result.EnvelopeVersion, result.APIVersion); err != nil {
		return controlclient.ServerInfo{}, err
	}
	return result, nil
}

func (c *Client) ListSessions(ctx context.Context, request controlclient.ListSessionsRequest) (session.SessionList, error) {
	query := make(url.Values)
	if request.WorkspaceKey != "" {
		query.Set("workspace_key", request.WorkspaceKey)
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

func (c *Client) CreateSession(ctx context.Context, request controlclient.CreateSessionRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions", request.WriteBase, request)
}

func (c *Client) CloseSession(ctx context.Context, request controlclient.CloseSessionRequest) (controlclient.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodDelete, "/sessions/"+sessionID, request.WriteBase, request)
}

func (c *Client) CompactSession(ctx context.Context, request controlclient.CompactSessionRequest) (controlclient.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/compact", request.WriteBase, request)
}

func (c *Client) InspectSession(ctx context.Context, request controlclient.StateRequest) (controlclient.SessionState, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.SessionState{}, err
	}
	response, err := c.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/state", nil, nil, nil)
	if err != nil {
		return controlclient.SessionState{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return controlclient.SessionState{}, err
	}
	var result controlclient.SessionState
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return controlclient.SessionState{}, fmt.Errorf("control http client: decode Session state: %w", err)
	}
	if err := validateRemoteState(result); err != nil {
		return controlclient.SessionState{}, err
	}
	return result, nil
}

func (c *Client) SessionStatus(ctx context.Context, request controlclient.StatusRequest) (controlstatus.StatusSnapshot, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	query := make(url.Values)
	if request.Surface != "" {
		query.Set("surface", request.Surface)
	}
	if request.IncludeDiagnostics {
		query.Set("diagnostics", "true")
	}
	response, err := c.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/status", query, nil, nil)
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

func (c *Client) Reconnect(ctx context.Context, request controlclient.ReconnectRequest) (controlclient.ReconnectResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.ReconnectResult{}, err
	}
	query := make(url.Values)
	if request.Cursor != "" {
		query.Set("after", request.Cursor)
	}
	response, err := c.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/reconnect", query, nil, nil)
	if err != nil {
		return controlclient.ReconnectResult{}, err
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "text/event-stream" {
		response.Body.Close()
		return controlclient.ReconnectResult{}, errors.New("control http client: reconnect response is not an SSE stream")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), c.maxEventBytes)
	frame, err := readRemoteSSEFrame(scanner)
	if err != nil {
		response.Body.Close()
		return controlclient.ReconnectResult{}, fmt.Errorf("control http client: read reconnect bootstrap: %w", err)
	}
	if frame.event != wirev1.BootstrapEventName {
		response.Body.Close()
		return controlclient.ReconnectResult{}, fmt.Errorf("control http client: first reconnect event is %q, want %q", frame.event, wirev1.BootstrapEventName)
	}
	var state controlclient.SessionState
	if err := wirev1.Unmarshal(frame.data, &state); err != nil {
		response.Body.Close()
		return controlclient.ReconnectResult{}, fmt.Errorf("control http client: decode reconnect bootstrap: %w", err)
	}
	if state.SessionID != strings.TrimSpace(request.SessionID) {
		response.Body.Close()
		return controlclient.ReconnectResult{}, errors.New("control http client: reconnect bootstrap Session does not match the request")
	}
	if err := validateRemoteState(state); err != nil {
		response.Body.Close()
		return controlclient.ReconnectResult{}, err
	}
	subscription := newRemoteSubscription(response, scanner, c.eventBuffer, strings.TrimSpace(request.Cursor))
	return controlclient.ReconnectResult{State: state, Subscription: subscription}, nil
}

func (c *Client) Prompt(ctx context.Context, request controlclient.PromptRequest) (controlclient.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/prompt", request.WriteBase, request)
}

func (c *Client) Steer(ctx context.Context, request controlclient.SteerRequest) (controlclient.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/steer", request.WriteBase, request)
}

func (c *Client) Cancel(ctx context.Context, request controlclient.CancelRequest) (controlclient.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/cancel", request.WriteBase, request)
}

func (c *Client) ResolveApproval(ctx context.Context, request controlclient.ResolveApprovalRequest) (controlclient.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	requestID, err := remotePathID("approval request", request.ApprovalRequestID)
	if err != nil {
		return controlclient.CommandResult{}, err
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
	write controlclient.WriteBase,
	body any,
) (controlclient.CommandResult, error) {
	if strings.TrimSpace(write.OperationID) == "" {
		return controlclient.CommandResult{}, errors.New("control http client: remote command operation ID is required")
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", strings.TrimSpace(write.OperationID))
	if write.ExpectedRevision != nil {
		headers.Set("If-Match", strconv.Quote(strconv.FormatUint(*write.ExpectedRevision, 10)))
	}
	response, err := c.doRequest(ctx, method, path, nil, body, headers)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBody+1))
	if readErr != nil {
		return controlclient.CommandResult{}, readErr
	}
	if len(raw) > maxRemoteResponseBody {
		return controlclient.CommandResult{}, errors.New("control http client: remote response exceeds the size limit")
	}
	var result controlclient.CommandResult
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return controlclient.CommandResult{}, remoteError(response.StatusCode, raw)
		}
		return controlclient.CommandResult{}, fmt.Errorf("control http client: decode Command result: %w", err)
	}
	expectedStatus, knownOutcome := commandOutcomeStatus(result.Outcome)
	if !knownOutcome || response.StatusCode != expectedStatus {
		return result, &RemoteError{
			StatusCode: response.StatusCode,
			Detail:     "HTTP status contradicts the Control command outcome",
		}
	}
	switch result.Outcome {
	case controlclient.OutcomeCommitted, controlclient.OutcomeAccepted:
		return result, nil
	case controlclient.OutcomeConflicted, controlclient.OutcomeRejected, controlclient.OutcomeUnknown:
		detail := strings.TrimSpace(result.Detail)
		if detail == "" {
			detail = string(result.Outcome)
		}
		return result, controlclient.NewOutcomeError(result.Outcome, &RemoteError{
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
		Error string                  `json:"error"`
		Code  errorcode.Code          `json:"code"`
		Kind  controlclient.ErrorKind `json:"kind"`
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

func validateRemoteState(state controlclient.SessionState) error {
	return validateRemoteVersions(state.ProtocolVersion, state.EnvelopeVersion, state.APIVersion)
}

func validateRemoteVersions(protocolVersion int, envelopeVersion, apiVersion string) error {
	if protocolVersion != schema.CurrentProtocolVersion {
		return fmt.Errorf("control http client: unsupported ACP protocol version %d", protocolVersion)
	}
	if envelopeVersion != controlclient.EnvelopeVersion {
		return fmt.Errorf("control http client: unsupported Envelope version %q", envelopeVersion)
	}
	if apiVersion != controlclient.HTTPAPIVersion {
		return fmt.Errorf("control http client: unsupported Control API version %q", apiVersion)
	}
	return nil
}

func commandOutcomeStatus(outcome controlclient.Outcome) (int, bool) {
	switch outcome {
	case controlclient.OutcomeCommitted:
		return http.StatusOK, true
	case controlclient.OutcomeAccepted, controlclient.OutcomeUnknown:
		return http.StatusAccepted, true
	case controlclient.OutcomeConflicted:
		return http.StatusConflict, true
	case controlclient.OutcomeRejected:
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

	backfill     chan eventstream.Envelope
	events       chan eventstream.Envelope
	backfillDone chan struct{}

	closeOnce sync.Once
	mu        sync.RWMutex
	err       error
	last      string
	closed    bool
}

func newRemoteSubscription(response *http.Response, scanner *bufio.Scanner, capacity int, initialCursor string) *remoteSubscription {
	subscription := &remoteSubscription{
		response:     response,
		scanner:      scanner,
		backfill:     make(chan eventstream.Envelope, capacity),
		events:       make(chan eventstream.Envelope, capacity),
		backfillDone: make(chan struct{}),
		last:         initialCursor,
	}
	go subscription.read()
	return subscription
}

func (s *remoteSubscription) Backfill() <-chan eventstream.Envelope { return s.backfill }
func (s *remoteSubscription) Events() <-chan eventstream.Envelope   { return s.events }
func (s *remoteSubscription) BackfillDone() <-chan struct{}         { return s.backfillDone }

func (s *remoteSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.response.Body.Close()
	})
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

func (s *remoteSubscription) LastCursor() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

func (s *remoteSubscription) read() {
	backfillOpen := true
	defer func() {
		if backfillOpen {
			close(s.backfill)
			close(s.backfillDone)
		}
		close(s.events)
		_ = s.response.Body.Close()
	}()
	for {
		frame, err := readRemoteSSEFrame(s.scanner)
		if err != nil {
			if s.wasClosed() {
				return
			}
			if errors.Is(err, io.EOF) {
				if backfillOpen {
					s.setError(errors.New("control http client: reconnect stream ended before the backfill marker"))
				} else {
					s.setError(&controlclient.FeedGapError{
						Cause:        io.ErrUnexpectedEOF,
						RetryCursor:  s.LastCursor(),
						Mode:         controlclient.ResumeModeDurableFallback,
						TransientGap: true,
					})
				}
			} else {
				s.setError(err)
			}
			return
		}
		switch frame.event {
		case wirev1.BackfillDoneEventName:
			if !backfillOpen {
				s.setError(errors.New("control http client: duplicate reconnect backfill marker"))
				return
			}
			backfillOpen = false
			close(s.backfill)
			close(s.backfillDone)
		case wirev1.ResumeEventName:
			var boundary wirev1.ResumeBoundary
			if err := json.Unmarshal(frame.data, &boundary); err != nil {
				s.setError(fmt.Errorf("control http client: decode reconnect gap: %w", err))
				return
			}
			s.setError(&controlclient.FeedGapError{
				Cause:        errors.New("control http client: remote Session feed requires reconnect"),
				RetryCursor:  boundary.BoundaryCursor,
				Mode:         boundary.ResumeMode,
				TransientGap: boundary.TransientGap,
			})
			return
		case "":
			envelope, err := wirev1.UnmarshalEnvelope(frame.data)
			if err != nil {
				s.setError(fmt.Errorf("control http client: decode remote Envelope: %w", err))
				return
			}
			if frame.id != "" && envelope.Cursor != frame.id {
				s.setError(errors.New("control http client: SSE id does not match Envelope cursor"))
				return
			}
			target := s.events
			if backfillOpen {
				target = s.backfill
			}
			select {
			case target <- envelope:
				s.mu.Lock()
				s.last = envelope.Cursor
				s.mu.Unlock()
			default:
				s.setError(&controlclient.FeedGapError{
					Cause:        controlclient.ErrSlowConsumer,
					RetryCursor:  s.LastCursor(),
					Mode:         controlclient.ResumeModeDurableFallback,
					TransientGap: true,
				})
				return
			}
		default:
			s.setError(fmt.Errorf("control http client: unsupported reconnect SSE event %q", frame.event))
			return
		}
	}
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

var _ controlclient.SessionClient = (*Client)(nil)
var _ controlclient.StatusClient = (*Client)(nil)
var _ controlclient.FeedSubscription = (*remoteSubscription)(nil)
