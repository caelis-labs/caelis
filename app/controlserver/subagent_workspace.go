package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

func (s *Server) subagentWorkspaceRoutes() {
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/subagents/input", s.submitSubagentInput)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/subagents/input-status", s.subagentInputStatuses)
	s.mux.HandleFunc("GET "+apiPrefix+"/presentation/preferences", s.loadUIPreferences)
	s.mux.HandleFunc("PUT "+apiPrefix+"/presentation/preferences", s.saveUIPreferences)
}
func (s *Server) submitSubagentInput(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.SubagentInputRequest
	if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
		return
	}
	result, err := s.config.Services.SubagentInputs.Submit(r.Context(), p, req)
	writeJSONResult(w, result, err)
}
func (s *Server) subagentInputStatuses(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req appserver.SubagentInputStatusRequest
	if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
		return
	}
	result, err := s.config.Services.SubagentInputs.Statuses(r.Context(), p, req)
	writeJSONResult(w, result, err)
}
func (s *Server) loadUIPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.UIPreferences.Load(r.Context(), p)
	writeJSONResult(w, result, err)
}
func (s *Server) saveUIPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req uipreferences.Preferences
	if !decodeBody(w, r, &req) {
		return
	}
	writeEmptyResult(w, s.config.Services.UIPreferences.Save(r.Context(), p, req))
}
