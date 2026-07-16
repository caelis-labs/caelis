// Package sessionconfig normalizes and applies Control-selected defaults to an
// external ACP session before it receives a prompt.
package sessionconfig

import (
	"context"
	"fmt"
	"sort"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

// Client is the ACP session-configuration subset used by Apply.
type Client interface {
	SetConfigOption(context.Context, string, string, any) (client.SetSessionConfigOptionResponse, error)
	SetModel(context.Context, string, string) error
}

// State is the model/config state returned by session/new, load, or resume.
type State struct {
	ConfigOptions []client.SessionConfigOption
	Models        *client.SessionModelState
}

// Apply validates and applies desired defaults against the real session
// handshake. Explicit unavailable values fail closed; they never silently fall
// back to the external Agent's default.
func Apply(ctx context.Context, acpClient Client, sessionID string, state State, desired controlagents.SessionOptions) (State, error) {
	desired = controlagents.NormalizeSessionOptions(desired)
	if desired.ModelID == "" && len(desired.ConfigValues) == 0 {
		return cloneState(state), nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if acpClient == nil {
		return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: client is required")
	}
	if sessionID == "" {
		return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: session id is required")
	}
	state = cloneState(state)
	modelOption, hasModelOption := findModelOption(state.ConfigOptions)
	if desired.ModelID != "" {
		if hasModelOption {
			if err := validateChoice(*modelOption, desired.ModelID); err != nil {
				return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: model %q is unavailable: %w", desired.ModelID, err)
			}
			if currentValue(modelOption.CurrentValue) != desired.ModelID {
				resp, err := acpClient.SetConfigOption(ctx, sessionID, modelOption.ID, desired.ModelID)
				if err != nil {
					return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: set model %q through config option %q: %w", desired.ModelID, modelOption.ID, err)
				}
				state.ConfigOptions, err = validatedConfigOptionResponse(resp, modelOption.ID, desired.ModelID)
				if err != nil {
					return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: set model %q through config option %q: %w", desired.ModelID, modelOption.ID, err)
				}
			}
		} else {
			if !hasModel(state.Models, desired.ModelID) {
				return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: model %q is not advertised by the ACP session", desired.ModelID)
			}
			if state.Models == nil || strings.TrimSpace(state.Models.CurrentModelID) != desired.ModelID {
				if err := acpClient.SetModel(ctx, sessionID, desired.ModelID); err != nil {
					return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: set model %q: %w", desired.ModelID, err)
				}
				if state.Models == nil {
					state.Models = &client.SessionModelState{}
				}
				state.Models.CurrentModelID = desired.ModelID
			}
		}
	}

	keys := make([]string, 0, len(desired.ConfigValues))
	for key := range desired.ConfigValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, configID := range keys {
		value := desired.ConfigValues[configID]
		option, ok := findConfigOption(state.ConfigOptions, configID)
		if !ok {
			return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: config option %q is not advertised by the ACP session", configID)
		}
		currentModelOption, hasCurrentModelOption := findModelOption(state.ConfigOptions)
		if hasCurrentModelOption && strings.EqualFold(option.ID, currentModelOption.ID) {
			if desired.ModelID != "" && value == desired.ModelID {
				continue
			}
			return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: model config option %q conflicts with desired model %q", option.ID, desired.ModelID)
		}
		if err := validateChoice(option, value); err != nil {
			return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: value %q for config option %q is unavailable: %w", value, option.ID, err)
		}
		if currentValue(option.CurrentValue) == value {
			continue
		}
		resp, err := acpClient.SetConfigOption(ctx, sessionID, option.ID, value)
		if err != nil {
			return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: set config option %q to %q: %w", option.ID, value, err)
		}
		state.ConfigOptions, err = validatedConfigOptionResponse(resp, option.ID, value)
		if err != nil {
			return State{}, fmt.Errorf("internal/acpagentbridge/sessionconfig: set config option %q to %q: %w", option.ID, value, err)
		}
	}
	return state, nil
}

// Snapshot converts a real ACP session handshake into a cacheable Control view.
func Snapshot(connection controlagents.Connection, cwd string, protocolVersion int, state State) controlagents.DiscoverySnapshot {
	connection = controlagents.NormalizeConnection(connection)
	state = cloneState(state)
	out := controlagents.DiscoverySnapshot{
		ConnectionID:      connection.ID,
		LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher),
		CWD:               strings.TrimSpace(cwd),
		ProtocolVersion:   protocolVersion,
		ConfigOptions:     configOptionsForControl(state.ConfigOptions),
	}
	if modelOption, ok := findModelOption(state.ConfigOptions); ok {
		out.CurrentModelID = currentValue(modelOption.CurrentValue)
		out.ModelControl = controlagents.ModelControl{Kind: controlagents.ModelControlConfigOption, ConfigID: strings.TrimSpace(modelOption.ID)}
		for _, choice := range modelOption.Options {
			id := strings.TrimSpace(choice.Value)
			if id == "" {
				continue
			}
			out.Models = append(out.Models, controlagents.RemoteModel{ID: id, Name: strings.TrimSpace(choice.Name), Description: strings.TrimSpace(choice.Description)})
		}
	} else if state.Models != nil {
		out.CurrentModelID = strings.TrimSpace(state.Models.CurrentModelID)
		out.ModelControl = controlagents.ModelControl{Kind: controlagents.ModelControlSetModel}
		for _, model := range state.Models.AvailableModels {
			id := strings.TrimSpace(model.ModelID)
			if id == "" {
				continue
			}
			out.Models = append(out.Models, controlagents.RemoteModel{ID: id, Name: strings.TrimSpace(model.Name), Description: strings.TrimSpace(model.Description)})
		}
	}
	return controlagents.NormalizeDiscoverySnapshot(out)
}

func findModelOption(options []client.SessionConfigOption) (*client.SessionConfigOption, bool) {
	for i := range options {
		if selectableConfigOption(options[i]) && strings.EqualFold(strings.TrimSpace(options[i].Category), "model") {
			return &options[i], true
		}
	}
	for i := range options {
		if !selectableConfigOption(options[i]) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(options[i].ID)) {
		case "model", "models", "model_id", "modelid":
			return &options[i], true
		}
	}
	return nil, false
}

func findConfigOption(options []client.SessionConfigOption, id string) (client.SessionConfigOption, bool) {
	id = strings.TrimSpace(id)
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.ID), id) {
			return option, true
		}
	}
	return client.SessionConfigOption{}, false
}

func validateChoice(option client.SessionConfigOption, value string) error {
	value = strings.TrimSpace(value)
	for _, choice := range option.Options {
		if strings.TrimSpace(choice.Value) == value {
			return nil
		}
	}
	return fmt.Errorf("advertised choices do not include the requested value")
}

func hasModel(models *client.SessionModelState, modelID string) bool {
	if models == nil {
		return false
	}
	for _, model := range models.AvailableModels {
		if strings.TrimSpace(model.ModelID) == strings.TrimSpace(modelID) {
			return true
		}
	}
	return false
}

func currentValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func cloneState(in State) State {
	out := State{ConfigOptions: cloneConfigOptions(in.ConfigOptions)}
	if in.Models != nil {
		out.Models = &client.SessionModelState{CurrentModelID: strings.TrimSpace(in.Models.CurrentModelID)}
		out.Models.AvailableModels = append([]client.ModelInfo(nil), in.Models.AvailableModels...)
	}
	return out
}

func cloneConfigOptions(in []client.SessionConfigOption) []client.SessionConfigOption {
	out := make([]client.SessionConfigOption, 0, len(in))
	for _, option := range in {
		option.Options = append([]client.SessionConfigSelectOption(nil), option.Options...)
		out = append(out, option)
	}
	return out
}

func validatedConfigOptionResponse(resp client.SetSessionConfigOptionResponse, id string, value string) ([]client.SessionConfigOption, error) {
	options := cloneConfigOptions(resp.ConfigOptions)
	updated, ok := findConfigOption(options, id)
	if !ok {
		return nil, fmt.Errorf("ACP response omitted updated config option %q", id)
	}
	if got := currentValue(updated.CurrentValue); got != strings.TrimSpace(value) {
		return nil, fmt.Errorf("ACP response reported current value %q for config option %q, want %q", got, id, strings.TrimSpace(value))
	}
	if err := validateChoice(updated, value); err != nil {
		return nil, fmt.Errorf("ACP response no longer advertises current value %q for config option %q", strings.TrimSpace(value), id)
	}
	return options, nil
}

func configOptionsForControl(options []client.SessionConfigOption) []controlagents.ConfigOption {
	out := make([]controlagents.ConfigOption, 0, len(options))
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" || !persistedSelectionOption(option) {
			continue
		}
		item := controlagents.ConfigOption{
			ID:           id,
			Name:         strings.TrimSpace(option.Name),
			Type:         strings.TrimSpace(option.Type),
			Category:     strings.TrimSpace(option.Category),
			Description:  strings.TrimSpace(option.Description),
			CurrentValue: currentValue(option.CurrentValue),
		}
		for _, choice := range option.Options {
			value := strings.TrimSpace(choice.Value)
			if value == "" {
				continue
			}
			item.Options = append(item.Options, controlagents.ConfigChoice{Value: value, Name: strings.TrimSpace(choice.Name), Description: strings.TrimSpace(choice.Description)})
		}
		out = append(out, item)
	}
	return out
}

func persistedSelectionOption(option client.SessionConfigOption) bool {
	return selectableConfigOption(option)
}

func selectableConfigOption(option client.SessionConfigOption) bool {
	return strings.EqualFold(strings.TrimSpace(option.Type), "select") && len(option.Options) > 0
}
