package agents

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ConnectState is the typed, opaque state shared by guided ACP onboarding
// completions and the final Control request.
type ConnectState struct {
	Agent        string            `json:"agent"`
	Launcher     LauncherChoice    `json:"launcher"`
	CommandLine  string            `json:"command_line,omitempty"`
	Model        string            `json:"model,omitempty"`
	ConfigValues map[string]string `json:"config_values,omitempty"`
}

// NormalizeConnectState returns a detached canonical guided-connect state.
func NormalizeConnectState(in ConnectState) ConnectState {
	req := NormalizeConnectRequest(ConnectRequest{
		AdapterID: in.Agent, Launcher: in.Launcher, CommandLine: in.CommandLine,
		ModelID: in.Model, ConfigValues: in.ConfigValues,
	})
	return ConnectState{
		Agent: req.AdapterID, Launcher: req.Launcher, CommandLine: req.CommandLine,
		Model: req.ModelID, ConfigValues: req.ConfigValues,
	}
}

// EncodeConnectState serializes guided-connect state as one opaque completion
// argument. Config values remain a typed map, so punctuation in ACP values is
// lossless.
func EncodeConnectState(in ConnectState) string {
	payload, err := json.Marshal(NormalizeConnectState(in))
	if err != nil {
		return ""
	}
	return url.QueryEscape(string(payload))
}

// DecodeConnectState parses one opaque guided-connect completion argument.
func DecodeConnectState(raw string) (ConnectState, error) {
	decoded, err := url.QueryUnescape(strings.TrimSpace(raw))
	if err != nil {
		return ConnectState{}, fmt.Errorf("control/agents: decode connect state: %w", err)
	}
	var state ConnectState
	if err := json.Unmarshal([]byte(decoded), &state); err != nil {
		return ConnectState{}, fmt.Errorf("control/agents: parse connect state: %w", err)
	}
	return NormalizeConnectState(state), nil
}

// ConnectRequest converts guided state into the Control-owned onboarding
// request used for discovery or persistence.
func (s ConnectState) ConnectRequest(cwd string) ConnectRequest {
	s = NormalizeConnectState(s)
	return NormalizeConnectRequest(ConnectRequest{
		AdapterID: s.Agent, Launcher: s.Launcher, CommandLine: s.CommandLine,
		ModelID: s.Model, ConfigValues: s.ConfigValues, CWD: cwd,
	})
}
