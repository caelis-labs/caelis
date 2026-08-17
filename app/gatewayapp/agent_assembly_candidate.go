package gatewayapp

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// validateAgentAssemblyCandidate rejects candidate configuration that could
// persist successfully but fail when a later Session assembles Agents from all
// configured sources.
func (s *runtimeComposition) validateAgentAssemblyCandidate(doc AppConfig) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if err := agentbinding.ValidateConfiguration(doc.AgentBindings, doc.ModelProfiles); err != nil {
		return err
	}
	if err := controlagents.ValidateConfiguration(doc.ExternalAgents); err != nil {
		return err
	}

	seen := make(map[string]struct{})
	for _, configured := range s.runtimeProcessSnapshot().runtime.BaseAssembly.Agents {
		if name := agentAssemblyNameKey(configured.Name); name != "" {
			seen[name] = struct{}{}
		}
	}

	contributions, err := resolveGatewayPluginContributions(doc.Plugins)
	if err != nil {
		return err
	}
	for _, registration := range contributions.Agents {
		configured, err := pluginAgentContributionToAssembly(registration.PluginID, registration.Agent)
		if err != nil {
			return err
		}
		name := agentAssemblyNameKey(configured.Name)
		if _, exists := seen[name]; exists {
			return fmt.Errorf("gatewayapp: plugin %q agent %q conflicts with an existing Agent", strings.TrimSpace(registration.PluginID), configured.Name)
		}
		seen[name] = struct{}{}
	}

	for _, configured := range controlagents.ListAgents(doc.ExternalAgents) {
		if forbiddenExternalAgentID(configured.ID) {
			return fmt.Errorf("gatewayapp: external Agent %q conflicts with a product command or system Agent", configured.ID)
		}
		name := agentAssemblyNameKey(configured.ID)
		if _, exists := seen[name]; exists {
			return fmt.Errorf("gatewayapp: external Agent %q conflicts with an existing Agent", configured.ID)
		}
		seen[name] = struct{}{}
	}

	for _, handle := range agentbinding.CatalogFor(doc.AgentBindings).DirectRunHandles() {
		binding, ok := agentbinding.Lookup(doc.AgentBindings, handle)
		if !ok {
			continue
		}
		profile, ok := modelprofile.Lookup(doc.ModelProfiles, binding.ProfileID)
		if !ok || profile.Kind() != modelprofile.BackendProvider {
			continue
		}
		name := agentAssemblyNameKey(string(handle))
		if _, exists := seen[name]; exists {
			return fmt.Errorf("gatewayapp: direct Agent handle %q conflicts with an existing Agent", handle)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func agentAssemblyNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
