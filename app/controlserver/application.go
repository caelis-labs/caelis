package controlserver

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func applicationServerInfo(info appserver.ServerInfo, services appserver.AppServerServices) appserver.ServerInfo {
	info = normalizeServerInfo(info)
	if services.Applications != nil {
		for _, capability := range info.Capabilities {
			if capability == application.Capability {
				return info
			}
		}
		info.Capabilities = append(info.Capabilities, application.Capability)
	}
	return info
}

// applicationPrincipal enforces the transport capability boundary before focused
// services run. Even a valid application credential cannot stop the shared Host,
// change model credentials, or use ordinary Session creation as an escape hatch.
func (s *Server) applicationPrincipal(r *http.Request) (appserver.Principal, bool, error) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, "app-client-") {
		return appserver.Principal{}, false, nil
	}
	service := s.config.Services.Applications
	if service == nil {
		return appserver.Principal{}, true, appserver.ErrUnauthorized
	}
	scope, err := service.Store().Authenticate(r.Context(), token)
	if err != nil {
		return appserver.Principal{}, true, err
	}
	p := appserver.Principal{ID: scope.PrincipalID, ApplicationID: scope.ApplicationID, ConnectionID: scope.ConnectionID}
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if path == "/initialize" || strings.HasPrefix(path, "/application/") {
		return p, true, nil
	}
	// Native state, replay, cancellation and precise approval remain with their
	// canonical owner. All other ordinary routes are denied before dispatch.
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "sessions" {
		permitted := false
		if r.Method == http.MethodGet && len(parts) == 3 {
			switch parts[2] {
			case "state", "reconnect", "events", "subscribe":
				permitted = true
			}
		}
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "cancel" {
			permitted = true
		}
		if r.Method == http.MethodPost && len(parts) == 5 && parts[2] == "approvals" && parts[4] == "resolve" {
			permitted = true
		}
		if permitted {
			_, err = service.Store().GetBinding(r.Context(), scope, parts[1])
			return p, true, err
		}
	}
	return appserver.Principal{}, true, appserver.ErrUnauthorized
}

func (s *Server) applicationRoutes() {
	if s.config.Services.Applications == nil {
		return
	}
	service := s.config.Services.Applications
	store := service.Store()
	s.mux.HandleFunc("POST "+apiPrefix+"/applications/register", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req application.Registration
		if !decodeBody(w, r, &req) {
			return
		}
		base := appserver.WriteBase{OperationID: req.OperationID}
		if !applyHostWriteHeaders(w, r, &base) {
			return
		}
		req.OperationID = base.OperationID
		out, err := service.Register(r.Context(), p, req)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/connection", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := store.Connection(r.Context(), scope)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/connection/renew", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok || !decodeEmptyApplicationBody(w, r) {
			return
		}
		out, err := store.Renew(r.Context(), scope)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/connection/revoke", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok || !decodeEmptyApplicationBody(w, r) {
			return
		}
		err := store.Revoke(r.Context(), scope)
		writeJSONResult(w, struct{}{}, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := store.ListBindings(r.Context(), scope)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req appserver.CreateApplicationSessionRequest
		if !decodeBody(w, r, &req) || !applyHostWriteHeaders(w, r, &req.WriteBase) {
			return
		}
		out, err := service.Create(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := store.GetBinding(r.Context(), scope, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/prompt", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req appserver.ApplicationPromptRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		out, err := service.Prompt(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/archive", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req appserver.CloseSessionRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		out, err := service.Archive(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := service.Operation(r.Context(), p, r.PathValue("operation_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/calls", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var out []application.Call
		var err error
		if r.URL.Query().Get("wait") == "true" {
			out, err = store.WaitCalls(r.Context(), scope, r.PathValue("session_id"))
		} else {
			out, err = store.ListCalls(r.Context(), scope, r.PathValue("session_id"))
		}
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/calls/{call_id}", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := store.GetCall(r.Context(), scope, r.PathValue("session_id"), r.PathValue("call_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/calls/{call_id}/claim", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok || !decodeEmptyApplicationBody(w, r) {
			return
		}
		out, err := store.ClaimCall(r.Context(), scope, r.PathValue("session_id"), r.PathValue("call_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/calls/{call_id}/result", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req application.CallResult
		if !decodeBody(w, r, &req) {
			return
		}
		err := store.CompleteCall(r.Context(), scope, r.PathValue("session_id"), r.PathValue("call_id"), req)
		writeJSONResult(w, struct{}{}, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/resources", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req appserver.ApplicationResourceRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		digest := sha256.Sum256(req.Data)
		if req.SHA256 != hex.EncodeToString(digest[:]) {
			writeJSONResult(w, nil, errorcode.New(errorcode.InvalidArgument, "resource SHA256 mismatch"))
			return
		}
		out, err := store.CreateResource(r.Context(), scope, req.SessionID, req.OperationID, req.Name, req.MediaType, req.Data)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/resources/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := store.GetResource(r.Context(), scope, r.PathValue("session_id"), r.PathValue("resource_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/resources/{resource_id}/content", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		resource, data, err := store.ReadResource(r.Context(), scope, r.PathValue("session_id"), r.PathValue("resource_id"))
		writeJSONResult(w, appserver.ApplicationResourceContent{Resource: resource, Data: data}, err)
	})
}

func (s *Server) requireApplication(w http.ResponseWriter, r *http.Request) (appserver.Principal, application.Scope, bool) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return p, application.Scope{}, false
	}
	scope, err := appserver.ApplicationScope(p)
	if err != nil {
		writeJSONResult(w, nil, err)
		return p, scope, false
	}
	return p, scope, true
}
func decodeEmptyApplicationBody(w http.ResponseWriter, r *http.Request) bool {
	var empty struct{}
	return decodeBody(w, r, &empty)
}
