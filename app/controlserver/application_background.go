package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) applicationBackgroundRoutes() {
	service := s.config.Services.Applications
	if service == nil {
		return
	}
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/background-grants", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req application.BackgroundGrantRequest
		if !decodeBody(w, r, &req) {
			return
		}
		base := appserver.WriteBase{OperationID: req.OperationID}
		if !applyHostWriteHeaders(w, r, &base) {
			return
		}
		req.OperationID = base.OperationID
		out, err := service.CreateBackgroundGrant(r.Context(), p, r.PathValue("session_id"), req)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/background-grants", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := service.ListBackgroundGrants(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/background-grants/{grant_id}", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := service.BackgroundGrant(r.Context(), p, r.PathValue("session_id"), r.PathValue("grant_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/background-grants/{grant_id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok || !decodeEmptyApplicationBody(w, r) {
			return
		}
		out, err := service.RevokeBackgroundGrant(r.Context(), p, r.PathValue("session_id"), r.PathValue("grant_id"))
		writeJSONResult(w, out, err)
	})
}
