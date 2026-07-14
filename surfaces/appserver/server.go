// Package appserver maps the transport-neutral Control client contract to
// HTTP JSON and Server-Sent Events. It owns no Runtime or persistence logic.
package appserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const apiPrefix = "/api/control/v1"

const (
	resumeModeHeader     = "Caelis-Resume-Mode"
	transientGapHeader   = "Caelis-Transient-Gap"
	boundaryCursorHeader = "Caelis-Boundary-Cursor"
	resumeEventName      = "caelis.control.resume"
)

type resumeBoundary struct {
	ResumeMode     controlclient.ResumeMode `json:"resume_mode"`
	TransientGap   bool                     `json:"transient_gap,omitempty"`
	BoundaryCursor string                   `json:"boundary_cursor,omitempty"`
}

type Authenticator interface {
	Authenticate(*http.Request) (controlclient.Principal, error)
}

type AuthenticatorFunc func(*http.Request) (controlclient.Principal, error)

func (f AuthenticatorFunc) Authenticate(request *http.Request) (controlclient.Principal, error) {
	return f(request)
}

type Config struct {
	Service        controlclient.Service
	Authenticator  Authenticator
	LocalPrincipal controlclient.Principal
	Heartbeat      time.Duration
}

type Server struct {
	config Config
	mux    *http.ServeMux
}

func New(config Config) (*Server, error) {
	if config.Service == nil {
		return nil, errors.New("appserver: control client service is required")
	}
	if config.Authenticator == nil && strings.TrimSpace(config.LocalPrincipal.ID) == "" {
		config.LocalPrincipal.ID = "local-user"
	}
	if config.Heartbeat <= 0 {
		config.Heartbeat = 15 * time.Second
	}
	server := &Server{config: config, mux: http.NewServeMux()}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if hasCredentialQuery(request) {
		writeError(writer, http.StatusBadRequest, "credentials are not accepted in query parameters")
		return
	}
	s.mux.ServeHTTP(writer, request)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions", s.listSessions)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions", s.createSession)
	s.mux.HandleFunc("DELETE "+apiPrefix+"/sessions/{session_id}", s.closeSession)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/state", s.sessionState)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/events", s.sessionEvents)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/stream", s.streamSessionEvents)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/prompt", s.prompt)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/steer", s.steer)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/cancel", s.cancel)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/approvals/{approval_request_id}/resolve", s.resolveApproval)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants", s.attachParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/{participant_id}/prompt", s.promptParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/{participant_id}/cancel", s.cancelParticipant)
	s.mux.HandleFunc("DELETE "+apiPrefix+"/sessions/{session_id}/participants/{participant_id}", s.detachParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/handoff", s.handoff)
}

func (s *Server) principal(request *http.Request) (controlclient.Principal, error) {
	if s.config.Authenticator == nil {
		return s.config.LocalPrincipal, nil
	}
	principal, err := s.config.Authenticator.Authenticate(request)
	if err != nil || strings.TrimSpace(principal.ID) == "" {
		return controlclient.Principal{}, errors.New("appserver: authentication failed")
	}
	return principal, nil
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.config.Service.ListSessions(r.Context(), principal, controlclient.ListSessionsRequest{WorkspaceKey: r.URL.Query().Get("workspace_key"), Cursor: r.URL.Query().Get("cursor"), Limit: limit})
	writeJSONResult(w, result, err)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.CreateSessionRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, "") {
		return
	}
	result, err := s.config.Service.CreateSession(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	req := controlclient.CloseSessionRequest{}
	if !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.CloseSession(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) sessionState(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Service.InspectSession(r.Context(), principal, controlclient.StateRequest{SessionID: r.PathValue("session_id")})
	writeJSONResult(w, result, err)
}

func (s *Server) sessionEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	cursor, ok := resumeCursor(w, r)
	if !ok {
		return
	}
	result, err := s.config.Service.Events(r.Context(), principal, controlclient.SubscribeRequest{SessionID: r.PathValue("session_id"), Cursor: cursor})
	writeJSONResult(w, result, err)
}

func (s *Server) streamSessionEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	cursor, ok := resumeCursor(w, r)
	if !ok {
		return
	}
	result, err := s.config.Service.Subscribe(r.Context(), principal, controlclient.SubscribeRequest{SessionID: r.PathValue("session_id"), Cursor: cursor})
	if err != nil {
		writeMappedError(w, err)
		return
	}
	defer result.Subscription.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set(resumeModeHeader, string(result.Mode))
	w.Header().Set(transientGapHeader, strconv.FormatBool(result.TransientGap))
	if result.BoundaryCursor != "" {
		w.Header().Set(boundaryCursorHeader, result.BoundaryCursor)
	}
	w.WriteHeader(http.StatusOK)
	boundary, err := json.Marshal(resumeBoundary{
		ResumeMode: result.Mode, TransientGap: result.TransientGap, BoundaryCursor: result.BoundaryCursor,
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", resumeEventName, boundary)
	flusher.Flush()
	heartbeat := time.NewTicker(s.config.Heartbeat)
	defer heartbeat.Stop()
	events := result.Subscription.Backfill()
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
					events = result.Subscription.Events()
					continue
				}
				var gap *controlclient.FeedGapError
				if errors.As(result.Subscription.Err(), &gap) {
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
			data, err := json.Marshal(envelope)
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
	var req controlclient.PromptRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.Prompt(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.SteerRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.Steer(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.CancelRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.Cancel(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.ResolveApprovalRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	if req.ApprovalRequestID != "" && req.ApprovalRequestID != r.PathValue("approval_request_id") {
		writeError(w, http.StatusBadRequest, "approval request id mismatch")
		return
	}
	req.ApprovalRequestID = r.PathValue("approval_request_id")
	result, err := s.config.Service.ResolveApproval(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) attachParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.AttachParticipantRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.AttachParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) promptParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.PromptParticipantRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) || !applyParticipantPath(w, &req.ParticipantID, r.PathValue("participant_id")) {
		return
	}
	result, err := s.config.Service.PromptParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) cancelParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.CancelParticipantRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) || !applyParticipantPath(w, &req.ParticipantID, r.PathValue("participant_id")) {
		return
	}
	result, err := s.config.Service.CancelParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) detachParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	req := controlclient.DetachParticipantRequest{ParticipantID: r.PathValue("participant_id")}
	if !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.DetachParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
func (s *Server) handoff(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.HandoffRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Service.Handoff(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) requirePrincipal(w http.ResponseWriter, r *http.Request) (controlclient.Principal, bool) {
	principal, err := s.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return controlclient.Principal{}, false
	}
	return principal, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	return true
}

func applyWriteHeaders(w http.ResponseWriter, r *http.Request, base *controlclient.WriteBase, sessionID string) bool {
	operationID := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
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
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		return true
	}
	ifMatch = strings.TrimPrefix(ifMatch, "W/")
	ifMatch = strings.Trim(ifMatch, `"`)
	revision, err := strconv.ParseUint(ifMatch, 10, 64)
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

func applyParticipantPath(w http.ResponseWriter, value *string, path string) bool {
	if *value != "" && strings.TrimSpace(*value) != path {
		writeError(w, http.StatusBadRequest, "participant id mismatch")
		return false
	}
	*value = path
	return true
}

func resumeCursor(w http.ResponseWriter, r *http.Request) (string, bool) {
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	last := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
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
	query := r.URL.Query()
	return query.Has("token") || query.Has("access_token") || query.Has("authorization") || query.Has("auth")
}

func writeCommandResult(w http.ResponseWriter, result controlclient.CommandResult, err error) {
	status := http.StatusOK
	switch result.Outcome {
	case controlclient.OutcomeAccepted, controlclient.OutcomeUnknown:
		status = http.StatusAccepted
	case controlclient.OutcomeConflicted:
		status = http.StatusConflict
	case controlclient.OutcomeRejected:
		status = http.StatusBadRequest
	}
	if errors.Is(err, eventstream.ErrInvalidCursor) {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

func writeJSONResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func writeMappedError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		status = http.StatusForbidden
	}
	writeError(w, status, err.Error())
}
func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"error": strings.TrimSpace(detail)})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// ValidateListener rejects unsafe unauthenticated non-loopback binding.
func ValidateListener(address string, authenticator Authenticator) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("appserver: invalid listen address: %w", err)
	}
	host = strings.Trim(host, "[]")
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback && authenticator == nil {
		return errors.New("appserver: non-loopback listener requires authentication")
	}
	return nil
}
