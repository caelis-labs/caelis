package controlserver

import (
	"github.com/caelis-labs/caelis/control/appserver"
	"net/http"
)

func (s *Server) sessionResourceRoutes() {
	service := s.config.Services.SessionProfiles
	if service == nil {
		return
	}
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/resources", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.SessionResourceRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		out, err := service.CreateSessionResource(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/resources/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		out, err := service.Store().GetResource(r.Context(), scope, r.PathValue("session_id"), r.PathValue("resource_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/resources/{resource_id}/content", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		resource, data, err := service.Store().ReadResource(r.Context(), scope, r.PathValue("session_id"), r.PathValue("resource_id"))
		writeJSONResult(w, appserver.SessionResourceContent{Resource: resource, Data: data}, err)
	})
}
