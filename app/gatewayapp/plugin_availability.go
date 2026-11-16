package gatewayapp

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/internal/controlassembly"
)

// Persisted package selections can become unavailable after admission. Resolve
// each selected package separately so its failure cannot suppress its siblings.
func (s *runtimeComposition) resolveAvailableApplicationPlugins(profile sessionprofile.Profile, paths plugin.RuntimePaths) (plugin.Contributions, error) {
	doc, err := s.loadGatewayAppConfig()
	if err != nil {
		return plugin.Contributions{}, err
	}
	var out plugin.Contributions
	seen := map[string]bool{}
	for _, selected := range profile.Plugins {
		one := profile
		one.Plugins = []sessionprofile.Plugin{selected}
		part, err := applicationPluginContributions(one, paths)
		if err != nil {
			source := plugin.Source{ID: filepath.Base(selected.Root), Root: selected.Root, Status: "failed", Error: err.Error()}
			if installed, parseErr := plugin.ParseConfiguredAvailable(plugin.Config{Root: selected.Root}); parseErr == nil {
				source.ID, source.Version = installed.ID, installed.Version
			}
			part = plugin.Contributions{Sources: []plugin.Source{source}}
		}
		out.Sources = append(out.Sources, part.Sources...)
		out.MCPServerSpecs = append(out.MCPServerSpecs, part.MCPServerSpecs...)
		out.SkillBundles = append(out.SkillBundles, part.SkillBundles...)
		for _, source := range part.Sources {
			if seen[source.ID] {
				out.Disable(source.ID, fmt.Errorf("duplicate native plugin identity %q", source.ID))
			}
			seen[source.ID] = true
		}
	}
	out = plugin.WithAvailableProductContributions(out, paths, doc.Plugins...)
	isolatePluginMCPConflicts(applicationMCPSpecs(profile), &out, &profile)
	isolatePluginSkillConflicts(profile, &out)
	return out, nil
}

func isolatePluginSkillConflicts(profile sessionprofile.Profile, contributions *plugin.Contributions) {
	var accepted []skill.PluginBundle
	for _, source := range contributions.Sources {
		if source.Error != "" {
			continue
		}
		candidate := append([]skill.PluginBundle(nil), accepted...)
		for _, bundle := range contributions.SkillBundles {
			if bundle.Plugin == source.ID {
				candidate = append(candidate, bundle)
			}
		}
		if _, _, err := assembleApplicationSkills(profile, false, candidate...); err != nil {
			contributions.Disable(source.ID, err)
			continue
		}
		accepted = candidate
	}
}

func isolatePluginMCPConflicts(catalog []mcp.ServerSpec, contributions *plugin.Contributions, explicit *sessionprofile.Profile) {
	counts := map[string]int{}
	for _, source := range contributions.Sources {
		counts[source.ID]++
	}
	var accepted []mcp.ServerSpec
	for _, source := range contributions.Sources {
		if source.Error != "" {
			continue
		}
		if counts[source.ID] > 1 {
			contributions.Disable(source.ID, fmt.Errorf("duplicate plugin identity %q", source.ID))
			continue
		}
		candidate := append([]mcp.ServerSpec(nil), accepted...)
		for _, spec := range contributions.MCPServerSpecs {
			if spec.PluginID == source.ID {
				candidate = append(candidate, spec)
			}
		}
		if _, err := mergeRuntimeMCPSpecs(catalog, candidate); err != nil {
			contributions.Disable(source.ID, err)
			continue
		}
		if explicit != nil {
			if err := validateApplicationMCPNamespaces(*explicit, plugin.Contributions{MCPServerSpecs: candidate}); err != nil {
				contributions.Disable(source.ID, err)
				continue
			}
		}
		accepted = candidate
	}
}

func (s *runtimeComposition) availablePluginAgentAssembly(base controlassembly.ResolvedAssembly, contributions *plugin.Contributions, runtimeCfg stackRuntimeConfig) (controlassembly.ResolvedAssembly, error) {
	resolved, err := s.configuredAssemblyWithPluginAgents(base, nil, runtimeCfg)
	if err != nil {
		return resolved, err
	}
	var accepted []plugin.AgentRegistration
	for _, source := range contributions.Sources {
		if source.Error != "" {
			continue
		}
		candidate := append([]plugin.AgentRegistration(nil), accepted...)
		for _, registration := range contributions.Agents {
			if registration.PluginID == source.ID {
				candidate = append(candidate, registration)
			}
		}
		if len(candidate) == len(accepted) {
			continue
		}
		assembled, err := s.configuredAssemblyWithPluginAgents(base, candidate, runtimeCfg)
		if err != nil {
			contributions.Disable(source.ID, err)
			continue
		}
		resolved, accepted = assembled, candidate
	}
	return resolved, nil
}

func (s *runtimeComposition) reportPluginFailures(contributions plugin.Contributions) {
	for _, source := range contributions.Sources {
		if source.Error == "" {
			continue
		}
		if logger := s.authorities.diagnostics; logger != nil {
			logger.Warn("Plugin disabled", "plugin_id", source.ID, "root", source.Root, "error", source.Error)
		}
		if s.activation == nil || s.retainRuntimeWork == nil || s.authorities.controlFeeds == nil {
			continue
		}
		ref := s.activation.sessionRef
		feed, err := s.authorities.controlFeeds.Session(ref)
		if err != nil || feed == nil {
			continue
		}
		_ = feed.Publish(eventstream.Envelope{
			Kind: eventstream.KindNotice, SessionID: ref.SessionID, Scope: eventstream.ScopeMain,
			OccurredAt: time.Now(), Notice: pluginFailureNotice(source),
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		})
	}
}

func pluginFailureNotice(source plugin.Source) string {
	text := fmt.Sprintf("Plugin %q disabled: %s", source.ID, source.Error)
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	if runes := []rune(text); len(runes) > 1024 {
		text = string(runes[:1024]) + "…"
	}
	return text
}
