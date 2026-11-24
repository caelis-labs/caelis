package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func (s *Server) sessionToolRoutes() {
	service := s.config.Services.SessionProfiles
	if service == nil {
		return
	}
	store := service.Store()
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/tool-calls", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		if r.URL.Query().Get("wait") == "true" {
			writeJSONResult(w, nil, sessionprofile.ErrCallbackRetired)
			return
		}
		out, err := store.ListCalls(r.Context(), scope, r.PathValue("session_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/tool-calls/{call_id}", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		out, err := store.GetCall(r.Context(), scope, r.PathValue("session_id"), r.PathValue("call_id"))
		writeJSONResult(w, out, err)
	})
}
