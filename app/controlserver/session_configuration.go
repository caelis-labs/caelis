package controlserver

import (
	"github.com/caelis-labs/caelis/control/appserver"
	"net/http"
)

func (s *Server) sessionConfigurationRoutes() {
	service := s.config.Services.SessionProfiles
	if service == nil {
		return
	}
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/profile", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := service.SessionProfile(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/reviewer-state", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		out, err := service.SessionReviewerState(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/model-capabilities", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		out, err := service.SessionModelCapabilities(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/mcp-status", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		out, err := service.SessionMCPStatus(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/configuration", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := service.SessionConfiguration(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.UpdateSessionConfigurationRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		out, err := service.UpdateSessionConfiguration(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
}
