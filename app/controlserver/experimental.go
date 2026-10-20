package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) experimentalRoutes() {
	service := s.config.Services.Experimental
	if service == nil {
		return
	}
	s.mux.HandleFunc("GET "+apiPrefix+"/configuration/experimental", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := service.Reader.ExperimentalFeatures(r.Context(), p, r.URL.Query().Get("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/configuration/experimental", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.ExperimentalRequest
		if !decodeBody(w, r, &req) || !applyHostWriteHeaders(w, r, &req.WriteBase) {
			return
		}
		out, err := service.Commands.SetExperimentalFeatures(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/mcp/refresh", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.RefreshMCPToolsRequest
		if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
			return
		}
		out, err := service.Commands.RefreshMCPTools(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
}
