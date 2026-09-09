package controlserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/control/collaboration"
)

func (s *Server) collaborationCall(w http.ResponseWriter, r *http.Request) {
	service := s.config.Services.Collaboration
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "collaboration unavailable")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, _, err := service.Authenticate(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid collaboration credential")
		return
	}
	var req collaboration.Request
	if !decodeBody(w, r, &req) {
		return
	}
	result, err := service.CallAuthenticated(r.Context(), token, req)
	writeJSONResult(w, json.RawMessage(result), err)
}
