package builder

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func providerSpeedCapability(cfg modelconfig.Config) modelprofile.SpeedCapability {
	if !modelconfig.SupportsSpeedMode(cfg, "fast") {
		return modelprofile.SpeedCapability{}
	}
	return modelprofile.SpeedCapability{DefaultSpeed: "standard", Choices: []modelprofile.SpeedChoice{
		{Canonical: "standard", WireValue: "default"}, {Canonical: "fast", WireValue: "priority"},
	}}
}

func acpSpeedCapability(discovery agents.DiscoverySnapshot, defaults map[string]string) (modelprofile.SpeedCapability, error) {
	var speed modelprofile.SpeedCapability
	for _, option := range discovery.ConfigOptions {
		if option.Purpose != agents.ConfigOptionPurposeServiceTier {
			continue
		}
		if speed.ACPConfigID != "" {
			return speed, fmt.Errorf("control/modelprofile/builder: multiple service tier selectors")
		}
		speed.ACPConfigID = option.ID
		current := option.CurrentValue
		explicit := false
		for id, value := range defaults {
			if strings.EqualFold(id, option.ID) {
				current = value
				delete(defaults, id)
				explicit = true
			}
		}
		if explicit {
			// The capability reports the selected default; SessionDefaults
			// remains the execution source for an explicitly configured value.
			defaults[option.ID] = current
		}
		for _, choice := range option.Options {
			canonical := choice.Value
			switch choice.Value {
			case "default":
				canonical = "standard"
			case "fast", "priority":
				canonical = "fast"
			}
			speed.Choices = append(speed.Choices, modelprofile.SpeedChoice{Canonical: canonical, WireValue: choice.Value})
			if choice.Value == current {
				speed.DefaultSpeed = canonical
			}
		}
	}
	return speed, nil
}
