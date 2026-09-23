package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) applicationConfigurationRoutes() {
	service := s.config.Services.Applications
	s.mux.HandleFunc("GET "+apiPrefix+"/application/sessions/{session_id}/configuration", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := service.ApplicationConfiguration(r.Context(), p, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/application/sessions/{session_id}/configuration", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req application.UpdateConfigurationRequest
		if !decodeBody(w, r, &req) {
			return
		}
		// If-Match belongs to canonical Session state, not configuration CAS.
		// Only the independent expected_configuration_revision is accepted here.
		base := appserver.WriteBase{OperationID: req.OperationID}
		if !applyHostWriteHeaders(w, r, &base) {
			return
		}
		if base.ExpectedRevision != nil {
			writeError(w, http.StatusBadRequest, "configuration updates use expected_configuration_revision, not If-Match")
			return
		}
		req.OperationID = base.OperationID
		out, err := service.UpdateApplicationConfiguration(r.Context(), p, r.PathValue("session_id"), req)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/configuration-operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := service.ApplicationConfigurationOperation(r.Context(), p, r.PathValue("operation_id"))
		writeJSONResult(w, out, err)
	})
}
