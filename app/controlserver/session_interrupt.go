package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) interruptSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.SessionInterruptRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Sessions.InterruptSession(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}
