package acpagentbridge

import (
	acpsdk "github.com/caelis-labs/acp-go-sdk"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
)

func acpPresentationSnapshot(snapshot appserver.PresentationSnapshot) (*acp.SessionModeState, []acp.SessionConfigOption, []acpsdk.AvailableCommand) {
	var modes *acp.SessionModeState
	if snapshot.Modes != nil && snapshot.Modes.Target != appserver.PresentationModeTargetApproval {
		modes = &acp.SessionModeState{CurrentModeID: snapshot.Modes.CurrentModeID}
		for _, mode := range snapshot.Modes.AvailableModes {
			modes.AvailableModes = append(modes.AvailableModes, acp.SessionMode{ID: mode.ID, Name: mode.Name, Description: mode.Description})
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

func acpPresentationConfigOptions(options []appserver.PresentationConfigOption) []acp.SessionConfigOption {
	result := make([]acp.SessionConfigOption, 0, len(options))
	for _, option := range options {
		mapped := acp.SessionConfigOption{
			Type: option.Type, ID: option.ID, Name: option.Name, Description: option.Description,
			Category: option.Category, CurrentValue: option.CurrentValue,
		}
		for _, choice := range option.Options {
			mapped.Options = append(mapped.Options, acp.SessionConfigSelectOption{Value: choice.Value, Name: choice.Name, Description: choice.Description})
		}
		result = append(result, mapped)
	}
	return result
}
