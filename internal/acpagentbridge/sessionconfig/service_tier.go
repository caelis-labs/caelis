package sessionconfig

import (
	"context"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

// A resumed Session may still have the previous model's Fast selection. Apply
// explicitly requested Standard before switching models so that selection cannot
// block the switch. Other defaults remain model-dependent and are applied after
// the switch, including reasserting the requested tier against the new catalog.
func applyStandardBeforeModel(ctx context.Context, acpClient Client, sessionID string, state State, desired controlagents.SessionOptions) (State, error) {
	for _, option := range state.ConfigOptions {
		purpose := controlagents.ClassifyConfigOptionPurpose(controlagents.ConfigOption{ID: option.ID, Name: option.Name, Category: option.Category})
		if purpose != controlagents.ConfigOptionPurposeServiceTier {
			continue
		}
		key, ok := matchingConfigValueKey(desired.ConfigValues, option.ID)
		if !ok {
			continue
		}
		value := desired.ConfigValues[key]
		if value != "default" && value != "standard" {
			continue
		}
		if err := validateChoice(option, value); err != nil {
			// The destination may advertise Standard even when the current
			// model does not. Do not send an unadvertised transition value.
			continue
		}
		var err error
		state.ConfigOptions, err = setConfigOption(ctx, acpClient, sessionID, option.ID, value)
		if err != nil {
			return State{}, err
		}
	}
	return state, nil
}
