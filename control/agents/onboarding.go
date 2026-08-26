package agents

import (
	"context"
	"fmt"
	"strings"
)

// LauncherChoice is the requested onboarding strategy for a local ACP
// endpoint. New product catalogs declare only Installed or Command. The npx,
// global, and managed values remain wire-readable for persisted preparations
// created by older Caelis versions and must not be offered by new onboarding.
type LauncherChoice string

const (
	// LauncherChoiceNPX is retained only for legacy preparation compatibility.
	LauncherChoiceNPX LauncherChoice = "npx"
	// LauncherChoiceGlobal is retained only for legacy preparation compatibility.
	LauncherChoiceGlobal LauncherChoice = "global"
	// LauncherChoiceManaged is retained only for legacy preparation compatibility.
	LauncherChoiceManaged   LauncherChoice = "managed"
	LauncherChoiceInstalled LauncherChoice = "installed"
	LauncherChoiceCommand   LauncherChoice = "command"
)

// ConnectRequest carries one guided local-ACP onboarding selection. One
// request creates or updates exactly one Agent so model-dependent session
// options cannot be shared accidentally across several Agent identities.
type ConnectRequest struct {
	AdapterID    string            `json:"adapter_id,omitempty"`
	Launcher     LauncherChoice    `json:"launcher,omitempty"`
	CommandLine  string            `json:"command_line,omitempty"`
	ModelID      string            `json:"model_id,omitempty"`
	ConfigValues map[string]string `json:"config_values,omitempty"`
	CWD          string            `json:"cwd,omitempty"`
}

// NormalizeConnectRequest returns a detached canonical onboarding request.
func NormalizeConnectRequest(in ConnectRequest) ConnectRequest {
	out := ConnectRequest{
		AdapterID:   normalizeID(in.AdapterID),
		Launcher:    LauncherChoice(strings.ToLower(strings.TrimSpace(string(in.Launcher)))),
		CommandLine: strings.TrimSpace(in.CommandLine),
		ModelID:     strings.TrimSpace(in.ModelID),
		CWD:         strings.TrimSpace(in.CWD),
	}
	out.ConfigValues = NormalizeSessionOptions(SessionOptions{ConfigValues: in.ConfigValues}).ConfigValues
	return out
}

// Connector is the Control-owned local ACP onboarding capability. Surfaces
// receive this narrow facet explicitly instead of discovering methods on the
// transitional aggregate control service at runtime.
type Connector interface {
	DiscoverACPConnection(context.Context, ConnectRequest) (DiscoverySnapshot, error)
	ConnectACP(context.Context, ConnectRequest) (ConnectResult, error)
}

// ResolveDiscoverySelection validates one Agent selection against the exact
// model-scoped discovery snapshot shown to the user.
func ResolveDiscoverySelection(snapshot DiscoverySnapshot, modelID string, configValues map[string]string) (RemoteModel, SessionOptions, error) {
	snapshot = NormalizeDiscoverySnapshot(snapshot)
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: select one ACP model")
	}
	if snapshot.SelectedModelID != "" && snapshot.SelectedModelID != modelID {
		return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: discovery snapshot is for model %q, not %q", snapshot.SelectedModelID, modelID)
	}
	var selected RemoteModel
	switch {
	case IsDefaultRemoteModelID(modelID):
		if len(snapshot.Models) != 0 {
			return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: Agent default is valid only when no ACP models are advertised")
		}
		selected = RemoteModel{ID: DefaultRemoteModelID, Name: "Agent default"}
	default:
		for _, model := range snapshot.Models {
			if model.ID == modelID {
				selected = model
				break
			}
		}
	}
	if selected.ID == "" {
		return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: ACP model %q is no longer advertised", modelID)
	}
	values := NormalizeSessionOptions(SessionOptions{ConfigValues: configValues}).ConfigValues
	available := make(map[string]ConfigOption, len(snapshot.ConfigOptions))
	for _, option := range snapshot.ConfigOptions {
		available[option.ID] = option
	}
	for configID, value := range values {
		if snapshot.ModelControl.ConfigID != "" && strings.EqualFold(configID, snapshot.ModelControl.ConfigID) {
			return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: model config option %q is selected through ModelID and cannot also be a session default", configID)
		}
		option, ok := available[configID]
		if !ok {
			return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: ACP config option %q is no longer advertised", configID)
		}
		valid := false
		for _, choice := range option.Options {
			if choice.Value == value {
				valid = true
				break
			}
		}
		if !valid {
			return RemoteModel{}, SessionOptions{}, fmt.Errorf("control/agents: ACP config value %q for %q is no longer advertised", value, configID)
		}
	}
	sessionModelID := selected.ID
	if IsDefaultRemoteModelID(sessionModelID) {
		sessionModelID = ""
	}
	return selected, SessionOptions{ModelID: sessionModelID, ConfigValues: values}, nil
}

// ConnectedProfile is the presentation-safe identity of a ModelProfile
// created by one ACP onboarding command. The canonical profile remains owned
// by Host configuration and is not reconstructed by presentation adapters.
type ConnectedProfile struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// ConnectResult is the completed persisted roster selection.
type ConnectResult struct {
	Connection Connection         `json:"connection"`
	Profiles   []ConnectedProfile `json:"profiles"`
	Discovery  DiscoverySnapshot  `json:"discovery"`
}
