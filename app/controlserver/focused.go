package controlserver

import (
	"net/http"
	"strings"

	controlclient "github.com/caelis-labs/caelis/control/client"
)

// focusedRoutes exposes the same focused capabilities used by the embedded
// AppServer clients. Paths are explicit semantic operations; there is no raw
// command or slash dispatch endpoint.
func (s *Server) focusedRoutes() {
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/participants/handles", s.participantHandles)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/start", s.startParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/prompt", s.promptParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/cancel", s.cancelParticipant)

	for path, action := range map[string]string{
		"session-mode": "session-mode", "connect-model": "connect-model", "use-model": "use-model",
		"delete-model": "delete-model", "sandbox-backend": "sandbox-backend", "sandbox-prepare": "sandbox-prepare",
		"sandbox-repair": "sandbox-repair", "sandbox-refresh": "sandbox-refresh",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration/"+path, s.configurationHandler(action))
	}
	for path, action := range map[string]string{
		"list": "list", "status": "status", "handoff": "handoff", "discover-acp": "discover-acp",
		"connect-acp": "connect-acp", "disconnect-candidates": "disconnect-candidates", "disconnect-acp": "disconnect-acp",
		"binding-status": "binding-status", "bind": "bind", "reset-binding": "reset-binding",
		"create-role": "create-role", "delete-role": "delete-role", "save-binding-set": "save-binding-set",
		"apply-binding-set": "apply-binding-set", "delete-binding-set": "delete-binding-set",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/agents/"+path, s.agentHandler(action))
	}
	for path, action := range map[string]string{
		"files": "files", "skills": "skills", "sessions": "sessions", "slash-arguments": "slash-arguments", "resolve-skill": "resolve-skill",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/completion/"+path, s.completionHandler(action))
	}
	for path, action := range map[string]string{
		"list": "list", "add-marketplace": "add-marketplace", "list-marketplaces": "list-marketplaces",
		"update-marketplace": "update-marketplace", "remove-marketplace": "remove-marketplace", "add-path": "add-path",
		"install": "install", "enable": "enable", "disable": "disable", "remove": "remove", "inspect": "inspect",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/plugins/"+path, s.pluginHandler(action))
	}
}

func (s *Server) participantHandles(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Participants.ListParticipantHandles(r.Context(), principal, r.PathValue("session_id"))
	writeJSONResult(w, result, err)
}

func (s *Server) startParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.StartParticipantRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Participants.StartParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) promptParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.PromptParticipantRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Participants.PromptParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) cancelParticipant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.CancelParticipantRequest
	if !decodeBody(w, r, &req) || !applyWriteHeaders(w, r, &req.WriteBase, r.PathValue("session_id")) {
		return
	}
	result, err := s.config.Services.Participants.CancelParticipant(r.Context(), principal, req)
	writeCommandResult(w, result, err)
}

func (s *Server) configurationHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		sessionID := r.PathValue("session_id")
		switch action {
		case "session-mode":
			var req controlclient.SessionModeRequest
			if decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				result, err := s.config.Services.Configuration.ConfigureSessionMode(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "connect-model":
			var req controlclient.ConnectModelRequest
			if decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				result, err := s.config.Services.Configuration.ConnectModel(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "use-model":
			var req controlclient.UseModelRequest
			if decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				result, err := s.config.Services.Configuration.UseModel(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "delete-model":
			var req controlclient.DeleteModelRequest
			if decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				err := s.config.Services.Configuration.DeleteModel(r.Context(), principal, req)
				writeEmptyResult(w, err)
			}
		default:
			var req controlclient.SandboxRequest
			if !decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				return
			}
			switch action {
			case "sandbox-backend":
				result, err := s.config.Services.Configuration.SetSandboxBackend(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "sandbox-prepare":
				result, err := s.config.Services.Configuration.PrepareSandbox(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "sandbox-repair":
				result, err := s.config.Services.Configuration.RepairSandbox(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "sandbox-refresh":
				writeEmptyResult(w, s.config.Services.Configuration.RefreshSandbox(r.Context(), principal, req))
			}
		}
	}
}

func (s *Server) agentHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		sessionID := r.PathValue("session_id")
		switch action {
		case "list", "status", "binding-status":
			var req controlclient.AgentRequest
			if !decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				return
			}
			switch action {
			case "list":
				result, err := s.config.Services.Agents.ListAgents(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "status":
				result, err := s.config.Services.Agents.AgentStatus(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "binding-status":
				result, err := s.config.Services.Agents.AgentBindingStatus(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "handoff":
			var req controlclient.HandoffAgentRequest
			if decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				result, err := s.config.Services.Agents.HandoffAgent(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "discover-acp", "connect-acp":
			var req controlclient.ConnectACPRequest
			if !decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				return
			}
			if action == "discover-acp" {
				result, err := s.config.Services.Agents.DiscoverACPConnection(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			} else {
				result, err := s.config.Services.Agents.ConnectACP(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "disconnect-candidates", "disconnect-acp":
			var req controlclient.DisconnectACPRequest
			if !decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				return
			}
			if action == "disconnect-candidates" {
				result, err := s.config.Services.Agents.DisconnectCandidates(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			} else {
				result, err := s.config.Services.Agents.DisconnectACP(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		default:
			var req controlclient.AgentBindingRequest
			if !decodeSessionBody(w, r, sessionID, &req.SessionID, &req) {
				return
			}
			var result any
			var err error
			switch action {
			case "bind":
				result, err = s.config.Services.Agents.BindAgentBinding(r.Context(), principal, req)
			case "reset-binding":
				result, err = s.config.Services.Agents.ResetAgentBinding(r.Context(), principal, req)
			case "create-role":
				result, err = s.config.Services.Agents.CreateAgentRole(r.Context(), principal, req)
			case "delete-role":
				result, err = s.config.Services.Agents.DeleteAgentRole(r.Context(), principal, req)
			case "save-binding-set":
				result, err = s.config.Services.Agents.SaveAgentBindingSet(r.Context(), principal, req)
			case "apply-binding-set":
				result, err = s.config.Services.Agents.ApplyAgentBindingSet(r.Context(), principal, req)
			case "delete-binding-set":
				result, err = s.config.Services.Agents.DeleteAgentBindingSet(r.Context(), principal, req)
			}
			writeJSONResult(w, result, err)
		}
	}
}

func (s *Server) completionHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req controlclient.CompletionRequest
		if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
			return
		}
		switch action {
		case "files":
			result, err := s.config.Services.Completion.CompleteFile(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "skills":
			result, err := s.config.Services.Completion.CompleteSkill(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "sessions":
			result, err := s.config.Services.Completion.CompleteResume(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "slash-arguments":
			result, err := s.config.Services.Completion.CompleteSlashArg(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "resolve-skill":
			result, err := s.config.Services.Completion.ResolveSkill(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		}
	}
}

func (s *Server) pluginHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req controlclient.PluginRequest
		if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
			return
		}
		switch action {
		case "list":
			result, err := s.config.Services.Plugins.ListPlugins(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "add-marketplace":
			result, err := s.config.Services.Plugins.AddMarketplace(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "list-marketplaces":
			result, err := s.config.Services.Plugins.ListMarketplaces(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "update-marketplace":
			result, err := s.config.Services.Plugins.UpdateMarketplace(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "remove-marketplace":
			writeEmptyResult(w, s.config.Services.Plugins.RemoveMarketplace(r.Context(), principal, req))
		case "add-path":
			result, err := s.config.Services.Plugins.AddPluginPath(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "install":
			result, err := s.config.Services.Plugins.InstallPlugin(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "enable":
			result, err := s.config.Services.Plugins.EnablePlugin(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "disable":
			result, err := s.config.Services.Plugins.DisablePlugin(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		case "remove":
			writeEmptyResult(w, s.config.Services.Plugins.RemovePlugin(r.Context(), principal, req))
		case "inspect":
			result, err := s.config.Services.Plugins.InspectPlugin(r.Context(), principal, req)
			writeJSONResult(w, result, err)
		}
	}
}

func decodeSessionBody(w http.ResponseWriter, r *http.Request, pathSessionID string, bodySessionID *string, target any) bool {
	if !decodeBody(w, r, target) {
		return false
	}
	pathSessionID = strings.TrimSpace(pathSessionID)
	if current := strings.TrimSpace(*bodySessionID); current != "" && current != pathSessionID {
		writeError(w, http.StatusBadRequest, "session id mismatch")
		return false
	}
	*bodySessionID = pathSessionID
	return true
}

func writeEmptyResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
