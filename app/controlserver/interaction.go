package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) recordInteraction(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var request appserver.RecordInteractionRequest
	if !decodeBody(w, r, &request) || !applyWriteHeaders(w, r, &request.WriteBase, r.PathValue("session_id")) {
		return
	}
	service, ok := s.config.Services.Sessions.(appserver.InteractionService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "conversation interaction capability is unavailable")
		return
	}
	result, err := service.RecordInteraction(r.Context(), principal, request)
	writeCommandResult(w, result, err)
}
