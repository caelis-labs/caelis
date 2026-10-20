package controlserver

import (
	"net/http"
	"slices"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func sessionServerInfo(info appserver.ServerInfo, services appserver.AppServerServices) appserver.ServerInfo {
	info = normalizeServerInfo(info)
	if services.Experimental != nil && !slices.Contains(info.Capabilities, appserver.CapabilityExperimental) {
		info.Capabilities = append(info.Capabilities, appserver.CapabilityExperimental)
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilityModelAuthStream) {
		info.Capabilities = append(info.Capabilities, appserver.CapabilityModelAuthStream)
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilityExecutionConfiguration) {
		info.Capabilities = append(info.Capabilities, appserver.CapabilityExecutionConfiguration)
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilityApprovalSubjects) {
		info.Capabilities = append(info.Capabilities, appserver.CapabilityApprovalSubjects)
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilityDisplayReplacements) {
		info.Capabilities = append(info.Capabilities, appserver.CapabilityDisplayReplacements)
	}
	if appserver.SupportsConversationInteractions(services.Sessions) && !slices.Contains(info.Capabilities, appserver.CapabilityConversationInteractions) {
		info.Capabilities = append(info.Capabilities, appserver.CapabilityConversationInteractions)
	}
	if services.SessionProfiles != nil {
		for _, capability := range services.SessionProfiles.Capabilities() {
			found := false
			for _, existing := range info.Capabilities {
				found = found || existing == capability
			}
			if !found {
				info.Capabilities = append(info.Capabilities, capability)
			}
		}
		if services.Terminal != nil && !slices.Contains(info.Capabilities, appserver.CapabilityTerminalObservation) {
			info.Capabilities = append(info.Capabilities, appserver.CapabilityTerminalObservation)
		}
		if services.Terminal != nil && !slices.Contains(info.Capabilities, appserver.CapabilityTerminalWait) {
			info.Capabilities = append(info.Capabilities, appserver.CapabilityTerminalWait)
		}
		if services.Terminal != nil && !slices.Contains(info.Capabilities, appserver.CapabilityTerminalControl) {
			info.Capabilities = append(info.Capabilities, appserver.CapabilityTerminalControl)
		}
	}
	return info
}

func (s *Server) requireSessionScope(w http.ResponseWriter, r *http.Request) (appserver.Principal, sessionprofile.Scope, bool) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return p, sessionprofile.Scope{}, false
	}
	scope, err := appserver.SessionScope(p)
	if err != nil {
		writeJSONResult(w, nil, err)
		return p, scope, false
	}
	return p, scope, true
}
func decodeEmptyToolCallBody(w http.ResponseWriter, r *http.Request) bool {
	var empty struct{}
	return decodeBody(w, r, &empty)
}
