package agents

import (
	"context"
	"fmt"
	"strings"
)

// LauncherChoice is the user-selected setup strategy for a local ACP endpoint.
// It remains distinct from Launcher.Kind because global and custom commands
// both become executable launchers after setup.
type LauncherChoice string

const (
	LauncherChoiceNPX       LauncherChoice = "npx"
	LauncherChoiceGlobal    LauncherChoice = "global"
	LauncherChoiceManaged   LauncherChoice = "managed"
	LauncherChoiceInstalled LauncherChoice = "installed"
	LauncherChoiceCommand   LauncherChoice = "command"
)

// ConnectRequest carries one guided local-ACP onboarding selection. One
// request creates or updates exactly one Agent so model-dependent session
// options cannot be shared accidentally across several Agent identities.
type ConnectRequest struct {
	AdapterID    string             `json:"adapter_id,omitempty"`
	Launcher     LauncherChoice     `json:"launcher,omitempty"`
	CommandLine  string             `json:"command_line,omitempty"`
	ModelID      string             `json:"model_id,omitempty"`
	ConfigValues map[string]string  `json:"config_values,omitempty"`
	CWD          string             `json:"cwd,omitempty"`
	Discovery    *DiscoverySnapshot `json:"-"`
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
	if in.Discovery != nil {
		snapshot := NormalizeDiscoverySnapshot(*in.Discovery)
		out.Discovery = &snapshot
	}
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
	for _, model := range snapshot.Models {
		if model.ID == modelID {
			selected = model
			break
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
	return selected, SessionOptions{ModelID: selected.ID, ConfigValues: values}, nil
}

// ConnectResult is the completed persisted roster selection.
type ConnectResult struct {
	Connection Connection        `json:"connection"`
	Agents     []Agent           `json:"agents"`
	Discovery  DiscoverySnapshot `json:"discovery"`
}
