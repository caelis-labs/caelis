// Package builder constructs standard Control ModelProfiles from configured
// provider models and external ACP discovery results.
package builder

import (
	"fmt"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// FromProvider constructs the selectable profile for one configured provider
// model. The provider endpoint remains infrastructure referenced by cfg.
func FromProvider(raw modelconfig.Config) (modelprofile.ModelProfile, error) {
	configured := modelconfig.NormalizeConfig(raw)
	if configured.ID == "" {
		return modelprofile.ModelProfile{}, fmt.Errorf("control/modelprofile/builder: provider model config ID is required")
	}
	profile := modelprofile.Normalize(modelprofile.ModelProfile{
		ID:          modelprofile.BuildProviderID(configured.ID),
		DisplayName: firstNonEmpty(configured.Alias, configured.Model, configured.ID),
		Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{
			ModelConfigID: configured.ID,
		}},
		Effort: providerEffortCapability(configured),
	})
	if err := modelprofile.Validate(profile); err != nil {
		return modelprofile.ModelProfile{}, err
	}
	return profile, nil
}

// FromACP constructs one selectable profile for an exact remote model. The
// Agent remains connection-scoped; model selection and defaults live only in
// the returned profile.
func FromACP(
	agent controlagents.Agent,
	connection controlagents.Connection,
	remoteModel controlagents.RemoteModel,
	sessionOptions controlagents.SessionOptions,
	discovery controlagents.DiscoverySnapshot,
) (modelprofile.ModelProfile, error) {
	agent = controlagents.NormalizeAgent(agent)
	connection = controlagents.NormalizeConnection(connection)
	sessionOptions = controlagents.NormalizeSessionOptions(sessionOptions)
	discovery = controlagents.NormalizeDiscoverySnapshot(discovery)
	remoteModel.ID = strings.TrimSpace(remoteModel.ID)
	remoteModel.Name = strings.TrimSpace(remoteModel.Name)
	if agent.ID == "" || remoteModel.ID == "" {
		return modelprofile.ModelProfile{}, fmt.Errorf("control/modelprofile/builder: ACP Agent and remote model are required")
	}
	if agent.ConnectionID == "" || agent.ConnectionID != connection.ID {
		return modelprofile.ModelProfile{}, fmt.Errorf(
			"control/modelprofile/builder: ACP Agent %q does not reference connection %q",
			agent.ID,
			connection.ID,
		)
	}

	nonEffortDefaults := make(map[string]string, len(sessionOptions.ConfigValues))
	for id, value := range sessionOptions.ConfigValues {
		id = strings.TrimSpace(id)
		if id == "" || strings.EqualFold(id, discovery.ModelControl.ConfigID) {
			continue
		}
		nonEffortDefaults[id] = strings.TrimSpace(value)
	}

	var effortOption *controlagents.ConfigOption
	for index := range discovery.ConfigOptions {
		option := discovery.ConfigOptions[index]
		if option.Purpose != controlagents.ConfigOptionPurposeReasoningEffort {
			continue
		}
		if effortOption != nil {
			return modelprofile.ModelProfile{}, fmt.Errorf(
				"control/modelprofile/builder: ACP model %q advertises multiple effort selectors",
				remoteModel.ID,
			)
		}
		effortOption = &option
	}

	effort := modelprofile.EffortCapability{
		DefaultEffort: "none",
		Choices:       []modelprofile.EffortChoice{{Canonical: "none"}},
	}
	if effortOption != nil {
		selectedWire := strings.TrimSpace(effortOption.CurrentValue)
		for id, value := range nonEffortDefaults {
			if strings.EqualFold(id, effortOption.ID) {
				selectedWire = value
				delete(nonEffortDefaults, id)
			}
		}
		effort = modelprofile.EffortCapability{ACPConfigID: strings.TrimSpace(effortOption.ID)}
		seen := map[string]string{}
		for _, choice := range effortOption.Options {
			wire := strings.TrimSpace(choice.Value)
			canonical := modelcatalog.NormalizeReasoningEffort(wire)
			if canonical == "" || wire == "" {
				continue
			}
			if previous, ok := seen[canonical]; ok && previous != wire {
				return modelprofile.ModelProfile{}, fmt.Errorf(
					"control/modelprofile/builder: ACP effort values %q and %q both map to %q",
					previous,
					wire,
					canonical,
				)
			}
			if _, ok := seen[canonical]; ok {
				continue
			}
			seen[canonical] = wire
			effort.Choices = append(effort.Choices, modelprofile.EffortChoice{
				Canonical: canonical,
				WireValue: wire,
			})
			if wire == selectedWire {
				effort.DefaultEffort = canonical
			}
		}
		if effort.DefaultEffort == "" {
			return modelprofile.ModelProfile{}, fmt.Errorf(
				"control/modelprofile/builder: ACP model %q selected effort %q is not advertised",
				remoteModel.ID,
				selectedWire,
			)
		}
	}
	if len(nonEffortDefaults) == 0 {
		nonEffortDefaults = nil
	}

	displayName := firstNonEmpty(connection.Name, agent.Name, agent.ID)
	if modelName := firstNonEmpty(remoteModel.Name, remoteModel.ID); modelName != "" {
		displayName += " — " + modelName
	}
	profile := modelprofile.Normalize(modelprofile.ModelProfile{
		ID:          modelprofile.BuildACPID(agent.ID, remoteModel.ID),
		DisplayName: displayName,
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
			AgentID:         agent.ID,
			RemoteModelID:   remoteModel.ID,
			SessionDefaults: nonEffortDefaults,
		}},
		Effort: effort,
	})
	if err := modelprofile.Validate(profile); err != nil {
		return modelprofile.ModelProfile{}, err
	}
	return profile, nil
}

func providerEffortCapability(configured modelconfig.Config) modelprofile.EffortCapability {
	configured = modelconfig.NormalizeConfig(configured)
	values := append([]string(nil), configured.ReasoningLevels...)
	values = append(values, configured.DefaultReasoningEffort, configured.ReasoningEffort)
	seen := map[string]struct{}{}
	capability := modelprofile.EffortCapability{}
	for _, raw := range values {
		canonical := modelcatalog.NormalizeReasoningEffort(raw)
		if canonical == "" {
			continue
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		capability.Choices = append(capability.Choices, modelprofile.EffortChoice{
			Canonical: canonical,
			WireValue: canonical,
		})
	}
	capability.DefaultEffort = modelcatalog.NormalizeReasoningEffort(firstNonEmpty(
		configured.DefaultReasoningEffort,
		configured.ReasoningEffort,
	))
	if len(capability.Choices) == 0 {
		return modelprofile.EffortCapability{
			DefaultEffort: "none",
			Choices:       []modelprofile.EffortChoice{{Canonical: "none", WireValue: "none"}},
		}
	}
	if capability.DefaultEffort == "" {
		capability.DefaultEffort = capability.Choices[0].Canonical
	}
	return capability
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
