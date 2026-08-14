package sessionconfig

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

type fakeClient struct {
	calls     []string
	responses []client.SetSessionConfigOptionResponse
}

func (f *fakeClient) SetConfigOption(_ context.Context, sessionID string, configID string, value any) (client.SetSessionConfigOptionResponse, error) {
	f.calls = append(f.calls, fmt.Sprintf("config:%s:%s:%v", sessionID, configID, value))
	if len(f.responses) == 0 {
		return client.SetSessionConfigOptionResponse{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *fakeClient) SetModel(_ context.Context, sessionID string, modelID string) error {
	f.calls = append(f.calls, fmt.Sprintf("model:%s:%s", sessionID, modelID))
	return nil
}

func TestApplyConfigModelAndEffortBeforePrompt(t *testing.T) {
	t.Parallel()

	acpClient := &fakeClient{responses: []client.SetSessionConfigOptionResponse{
		{ConfigOptions: []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "opus",
				Options: []client.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
			},
			{
				ID: "effort", Name: "Reasoning effort", Type: "select", Category: "reasoning", CurrentValue: "high",
				Options: []client.SessionConfigSelectOption{{Value: "high", Name: "High"}, {Value: "max", Name: "Max"}},
			},
		}},
		{ConfigOptions: []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "opus",
				Options: []client.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
			},
			{
				ID: "effort", Name: "Reasoning effort", Type: "select", Category: "reasoning", CurrentValue: "max",
				Options: []client.SessionConfigSelectOption{{Value: "high", Name: "High"}, {Value: "max", Name: "Max"}},
			},
		}},
	}}
	state, err := Apply(context.Background(), acpClient, "session-1", State{ConfigOptions: []client.SessionConfigOption{
		{
			ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "sonnet",
			Options: []client.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
		},
		{
			ID: "effort", Name: "Reasoning effort", Type: "select", Category: "reasoning", CurrentValue: "high",
			Options: []client.SessionConfigSelectOption{{Value: "high", Name: "High"}, {Value: "max", Name: "Max"}},
		},
	}}, controlagents.SessionOptions{ModelID: "opus", ConfigValues: map[string]string{"effort": "max"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got, want := acpClient.calls, []string{"config:session-1:model:opus", "config:session-1:effort:max"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	model, _ := findConfigOption(state.ConfigOptions, "model")
	effort, _ := findConfigOption(state.ConfigOptions, "effort")
	if currentValue(model.CurrentValue) != "opus" || currentValue(effort.CurrentValue) != "max" {
		t.Fatalf("Apply() state = %#v", state.ConfigOptions)
	}
}

func TestApplyPlacesEffortAfterNonEffortDefaults(t *testing.T) {
	t.Parallel()

	options := func(mode, effort string) []client.SessionConfigOption {
		return []client.SessionConfigOption{
			{ID: "a_effort", Type: "select", CurrentValue: effort, Options: []client.SessionConfigSelectOption{{Value: "high"}, {Value: "max"}}},
			{ID: "z_mode", Type: "select", CurrentValue: mode, Options: []client.SessionConfigSelectOption{{Value: "ask"}, {Value: "code"}}},
		}
	}
	acpClient := &fakeClient{responses: []client.SetSessionConfigOptionResponse{
		{ConfigOptions: options("code", "high")},
		{ConfigOptions: options("code", "max")},
	}}
	_, err := Apply(context.Background(), acpClient, "session-order", State{ConfigOptions: options("ask", "high")}, controlagents.SessionOptions{
		ConfigValues:            map[string]string{"a_effort": "max", "z_mode": "code"},
		ReasoningEffortConfigID: "a_effort",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"config:session-order:z_mode:code", "config:session-order:a_effort:max"}
	if !reflect.DeepEqual(acpClient.calls, want) {
		t.Fatalf("calls = %#v, want non-effort then effort %#v", acpClient.calls, want)
	}
}

func TestApplyUsesSetModelWhenOnlyModelsAreAdvertised(t *testing.T) {
	t.Parallel()

	acpClient := &fakeClient{}
	state, err := Apply(context.Background(), acpClient, "session-2", State{Models: &client.SessionModelState{
		CurrentModelID: "gpt-sol/high",
		AvailableModels: []client.ModelInfo{
			{ModelID: "gpt-sol/high", Name: "Sol High"},
			{ModelID: "gpt-sol/xhigh", Name: "Sol XHigh"},
		},
	}}, controlagents.SessionOptions{ModelID: "gpt-sol/xhigh"})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got, want := acpClient.calls, []string{"model:session-2:gpt-sol/xhigh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if state.Models == nil || state.Models.CurrentModelID != "gpt-sol/xhigh" {
		t.Fatalf("Apply().Models = %#v", state.Models)
	}
}

func TestApplyFailsClosedForUnadvertisedModel(t *testing.T) {
	t.Parallel()

	acpClient := &fakeClient{}
	_, err := Apply(context.Background(), acpClient, "session-3", State{Models: &client.SessionModelState{
		CurrentModelID:  "sonnet",
		AvailableModels: []client.ModelInfo{{ModelID: "sonnet", Name: "Sonnet"}},
	}}, controlagents.SessionOptions{ModelID: "opus"})
	if err == nil {
		t.Fatal("Apply() error = nil, want unavailable model")
	}
	if len(acpClient.calls) != 0 {
		t.Fatalf("calls = %#v, want no fallback mutation", acpClient.calls)
	}
}

func TestApplyReplacesConfigOptionsAfterModelSwitch(t *testing.T) {
	t.Parallel()

	acpClient := &fakeClient{responses: []client.SetSessionConfigOptionResponse{{ConfigOptions: []client.SessionConfigOption{{
		ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "opus",
		Options: []client.SessionConfigSelectOption{{Value: "sonnet"}, {Value: "opus"}},
	}}}}}
	_, err := Apply(context.Background(), acpClient, "session-replace", State{ConfigOptions: []client.SessionConfigOption{
		{
			ID: "model", Type: "select", Category: "model", CurrentValue: "sonnet",
			Options: []client.SessionConfigSelectOption{{Value: "sonnet"}, {Value: "opus"}},
		},
		{
			ID: "effort", Type: "select", CurrentValue: "high",
			Options: []client.SessionConfigSelectOption{{Value: "high"}, {Value: "max"}},
		},
	}}, controlagents.SessionOptions{ModelID: "opus", ConfigValues: map[string]string{"effort": "max"}})
	if err == nil || !strings.Contains(err.Error(), `config option "effort" is not advertised`) {
		t.Fatalf("Apply() error = %v, want removed effort option rejection", err)
	}
	if got, want := acpClient.calls, []string{"config:session-replace:model:opus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestApplyRejectsConfigResponseThatDoesNotConfirmSelection(t *testing.T) {
	t.Parallel()

	acpClient := &fakeClient{responses: []client.SetSessionConfigOptionResponse{{ConfigOptions: []client.SessionConfigOption{{
		ID: "mode", Type: "select", CurrentValue: "ask",
		Options: []client.SessionConfigSelectOption{{Value: "ask"}, {Value: "code"}},
	}}}}}
	_, err := Apply(context.Background(), acpClient, "session-confirm", State{ConfigOptions: []client.SessionConfigOption{{
		ID: "mode", Type: "select", CurrentValue: "ask",
		Options: []client.SessionConfigSelectOption{{Value: "ask"}, {Value: "code"}},
	}}}, controlagents.SessionOptions{ConfigValues: map[string]string{"mode": "code"}})
	if err == nil || !strings.Contains(err.Error(), `reported current value "ask"`) {
		t.Fatalf("Apply() error = %v, want non-confirming response rejection", err)
	}
}

func TestSnapshotPrefersModelConfigOption(t *testing.T) {
	t.Parallel()

	connection := controlagents.Connection{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}
	snapshot := Snapshot(connection, "/repo", 1, State{
		ConfigOptions: []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: "opus",
				Options: []client.SessionConfigSelectOption{{Value: "opus", Name: "Opus"}},
			},
			{
				ID: "api_key", Name: "API key", Type: "text", CurrentValue: "must-not-persist",
			},
		},
		Models: &client.SessionModelState{CurrentModelID: "legacy", AvailableModels: []client.ModelInfo{{ModelID: "legacy", Name: "Legacy"}}},
	})
	if snapshot.ModelControl.Kind != controlagents.ModelControlConfigOption || snapshot.ModelControl.ConfigID != "model" {
		t.Fatalf("ModelControl = %#v", snapshot.ModelControl)
	}
	if got, want := snapshot.Models, []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Models = %#v, want %#v", got, want)
	}
	if got, want := len(snapshot.ConfigOptions), 1; got != want {
		t.Fatalf("len(ConfigOptions) = %d, want %d safe selectable option", got, want)
	}
}

func TestSnapshotPreservesEverySelectConfigOption(t *testing.T) {
	t.Parallel()

	snapshot := Snapshot(controlagents.Connection{ID: "custom", Launcher: controlagents.Launcher{Command: "custom-acp"}}, "/repo", 1, State{
		ConfigOptions: []client.SessionConfigOption{
			{
				ID: "mode", Name: "Mode", Type: "select", CurrentValue: "review",
				Options: []client.SessionConfigSelectOption{{Value: "review"}, {Value: "code"}},
			},
			{
				ID: "tone", Name: "Tone", Type: "select", CurrentValue: "concise",
				Options: []client.SessionConfigSelectOption{{Value: "concise"}, {Value: "detailed"}},
			},
		},
	})
	if _, ok := controlConfigOption(snapshot.ConfigOptions, "mode"); !ok {
		t.Fatalf("Snapshot() omitted mode selector: %#v", snapshot.ConfigOptions)
	}
	if _, ok := controlConfigOption(snapshot.ConfigOptions, "tone"); !ok {
		t.Fatalf("Snapshot() omitted arbitrary select option: %#v", snapshot.ConfigOptions)
	}
}

func TestSnapshotProjectsCodexHandshakeAsModelAndEffortDimensions(t *testing.T) {
	t.Parallel()

	snapshot := Snapshot(controlagents.Connection{ID: "codex", Launcher: controlagents.Launcher{Command: "codex-acp"}}, "/repo", 1, State{
		Models: &client.SessionModelState{
			CurrentModelID: "gpt-5.6-sol[xhigh]",
			AvailableModels: []client.ModelInfo{
				{ModelID: "gpt-5.6-sol[low]", Name: "GPT-5.6-Sol (low)"},
				{ModelID: "gpt-5.6-sol[xhigh]", Name: "GPT-5.6-Sol (xhigh)"},
				{ModelID: "gpt-5.6-terra[xhigh]", Name: "GPT-5.6-Terra (xhigh)"},
			},
		},
		ConfigOptions: []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: "gpt-5.6-sol",
				Options: []client.SessionConfigSelectOption{
					{Value: "gpt-5.6-sol", Name: "GPT-5.6-Sol"},
					{Value: "gpt-5.6-terra", Name: "GPT-5.6-Terra"},
					{Value: "gpt-5.6-luna", Name: "GPT-5.6-Luna"},
				},
			},
			{
				ID: "reasoning_effort", Name: "Reasoning effort", Category: "thought_level", Type: "select", CurrentValue: "xhigh",
				Options: []client.SessionConfigSelectOption{{Value: "low", Name: "low"}, {Value: "xhigh", Name: "xhigh"}, {Value: "max", Name: "max"}, {Value: "ultra", Name: "ultra"}},
			},
		},
	})
	if got, want := len(snapshot.Models), 3; got != want {
		t.Fatalf("len(Models) = %d, want %d base Codex models", got, want)
	}
	if snapshot.Models[1].ID != "gpt-5.6-terra" || snapshot.CurrentModelID != "gpt-5.6-sol" {
		t.Fatalf("Codex model projection = %#v current=%q", snapshot.Models, snapshot.CurrentModelID)
	}
	effort, ok := controlConfigOption(snapshot.ConfigOptions, "reasoning_effort")
	if !ok || len(effort.Options) != 4 || effort.CurrentValue != "xhigh" {
		t.Fatalf("Codex effort projection = %#v", effort)
	}
}

func TestSnapshotProjectsClaudeHandshakeModelAndEffort(t *testing.T) {
	t.Parallel()

	snapshot := Snapshot(controlagents.Connection{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-agent-acp"}}, "/repo", 1, State{
		ConfigOptions: []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: "default",
				Options: []client.SessionConfigSelectOption{{Value: "default", Name: "Default (recommended)"}, {Value: "opus", Name: "Opus"}, {Value: "sonnet", Name: "Sonnet"}, {Value: "haiku", Name: "Haiku"}},
			},
			{
				ID: "effort", Name: "Effort", Category: "thought_level", Type: "select", CurrentValue: "default",
				Options: []client.SessionConfigSelectOption{{Value: "default", Name: "Default"}, {Value: "low", Name: "Low"}, {Value: "max", Name: "Max"}},
			},
		},
	})
	if got, want := len(snapshot.Models), 4; got != want || snapshot.Models[0].ID != "default" {
		t.Fatalf("Claude models = %#v, want default/opus/sonnet/haiku", snapshot.Models)
	}
	effort, ok := controlConfigOption(snapshot.ConfigOptions, "effort")
	if !ok || len(effort.Options) != 3 {
		t.Fatalf("Claude effort projection = %#v", effort)
	}
}

func controlConfigOption(options []controlagents.ConfigOption, id string) (controlagents.ConfigOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return controlagents.ConfigOption{}, false
}
