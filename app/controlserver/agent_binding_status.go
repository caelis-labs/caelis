package controlserver

import (
	"net/http"
	"slices"

	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) agentBindingStatus(w http.ResponseWriter, r *http.Request, principal appserver.Principal, request appserver.AgentRequest) {
	include, negotiated := r.URL.Query()["include"]
	if negotiated && (len(include) != 1 || include[0] != "eligible_profile_ids") {
		writeError(w, http.StatusBadRequest, "include must be eligible_profile_ids")
		return
	}
	status, err := s.config.Services.Agents.AgentBindingStatus(r.Context(), principal, request)
	if err != nil {
		writeMappedError(w, err)
		return
	}
	if !negotiated {
		// Strict v1 clients reject added fields. Keep the HTTP default shape
		// until a protocol version retires the unnegotiated v1 response.
		status.Handles = slices.Clone(status.Handles)
		for i := range status.Handles {
			status.Handles[i].EligibleProfileIDs = nil
		}
	}
	writeJSON(w, http.StatusOK, status)
}
