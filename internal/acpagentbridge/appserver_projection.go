package acpagentbridge

import (
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp"
)

func acpPresentationSnapshot(snapshot controlclient.PresentationSnapshot) (*acp.SessionModeState, []acp.SessionConfigOption, *acp.SessionModelState, []acp.AvailableCommand) {
	var modes *acp.SessionModeState
	if snapshot.Modes != nil && snapshot.Modes.Target != controlclient.PresentationModeTargetApproval {
		modes = &acp.SessionModeState{CurrentModeID: snapshot.Modes.CurrentModeID}
		for _, mode := range snapshot.Modes.AvailableModes {
			modes.AvailableModes = append(modes.AvailableModes, acp.SessionMode{ID: mode.ID, Name: mode.Name, Description: mode.Description})
		}
	}
	configs := acpPresentationConfigOptions(snapshot.ConfigOptions)
	var models *acp.SessionModelState
	if snapshot.Models != nil {
		models = &acp.SessionModelState{CurrentModelID: snapshot.Models.CurrentModelID}
		for _, model := range snapshot.Models.AvailableModels {
			models.AvailableModels = append(models.AvailableModels, acp.ModelInfo{ModelID: model.ID, Name: model.Name, Description: model.Description})
		}
	}
	commands := make([]acp.AvailableCommand, 0, len(snapshot.Commands))
	for _, command := range snapshot.Commands {
		mapped := acp.AvailableCommand{Name: command.Name, Description: command.Description}
		if command.Input != nil {
			mapped.Input = &acp.AvailableCommandInput{Hint: command.Input.Hint}
		}
		commands = append(commands, mapped)
	}
	return modes, configs, models, commands
}

func acpPresentationConfigOptions(options []controlclient.PresentationConfigOption) []acp.SessionConfigOption {
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
