package controlserver

import (
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"net/http"
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
		var out []sessionprofile.Call
		var err error
		if r.URL.Query().Get("wait") == "true" {
			out, err = store.WaitCalls(r.Context(), scope, r.PathValue("session_id"))
		} else {
			out, err = store.ListCalls(r.Context(), scope, r.PathValue("session_id"))
		}
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
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/tool-calls/{call_id}/claim", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireSessionScope(w, r)
		if !ok || !decodeEmptyToolCallBody(w, r) {
			return
		}
		out, err := store.ClaimCall(r.Context(), scope, r.PathValue("session_id"), r.PathValue("call_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/tool-calls/{call_id}/result", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireSessionScope(w, r)
		if !ok {
			return
		}
		var req sessionprofile.CallResult
		if !decodeBody(w, r, &req) {
			return
		}
		err := store.CompleteCall(r.Context(), scope, r.PathValue("session_id"), r.PathValue("call_id"), req)
		writeJSONResult(w, struct{}{}, err)
	})
}
