package client

import (
	"encoding/json"
	"fmt"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

// decodeStandardSessionStateUpdate delegates validation of standard ACP
// session-state variants to the SDK before adapting them to Host-private state.
func decodeStandardSessionStateUpdate(raw json.RawMessage, discriminator string) (Update, error) {
	var update acpsdk.SessionUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil, err
	}

	switch discriminator {
	case UpdateAvailableCmds:
		if update.AvailableCommandsUpdate == nil {
			return nil, fmt.Errorf("ACP update %q did not decode as the expected SDK variant", discriminator)
		}
		return *update.AvailableCommandsUpdate, nil
	case UpdateCurrentMode:
		if update.CurrentModeUpdate == nil {
			return nil, fmt.Errorf("ACP update %q did not decode as the expected SDK variant", discriminator)
		}
		return *update.CurrentModeUpdate, nil
	case UpdateConfigOption:
		if update.ConfigOptionUpdate == nil {
			return nil, fmt.Errorf("ACP update %q did not decode as the expected SDK variant", discriminator)
		}
		return configOptionUpdateFromSDK(*update.ConfigOptionUpdate), nil
	case UpdateSessionInfo:
		if update.SessionInfoUpdate == nil {
			return nil, fmt.Errorf("ACP update %q did not decode as the expected SDK variant", discriminator)
		}
		return sessionInfoUpdateFromSDK(raw, *update.SessionInfoUpdate)
	default:
		return nil, fmt.Errorf("ACP update %q is not a standard session-state variant", discriminator)
	}
}

func configOptionUpdateFromSDK(update acpsdk.SessionConfigOptionUpdate) ConfigOptionUpdate {
	options := make([]SessionConfigOption, 0, len(update.ConfigOptions))
	for _, option := range update.ConfigOptions {
		switch {
		case option.Select != nil:
			selectOption := option.Select
			normalized := SessionConfigOption{
				Type:         selectOption.Type,
				ID:           string(selectOption.Id),
				Name:         selectOption.Name,
				CurrentValue: string(selectOption.CurrentValue),
			}
			if selectOption.Description != nil {
				normalized.Description = *selectOption.Description
			}
			if selectOption.Category != nil {
				normalized.Category = string(*selectOption.Category)
			}
			normalized.Options = sessionConfigChoicesFromSDK(selectOption.Options)
			options = append(options, normalized)
		case option.Boolean != nil:
			booleanOption := option.Boolean
			normalized := SessionConfigOption{
				Type:         booleanOption.Type,
				ID:           string(booleanOption.Id),
				Name:         booleanOption.Name,
				CurrentValue: booleanOption.CurrentValue,
			}
			if booleanOption.Description != nil {
				normalized.Description = *booleanOption.Description
			}
			if booleanOption.Category != nil {
				normalized.Category = string(*booleanOption.Category)
			}
			options = append(options, normalized)
		}
	}
	return ConfigOptionUpdate{
		SessionUpdate: update.SessionUpdate,
		ConfigOptions: options,
	}
}

func sessionConfigChoicesFromSDK(options acpsdk.SessionConfigSelectOptions) []SessionConfigSelectOption {
	var choices []acpsdk.SessionConfigSelectOption
	switch {
	case options.Ungrouped != nil:
		choices = append(choices, (*options.Ungrouped)...)
	case options.Grouped != nil:
		for _, group := range *options.Grouped {
			choices = append(choices, group.Options...)
		}
	}
	if len(choices) == 0 {
		return nil
	}
	normalized := make([]SessionConfigSelectOption, 0, len(choices))
	for _, choice := range choices {
		item := SessionConfigSelectOption{
			Value: string(choice.Value),
			Name:  choice.Name,
		}
		if choice.Description != nil {
			item.Description = *choice.Description
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func sessionInfoUpdateFromSDK(raw json.RawMessage, update acpsdk.SessionSessionInfoUpdate) (SessionInfoUpdate, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return SessionInfoUpdate{}, err
	}
	_, titlePresent := fields["title"]
	_, updatedAtPresent := fields["updatedAt"]
	return SessionInfoUpdate{
		SessionUpdate:    update.SessionUpdate,
		Title:            update.Title,
		TitlePresent:     titlePresent,
		UpdatedAt:        update.UpdatedAt,
		UpdatedAtPresent: updatedAtPresent,
	}, nil
}
