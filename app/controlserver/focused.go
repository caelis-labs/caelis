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
	s.mux.HandleFunc("GET "+apiPrefix+"/presentation/capabilities", s.presentationCapabilities)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/presentation", s.presentationSnapshot)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/terminals/output", s.terminalOutput)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/terminals/wait", s.waitTerminal)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/terminals/kill", s.killTerminal)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/terminals/release", s.releaseTerminal)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/agent-messages", s.deliverAgentMessage)
	s.mux.HandleFunc("GET "+apiPrefix+"/sessions/{session_id}/participants/handles", s.participantHandles)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/start", s.startParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/prompt", s.promptParticipant)
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/participants/cancel", s.cancelParticipant)

	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration/session-mode", s.configurationHandler("session-mode"))
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration/model", s.configurationHandler("session-model"))
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration/controller-mode", s.configurationHandler("controller-mode"))
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration/presentation-mode", s.configurationHandler("presentation-mode"))
	s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/configuration/presentation-config", s.configurationHandler("presentation-config"))
	for path, action := range map[string]string{
		"connect-model": "connect-model", "use-model": "use-model", "delete-model": "delete-model",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/configuration/"+path, s.configurationHandler(action))
	}
	for path, action := range map[string]string{
		"sandbox-backend": "sandbox-backend", "sandbox-prepare": "sandbox-prepare", "sandbox-repair": "sandbox-repair",
		"sandbox-reset": "sandbox-reset", "sandbox-refresh": "sandbox-refresh",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/configuration/"+path, s.configurationHandler(action))
	}
	for path, action := range map[string]string{
		"list": "list", "status": "status", "handoff": "handoff",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/agents/"+path, s.agentHandler(action))
		if path != "handoff" {
			s.mux.HandleFunc("POST "+apiPrefix+"/agents/"+path, s.agentHandler(action))
		}
	}
	s.mux.HandleFunc("POST "+apiPrefix+"/agents/prepare-acp", s.agentHandler("prepare-acp"))
	s.mux.HandleFunc("POST "+apiPrefix+"/agents/prepare-acp-auth", s.agentHandler("prepare-acp-auth"))
	s.mux.HandleFunc("POST "+apiPrefix+"/agents/connect-acp", s.agentHandler("connect-acp"))
	s.mux.HandleFunc("GET "+apiPrefix+"/agents/acp-preparations/{preparation_ref}", s.agentHandler("acp-preparation"))
	s.mux.HandleFunc("POST "+apiPrefix+"/agents/disconnect-candidates", s.agentHandler("disconnect-candidates"))
	s.mux.HandleFunc("POST "+apiPrefix+"/agents/disconnect-acp", s.agentHandler("disconnect-acp"))
	for path, action := range map[string]string{
		"binding-status": "binding-status", "bind": "bind", "reset-binding": "reset-binding",
		"create-role": "create-role", "delete-role": "delete-role", "save-binding-set": "save-binding-set",
		"apply-binding-set": "apply-binding-set", "delete-binding-set": "delete-binding-set",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/agents/"+path, s.agentHandler(action))
	}
	for path, action := range map[string]string{
		"files": "files", "skills": "skills", "sessions": "sessions", "slash-arguments": "slash-arguments", "resolve-skill": "resolve-skill",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/completion/"+path, s.completionHandler(action))
		s.mux.HandleFunc("POST "+apiPrefix+"/completion/"+path, s.completionHandler(action))
	}
	for path, action := range map[string]string{
		"list": "list", "list-marketplaces": "list-marketplaces", "inspect": "inspect",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/sessions/{session_id}/plugins/"+path, s.pluginHandler(action))
		s.mux.HandleFunc("POST "+apiPrefix+"/plugins/"+path, s.pluginHandler(action))
	}
	for path, action := range map[string]string{
		"add-marketplace": "add-marketplace", "update-marketplace": "update-marketplace",
		"remove-marketplace": "remove-marketplace", "add-path": "add-path", "install": "install",
		"enable": "enable", "disable": "disable", "remove": "remove",
	} {
		s.mux.HandleFunc("POST "+apiPrefix+"/plugins/"+path, s.pluginHandler(action))
	}
}

func (s *Server) presentationCapabilities(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Presentation.PresentationCapabilities(r.Context(), principal)
	writeJSONResult(w, result, err)
}

func (s *Server) presentationSnapshot(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Presentation.PresentationSnapshot(r.Context(), principal, controlclient.PresentationRequest{SessionID: r.PathValue("session_id")})
	writeJSONResult(w, result, err)
}

func (s *Server) terminalOutput(w http.ResponseWriter, r *http.Request) {
	principal, req, ok := terminalRequest(w, r, s)
	if !ok {
		return
	}
	result, err := s.config.Services.Terminal.TerminalOutput(r.Context(), principal, req)
	writeJSONResult(w, result, err)
}

func (s *Server) waitTerminal(w http.ResponseWriter, r *http.Request) {
	principal, req, ok := terminalRequest(w, r, s)
	if !ok {
		return
	}
	result, err := s.config.Services.Terminal.WaitTerminal(r.Context(), principal, req)
	writeJSONResult(w, result, err)
}

func (s *Server) killTerminal(w http.ResponseWriter, r *http.Request) {
	principal, req, ok := terminalRequest(w, r, s)
	if ok {
		writeEmptyResult(w, s.config.Services.Terminal.KillTerminal(r.Context(), principal, req))
	}
}

func (s *Server) releaseTerminal(w http.ResponseWriter, r *http.Request) {
	principal, req, ok := terminalRequest(w, r, s)
	if ok {
		writeEmptyResult(w, s.config.Services.Terminal.ReleaseTerminal(r.Context(), principal, req))
	}
}

func terminalRequest(w http.ResponseWriter, r *http.Request, s *Server) (controlclient.Principal, controlclient.TerminalRequest, bool) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return controlclient.Principal{}, controlclient.TerminalRequest{}, false
	}
	var req controlclient.TerminalRequest
	if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
		return controlclient.Principal{}, controlclient.TerminalRequest{}, false
	}
	return principal, req, true
}

func (s *Server) deliverAgentMessage(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req controlclient.AgentMessageRequest
	if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
		return
	}
	result, err := s.config.Services.AgentMessages.DeliverAgentMessage(r.Context(), principal, req)
	writeJSONResult(w, result, err)
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
			if decodeBody(w, r, &req) && applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
				result, err := s.config.Services.Configuration.ConfigureSessionMode(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "session-model":
			var req controlclient.SessionModelRequest
			if decodeBody(w, r, &req) && applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
				result, err := s.config.Services.Configuration.UseSessionModel(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "controller-mode":
			var req controlclient.SessionControllerModeRequest
			if decodeBody(w, r, &req) && applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
				result, err := s.config.Services.Configuration.ConfigureSessionControllerMode(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "presentation-mode":
			var req controlclient.SessionPresentationModeRequest
			if decodeBody(w, r, &req) && applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
				result, err := s.config.Services.Configuration.ConfigureSessionPresentationMode(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "presentation-config":
			var req controlclient.SessionPresentationConfigRequest
			if decodeBody(w, r, &req) && applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
				result, err := s.config.Services.Configuration.ConfigureSessionPresentation(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "connect-model":
			var req controlclient.ConnectModelRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Configuration.ConnectModel(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "use-model":
			var req controlclient.UseModelRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Configuration.UseModel(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "delete-model":
			var req controlclient.DeleteModelRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Configuration.DeleteModel(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		default:
			var req controlclient.SandboxRequest
			if !decodeBody(w, r, &req) || !applyHostWriteHeaders(w, r, &req.WriteBase) {
				return
			}
			switch action {
			case "sandbox-backend":
				result, err := s.config.Services.Configuration.SetSandboxBackend(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			case "sandbox-prepare":
				result, err := s.config.Services.Configuration.PrepareSandbox(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			case "sandbox-repair":
				result, err := s.config.Services.Configuration.RepairSandbox(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			case "sandbox-reset":
				result, err := s.config.Services.Configuration.ResetSandbox(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			case "sandbox-refresh":
				result, err := s.config.Services.Configuration.RefreshSandbox(r.Context(), principal, req)
				writeCommandResult(w, result, err)
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
			if decodeSessionBody(w, r, sessionID, &req.SessionID, &req) &&
				applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
				result, err := s.config.Services.Agents.HandoffAgent(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "prepare-acp":
			var req controlclient.PrepareACPRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Agents.PrepareACP(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "prepare-acp-auth":
			var req controlclient.PrepareACPAuthenticationRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Agents.PrepareACPAuthentication(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "connect-acp":
			var req controlclient.ConnectACPRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Agents.ConnectACP(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "acp-preparation":
			result, err := s.config.Services.Agents.ACPPreparation(r.Context(), principal, controlclient.ACPPreparationRequest{
				Ref: r.PathValue("preparation_ref"),
			})
			writeJSONResult(w, result, err)
		case "disconnect-candidates":
			var req controlclient.AgentRequest
			if decodeBody(w, r, &req) {
				result, err := s.config.Services.Agents.DisconnectCandidates(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "disconnect-acp":
			var req controlclient.DisconnectACPRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Agents.DisconnectACP(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		default:
			switch action {
			case "bind":
				var req controlclient.BindAgentBindingRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.BindAgentBinding(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			case "reset-binding":
				var req controlclient.ResetAgentBindingRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.ResetAgentBinding(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			case "create-role":
				var req controlclient.CreateAgentRoleRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.CreateAgentRole(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			case "delete-role":
				var req controlclient.DeleteAgentRoleRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.DeleteAgentRole(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			case "save-binding-set":
				var req controlclient.AgentBindingSetRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.SaveAgentBindingSet(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			case "apply-binding-set":
				var req controlclient.AgentBindingSetRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.ApplyAgentBindingSet(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			case "delete-binding-set":
				var req controlclient.AgentBindingSetRequest
				if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
					result, err := s.config.Services.Agents.DeleteAgentBindingSet(r.Context(), principal, req)
					writeCommandResult(w, result, err)
				}
			}
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
		switch action {
		case "list", "list-marketplaces", "inspect":
			var req controlclient.PluginRequest
			if !decodeSessionBody(w, r, r.PathValue("session_id"), &req.SessionID, &req) {
				return
			}
			switch action {
			case "list":
				result, err := s.config.Services.Plugins.ListPlugins(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "list-marketplaces":
				result, err := s.config.Services.Plugins.ListMarketplaces(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			case "inspect":
				result, err := s.config.Services.Plugins.InspectPlugin(r.Context(), principal, req)
				writeJSONResult(w, result, err)
			}
		case "add-marketplace":
			var req controlclient.AddMarketplaceRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.AddMarketplace(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "update-marketplace":
			var req controlclient.UpdateMarketplaceRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.UpdateMarketplace(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "remove-marketplace":
			var req controlclient.RemoveMarketplaceRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.RemoveMarketplace(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "add-path":
			var req controlclient.AddPluginPathRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.AddPluginPath(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "install":
			var req controlclient.InstallPluginRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.InstallPlugin(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "enable":
			var req controlclient.EnablePluginRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.EnablePlugin(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "disable":
			var req controlclient.DisablePluginRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.DisablePlugin(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
		case "remove":
			var req controlclient.RemovePluginRequest
			if decodeBody(w, r, &req) && applyHostWriteHeaders(w, r, &req.WriteBase) {
				result, err := s.config.Services.Plugins.RemovePlugin(r.Context(), principal, req)
				writeCommandResult(w, result, err)
			}
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
