package modelprofile

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
)

// SpeedChoice maps a product speed to an exact backend service tier.
type SpeedChoice struct {
	Canonical string `json:"canonical"`
	WireValue string `json:"wire_value"`
}

// SpeedCapability is a discovered, model-specific selector. Empty Choices
// means unsupported/unknown, not Off. Empty selections preserve profile defaults
// when configured and otherwise inherit the backend default.
type SpeedCapability struct {
	DefaultSpeed string        `json:"default_speed,omitempty"`
	Choices      []SpeedChoice `json:"choices,omitempty"`
	ACPConfigID  string        `json:"acp_config_id,omitempty"`
}

func normalizeSpeed(in SpeedCapability) SpeedCapability {
	out := SpeedCapability{DefaultSpeed: strings.TrimSpace(in.DefaultSpeed), ACPConfigID: strings.TrimSpace(in.ACPConfigID)}
	for _, choice := range in.Choices {
		out.Choices = append(out.Choices, SpeedChoice{Canonical: strings.TrimSpace(choice.Canonical), WireValue: strings.TrimSpace(choice.WireValue)})
	}
	return out
}

// WireSpeed resolves an explicit selection; inheritance is represented by omission.
func (p ModelProfile) WireSpeed(speed string) (string, bool) {
	for _, choice := range p.Speed.Choices {
		if choice.Canonical == speed {
			return choice.WireValue, true
		}
	}
	return "", false
}

// SupportsFast reports an advertised pair of standard and Fast choices.
func (p ModelProfile) SupportsFast() bool {
	_, standard := p.WireSpeed("standard")
	_, fast := p.WireSpeed("fast")
	return standard && fast
}

// SelectedSpeed reads the frozen execution selection without consulting a binding.
func (p ModelProfile) SelectedSpeed(frozen placement.Placement) string {
	wire := frozen.ServiceTier
	if p.Kind() == BackendACP {
		wire = frozen.SessionConfigValues[p.Speed.ACPConfigID]
	}
	for _, choice := range p.Speed.Choices {
		if choice.WireValue == wire {
			return choice.Canonical
		}
	}
	return p.Speed.DefaultSpeed
}

// ApplySpeed seals an explicit speed into the existing execution configuration.
// It does not change the profile configuration fingerprint or other selections.
func (p ModelProfile) ApplySpeed(frozen placement.Placement, speed string) (placement.Placement, error) {
	if speed == "" {
		return frozen, nil
	}
	wire, ok := p.WireSpeed(speed)
	if !ok {
		return placement.Placement{}, fmt.Errorf("control/modelprofile: profile %q does not support speed %q", p.ID, speed)
	}
	frozen = placement.Normalize(frozen)
	if p.Kind() == BackendACP {
		if frozen.SessionConfigValues == nil {
			frozen.SessionConfigValues = map[string]string{}
		}
		frozen.SessionConfigValues[p.Speed.ACPConfigID] = wire
	} else {
		frozen.ServiceTier = wire
	}
	return placement.Seal(frozen)
}

func validateSpeed(p ModelProfile) error {
	if len(p.Speed.Choices) == 0 {
		if p.Speed.DefaultSpeed != "" || p.Speed.ACPConfigID != "" {
			return fmt.Errorf("control/modelprofile: speed selector requires choices")
		}
		return nil
	}
	seen := map[string]bool{}
	for _, choice := range p.Speed.Choices {
		if choice.Canonical == "" || choice.WireValue == "" || seen[choice.Canonical] {
			return fmt.Errorf("control/modelprofile: invalid speed choice for %q", p.ID)
		}
		seen[choice.Canonical] = true
	}
	if !seen[p.Speed.DefaultSpeed] {
		return fmt.Errorf("control/modelprofile: default speed is not advertised for %q", p.ID)
	}
	if p.Kind() == BackendACP {
		if p.Speed.ACPConfigID == "" || p.Speed.ACPConfigID == p.Effort.ACPConfigID {
			return fmt.Errorf("control/modelprofile: ACP speed requires its own config ID")
		}
		for id, value := range p.Backend.ACP.SessionDefaults {
			if strings.EqualFold(id, p.Speed.ACPConfigID) {
				wire, _ := p.WireSpeed(p.Speed.DefaultSpeed)
				if id != p.Speed.ACPConfigID || value != wire {
					return fmt.Errorf("control/modelprofile: speed capability disagrees with session default %q", id)
				}
			}
		}
	} else if p.Speed.ACPConfigID != "" {
		return fmt.Errorf("control/modelprofile: provider speed must not declare ACP config ID")
	}
	return nil
}
