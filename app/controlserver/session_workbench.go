package controlserver

import (
	"net/http"
	"strconv"

	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) sessionWorkbenchRoutes() {
	workbench, ok := s.config.Services.Sessions.(appserver.SessionWorkbenchService)
	if !ok || !workbench.HasSessionWorkbench() {
		return
	}
	s.mux.HandleFunc("GET "+apiPrefix+"/session-workbench", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		query := r.URL.Query()
		limit := 0
		if raw := query.Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
		}
		page, err := workbench.ListSessionWorkbench(r.Context(), principal, appserver.SessionWorkbenchRequest{
			Cursor: query.Get("cursor"), Limit: limit, RefreshIDs: query["refresh_id"],
		})
		writeJSONResult(w, page, err)
	})
	s.mux.HandleFunc("PUT "+apiPrefix+"/sessions/{session_id}/read-position", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var receipt appserver.SessionReadReceipt
		if !decodeBody(w, r, &receipt) {
			return
		}
		if err := workbench.AcknowledgeSessionRead(r.Context(), principal, r.PathValue("session_id"), receipt); err != nil {
			writeMappedError(w, err)
			return
		}
		writeJSONResult(w, struct{}{}, nil)
	})
}
