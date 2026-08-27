package acpagentbridge

import (
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func acpPresentationSnapshot(snapshot appserver.PresentationSnapshot) (*acpsdk.SessionModeState, []acpsdk.SessionConfigOption, []acpsdk.AvailableCommand) {
	var modes *acpsdk.SessionModeState
	if snapshot.Modes != nil && snapshot.Modes.Target != appserver.PresentationModeTargetApproval {
		modes = &acpsdk.SessionModeState{CurrentModeId: acpsdk.SessionModeId(snapshot.Modes.CurrentModeID)}
		for _, mode := range snapshot.Modes.AvailableModes {
			mapped := acpsdk.SessionMode{Id: acpsdk.SessionModeId(mode.ID), Name: mode.Name}
			if description := strings.TrimSpace(mode.Description); description != "" {
				mapped.Description = &description
			}
			modes.AvailableModes = append(modes.AvailableModes, mapped)
		}
	}
	configs := acpPresentationConfigOptions(snapshot.ConfigOptions)
	commands := make([]acpsdk.AvailableCommand, 0, len(snapshot.Commands))
	for _, command := range snapshot.Commands {
		mapped := acpsdk.AvailableCommand{Name: command.Name, Description: command.Description}
		if command.Input != nil {
			mapped.Input = &acpsdk.AvailableCommandInput{Unstructured: &acpsdk.UnstructuredCommandInput{Hint: command.Input.Hint}}
		}
		commands = append(commands, mapped)
	}
	return modes, configs, commands
}

func acpPresentationConfigOptions(options []appserver.PresentationConfigOption) []acpsdk.SessionConfigOption {
	result := make([]acpsdk.SessionConfigOption, 0, len(options))
	for _, option := range options {
		switch strings.TrimSpace(option.Type) {
		case "select":
			currentValue, ok := option.CurrentValue.(string)
			if !ok {
				continue
			}
			selectOption := acpsdk.SessionConfigOptionSelect{
				Type:         "select",
				Id:           acpsdk.SessionConfigId(option.ID),
				Name:         option.Name,
				CurrentValue: acpsdk.SessionConfigValueId(currentValue),
			}
			applyACPConfigOptionDisplay(&selectOption.Description, &selectOption.Category, option)
			choices := make(acpsdk.SessionConfigSelectOptionsUngrouped, 0, len(option.Options))
			for _, choice := range option.Options {
				mapped := acpsdk.SessionConfigSelectOption{
					Value: acpsdk.SessionConfigValueId(choice.Value),
					Name:  choice.Name,
				}
				if description := strings.TrimSpace(choice.Description); description != "" {
					mapped.Description = &description
				}
				choices = append(choices, mapped)
			}
			selectOption.Options = acpsdk.SessionConfigSelectOptions{Ungrouped: &choices}
			result = append(result, acpsdk.SessionConfigOption{Select: &selectOption})
		}
	}
	return result
}

func applyACPConfigOptionDisplay(description **string, category **acpsdk.SessionConfigOptionCategory, option appserver.PresentationConfigOption) {
	if value := strings.TrimSpace(option.Description); value != "" {
		*description = &value
	}
	if value := strings.TrimSpace(option.Category); value != "" {
		typed := acpsdk.SessionConfigOptionCategory(value)
		*category = &typed
	}
}

func acpSessionConfigMutation(req acpsdk.SetSessionConfigOptionRequest) (sessionID string, configID string, value any, err error) {
	switch {
	case req.ValueId != nil:
		return strings.TrimSpace(string(req.ValueId.SessionId)), strings.TrimSpace(string(req.ValueId.ConfigId)), string(req.ValueId.Value), nil
	case req.Boolean != nil:
		return strings.TrimSpace(string(req.Boolean.SessionId)), strings.TrimSpace(string(req.Boolean.ConfigId)), req.Boolean.Value, nil
	default:
		return "", "", nil, fmt.Errorf("internal/acpagentbridge: session config request has no value")
	}
}
