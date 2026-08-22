package controlserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const apiPrefix = wirev1.APIPrefix

// The HTTP envelope must accommodate the base64 expansion of the shared
// AppServer prompt-image budget plus ordinary request metadata.
const maxJSONRequestBytes = 64 << 20

const (
	resumeModeHeader      = wirev1.ResumeModeHeader
	transientGapHeader    = wirev1.TransientGapHeader
	boundaryCursorHeader  = wirev1.BoundaryCursorHeader
	resumeEventName       = wirev1.ResumeEventName
	bootstrapEventName    = wirev1.BootstrapEventName
	backfillDoneEventName = wirev1.BackfillDoneEventName
)

type resumeBoundary = wirev1.ResumeBoundary

type Authenticator interface {
	Authenticate(*http.Request) (appserver.Principal, error)
}

type AuthenticatorFunc func(*http.Request) (appserver.Principal, error)

func (f AuthenticatorFunc) Authenticate(request *http.Request) (appserver.Principal, error) {
	return f(request)
}

// HandlerConfig configures the authenticated Control HTTP handler. Shutdown is
// an optional listener-lifecycle callback; Host quiesce remains owned by the
// server runner.
type HandlerConfig struct {
	Services      appserver.AppServerServices
	Authenticator Authenticator
	AllowedHosts  []string
	Heartbeat     time.Duration
	ServerInfo    appserver.ServerInfo
	Ready         func() bool
	Shutdown      func()
}

type Server struct {
	config HandlerConfig
	mux    *http.ServeMux
	policy *requestPolicy
}

// New constructs the authenticated Control HTTP handler.
func New(config HandlerConfig) (*Server, error) {
	if err := config.Services.Validate(); err != nil {
		return nil, fmt.Errorf("controlserver: invalid AppServer services: %w", err)
	}
	if config.Authenticator == nil {
		return nil, errors.New("controlserver: authenticator is required for an HTTP handler")
	}
	if config.Heartbeat <= 0 {
		config.Heartbeat = 15 * time.Second
	}
	config.ServerInfo = normalizeServerInfo(config.ServerInfo)
	if config.Ready == nil {
		config.Ready = func() bool { return true }
	}
	policy, err := newRequestPolicy(config.AllowedHosts)
	if err != nil {
		return nil, err
	}
	server := &Server{config: config, mux: http.NewServeMux(), policy: policy}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if err := s.policy.authorize(request); err != nil {
		writeError(writer, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if hasCredentialQuery(request) {
		writeError(writer, http.StatusBadRequest, "credentials are not accepted in query parameters")
		return
	}
	s.mux.ServeHTTP(writer, request)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.readiness)
	s.mux.HandleFunc("GET "+apiPrefix+"/initialize", s.initialize)
	s.mux.HandleFunc("POST "+apiPrefix+"/host/shutdown", s.shutdown)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions", s.listSessions)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions", s.createSession)
	s.mux.HandleFunc("DELETE "+apiPrefix+"/sessions/{session_id}", s.closeSession)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/compact", s.compactSession)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/state", s.sessionState)
	s.mux.HandleFunc("GET "+apiPrefix+"/status", s.sessionStatus)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/status", s.sessionStatus)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/reconnect", s.reconnectSession)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/prompt", s.prompt)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/steer", s.steer)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/cancel", s.cancel)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/approvals/{approval_request_id}/resolve", s.resolveApproval)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/tasks", s.listTasks)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/tasks/watch", s.watchTaskDirectory)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/tasks/{task_id}/events", s.taskEvents)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/tasks/{task_id}/subscribe", s.subscribeTask)
	s.focusedRoutes()
}

func (s *Server) initialize(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePrincipal(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.config.ServerInfo)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, appserver.HostStatus{
		ServerID: s.config.ServerInfo.ServerID, InstanceID: s.config.ServerInfo.InstanceID,
		Ready: s.config.Ready(),
	})
}

func (s *Server) readiness(w http.ResponseWriter, _ *http.Request) {
	status := appserver.HostStatus{
		ServerID: s.config.ServerInfo.ServerID, InstanceID: s.config.ServerInfo.InstanceID,
		Ready: s.config.Ready(),
	}
	if !status.Ready {
		writeJSON(w, http.StatusServiceUnavailable, status)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePrincipal(w, r); !ok {
		return
	}
	if s.config.Shutdown == nil {
		writeError(w, http.StatusServiceUnavailable, "Host shutdown is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, appserver.HostStatus{
		ServerID: s.config.ServerInfo.ServerID, InstanceID: s.config.ServerInfo.InstanceID,
		Ready: false,
	})
	go s.config.Shutdown()
}

func normalizeServerInfo(info appserver.ServerInfo) appserver.ServerInfo {
	info.ProtocolVersion = schema.CurrentProtocolVersion
	info.EnvelopeVersion = appserver.EnvelopeVersion
	info.APIVersion = appserver.HTTPAPIVersion
	info.DistributionVersion = strings.TrimSpace(info.DistributionVersion)
	info.BuildID = strings.TrimSpace(info.BuildID)
	info.BuildKind = strings.TrimSpace(info.BuildKind)
	if strings.TrimSpace(info.ServerID) == "" {
		info.ServerID = appserver.ServerIdentity
	}
	info.ServerID = strings.TrimSpace(info.ServerID)
	info.InstanceID = strings.TrimSpace(info.InstanceID)
	info.Capabilities = append([]string(nil), info.Capabilities...)
	info.Transports = append([]string(nil), info.Transports...)
	return info
}

func (s *Server) principal(request *http.Request) (appserver.Principal, error) {
	principal, err := s.config.Authenticator.Authenticate(request)
	if err != nil {
		return appserver.Principal{}, errorcode.Wrap(errorcode.Unauthenticated, "controlserver: authentication failed", err)
	}
	if strings.TrimSpace(principal.ID) == "" {
		return appserver.Principal{}, errorcode.New(errorcode.Unauthenticated, "controlserver: authentication failed")
	}
	return principal, nil
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	limit := 0
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		var err error
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
	}
	result, err := s.config.Services.Sessions.ListSessions(r.Context(), principal, appserver.ListSessionsRequest{
		WorkspaceKey: r.URL.Query().Get("workspace_key"),
		CWD:          r.URL.Query().Get("cwd"),
		Cursor:       r.URL.Query().Get("cursor"),
		Limit:        limit,
	})
	writeJSONResult(w, result, err)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.CreateSessionRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, "") {
		return
	}
	result, err := s.config.Services.Sessions.CreateSession(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.CloseSessionRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Sessions.CloseSession(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) compactSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.CompactSessionRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Sessions.CompactSession(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) sessionState(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Sessions.InspectSession(r.Context(), principal, appserver.StateRequest{SessionID: r.PathValue("session_id")})
	writeJSONResult(w, result, err)
}

func (s *Server) sessionStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	diagnostics := false
	if raw := strings.TrimSpace(r.URL.Query().Get("diagnostics")); raw != "" {
		var err error
		diagnostics, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "diagnostics must be a boolean")
			return
		}
	}
	result, err := s.config.Services.Status.SessionStatus(r.Context(), principal, appserver.StatusRequest{
		SessionID:          r.PathValue("session_id"),
		WorkspaceKey:       strings.TrimSpace(r.URL.Query().Get("workspace_key")),
		CWD:                strings.TrimSpace(r.URL.Query().Get("cwd")),
		Surface:            strings.TrimSpace(r.URL.Query().Get("surface")),
		IncludeDiagnostics: diagnostics,
	})
	writeJSONResult(w, result, err)
}

func (s *Server) reconnectSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	cursor, ok := resumeCursor(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Sessions.Reconnect(r.Context(), principal, appserver.ReconnectRequest{
		SessionID: r.PathValue("session_id"),
		Cursor:    cursor,
	})
	if err != nil {
		writeMappedError(w, err)
		return
	}
	defer result.Subscription.Close()
	s.streamControlSubscription(
		w,
		r,
		result.Subscription,
		result.State,
	)
}

func (s *Server) streamControlSubscription(
	w http.ResponseWriter,
	r *http.Request,
	subscription appserver.FeedSubscription,
	state appserver.SessionState,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	initial, err := wirev1.Marshal(state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set(resumeModeHeader, string(state.ResumeMode))
	w.Header().Set(transientGapHeader, strconv.FormatBool(state.TransientGap))
	if state.BoundaryCursor != "" {
		w.Header().Set(boundaryCursorHeader, state.BoundaryCursor)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", bootstrapEventName, initial)
	flusher.Flush()
	heartbeat := time.NewTicker(s.config.Heartbeat)
	defer heartbeat.Stop()
	events := subscription.Backfill()
	backfill := true
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case envelope, open := <-events:
			if !open {
				if backfill {
					backfill = false
					_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", backfillDoneEventName)
					flusher.Flush()
					events = subscription.Events()
					continue
				}
				var gap *appserver.FeedGapError
				if errors.As(subscription.Err(), &gap) {
					retry, marshalErr := json.Marshal(resumeBoundary{
						ResumeMode: gap.Mode, TransientGap: gap.TransientGap, BoundaryCursor: gap.RetryCursor,
					})
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", resumeEventName, retry)
						flusher.Flush()
					}
				}
				return
			}
			data, err := wirev1.MarshalEnvelope(envelope)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", envelope.Cursor, data)
			flusher.Flush()
		}
	}
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.PromptRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Sessions.Prompt(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.SteerRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Sessions.Steer(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.CancelRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Sessions.Cancel(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.ResolveApprovalRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	if req.ApprovalRequestID != "" && req.ApprovalRequestID != r.PathValue("approval_request_id") {
		writeError(w, http.StatusBadRequest, "approval request id mismatch")
		return
	}
	req.ApprovalRequestID = r.PathValue("approval_request_id")
	result, err := s.config.Services.Sessions.ResolveApproval(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) requirePrincipal(w http.ResponseWriter, r *http.Request) (appserver.Principal, bool) {
	principal, err := s.principal(r)
	if err != nil {
		writeMappedError(w, err)
		return appserver.Principal{}, false
	}
	return principal, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if !strings.EqualFold(contentType, "application/json") {
		writeError(w, http.StatusBadRequest, "Content-Type must be application/json")
		return false
	}
	limited := http.MaxBytesReader(nil, r.Body, maxJSONRequestBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		if isRequestBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, jsonRequestBodyTooLargeDetail())
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON value")
		return false
	}
	if err := wirev1.DecodeRequest(raw, target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	return true
}

func isRequestBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

func jsonRequestBodyTooLargeDetail() string {
	return fmt.Sprintf("JSON request body exceeds %d MiB", maxJSONRequestBytes>>20)
}

func applyWriteHeaders(w http.ResponseWriter, r *http.Request, base *appserver.WriteBase, sessionID string) bool {
	operationValues := r.Header.Values("Idempotency-Key")
	if len(operationValues) != 1 || strings.Contains(operationValues[0], ",") {
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be provided exactly once")
		return false
	}
	operationID := strings.TrimSpace(operationValues[0])
	if operationID == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required")
		return false
	}
	if base.OperationID != "" && strings.TrimSpace(base.OperationID) != operationID {
		writeError(w, http.StatusBadRequest, "operation id contradicts Idempotency-Key")
		return false
	}
	base.OperationID = operationID
	if sessionID != "" {
		if base.SessionID != "" && strings.TrimSpace(base.SessionID) != sessionID {
			writeError(w, http.StatusBadRequest, "session id mismatch")
			return false
		}
		base.SessionID = sessionID
	}
	ifMatchValues := r.Header.Values("If-Match")
	if len(ifMatchValues) > 1 {
		writeError(w, http.StatusBadRequest, "If-Match must be provided at most once")
		return false
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		return true
	}
	if strings.Contains(ifMatch, ",") {
		writeError(w, http.StatusBadRequest, "If-Match must contain one revision")
		return false
	}
	ifMatch = strings.TrimPrefix(ifMatch, "W/")
	if len(ifMatch) < 2 || ifMatch[0] != '"' || ifMatch[len(ifMatch)-1] != '"' {
		writeError(w, http.StatusBadRequest, "If-Match revision must be a quoted decimal string")
		return false
	}
	ifMatch = ifMatch[1 : len(ifMatch)-1]
	revision, err := wirev1.ParseUint64Decimal(ifMatch)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid If-Match revision")
		return false
	}
	if base.ExpectedRevision != nil && *base.ExpectedRevision != revision {
		writeError(w, http.StatusBadRequest, "expected_revision contradicts If-Match")
		return false
	}
	base.ExpectedRevision = &revision
	return true
}

func applyHostWriteHeaders(w http.ResponseWriter, r *http.Request, base *appserver.WriteBase) bool {
	if strings.TrimSpace(base.SessionID) != "" {
		writeError(w, http.StatusBadRequest, "Host mutation must not address a Session")
		return false
	}
	return applyWriteHeaders(w, r, base, "")
}

func resumeCursor(w http.ResponseWriter, r *http.Request) (string, bool) {
	afterValues := r.URL.Query()["after"]
	if len(afterValues) > 1 {
		writeError(w, http.StatusBadRequest, "after must be provided at most once")
		return "", false
	}
	after := ""
	if len(afterValues) == 1 {
		after = strings.TrimSpace(afterValues[0])
	}
	lastValues := r.Header.Values("Last-Event-ID")
	if len(lastValues) > 1 || len(lastValues) == 1 && strings.Contains(lastValues[0], ",") {
		writeError(w, http.StatusBadRequest, "Last-Event-ID must be provided at most once")
		return "", false
	}
	last := ""
	if len(lastValues) == 1 {
		last = strings.TrimSpace(lastValues[0])
	}
	if after != "" && last != "" && after != last {
		writeError(w, http.StatusBadRequest, "after and Last-Event-ID must match")
		return "", false
	}
	if last != "" {
		return last, true
	}
	return after, true
}

func hasCredentialQuery(r *http.Request) bool {
	for key := range r.URL.Query() {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "token", "access_token", "authorization", "auth":
			return true
		}
	}
	return false
}

func writeCommandResult(w http.ResponseWriter, result appserver.CommandResult, err error) {
	status, knownOutcome := commandOutcomeStatus(result.Outcome)
	if !knownOutcome {
		status = http.StatusInternalServerError
	}
	switch result.Outcome {
	case appserver.OutcomeUnknown:
		result.Detail = "effect outcome cannot be proven"
	case appserver.OutcomeConflicted:
		result.Detail = "conflict"
	}
	if err != nil {
		if result.ErrorCode == "" {
			result.ErrorCode = errorcode.CodeOf(err)
			if result.ErrorCode == errorcode.Unknown && result.Outcome == appserver.OutcomeUnknown {
				result.ErrorCode = errorcode.UnknownOutcome
			}
		}
		if result.ErrorKind == "" {
			result.ErrorKind = appserver.ErrorKindOf(err)
		}
		mapped := statusForError(err)
		switch mapped {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeMappedError(w, err)
			return
		case http.StatusBadRequest, http.StatusConflict:
			if !knownOutcome {
				writeMappedError(w, err)
				return
			}
			// The CommandResult outcome is the recovery contract. A rejected
			// failed-precondition, for example, remains HTTP 400 rather than
			// contradicting its outcome with HTTP 409.
			if status == http.StatusConflict && strings.TrimSpace(result.Detail) != "" {
				result.Detail = "conflict"
			}
		default:
			// CommandResult.Outcome is the transport-neutral recovery contract.
			// In particular, an uncoded backend error accompanying unknown or
			// conflicted must not erase the client's 202/409 recovery path.
			switch result.Outcome {
			case appserver.OutcomeUnknown:
				result.Detail = "effect outcome cannot be proven"
			case appserver.OutcomeConflicted:
				result.Detail = "conflict"
			default:
				writeMappedError(w, err)
				return
			}
		}
	}
	writeJSON(w, status, result)
}

func commandOutcomeStatus(outcome appserver.Outcome) (int, bool) {
	var status int
	switch outcome {
	case appserver.OutcomeCommitted:
		status = http.StatusOK
	case appserver.OutcomeAccepted, appserver.OutcomeUnknown:
		status = http.StatusAccepted
	case appserver.OutcomeConflicted:
		status = http.StatusConflict
	case appserver.OutcomeRejected:
		status = http.StatusBadRequest
	default:
		return 0, false
	}
	return status, true
}

func writeJSONResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func writeError(w http.ResponseWriter, status int, detail string) {
	writeErrorIdentity(w, status, detail, errorCodeForStatus(status), "")
}

func writeErrorIdentity(w http.ResponseWriter, status int, detail string, code errorcode.Code, kind appserver.ErrorKind) {
	response := map[string]any{
		"error": strings.TrimSpace(detail),
		"code":  code,
	}
	if kind != "" {
		response["kind"] = kind
	}
	writeJSON(w, status, response)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := wirev1.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":"internal server error","code":"internal"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
