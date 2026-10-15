package controlserver

import (
	"github.com/caelis-labs/caelis/control/appserver"
	"net/http"
)

func (s *Server) sessionBackgroundRoutes() {
	service := s.config.Services.SessionProfiles
	if service == nil {
		return
	}
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/background-grants", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.CreateSessionBackgroundGrantRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		out, err := service.CreateSessionBackgroundGrant(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/background-grants", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := service.ListBackgroundGrants(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/background-grants/{grant_id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := service.BackgroundGrant(r.Context(), p, r.PathValue("session_id"), r.PathValue("grant_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/background-grants/{grant_id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.RevokeSessionBackgroundGrantRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		if req.GrantID != "" && req.GrantID != r.PathValue("grant_id") {
			writeError(w, http.StatusBadRequest, "grant ID contradicts path")
			return
		}
		req.GrantID = r.PathValue("grant_id")
		out, err := service.RevokeSessionBackgroundGrant(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
}
