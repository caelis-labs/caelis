package gatewayagent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestGatewayAgentExposesZedSessionSurface(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acp-zed-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider:        "openai",
			Model:           "gpt-4o",
			ReasoningEffort: "medium",
			ReasoningLevels: []string{"low", "medium", "high"},
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := New(stack)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	initResp, err := agent.Initialize(ctx, acp.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if initResp.AgentInfo == nil || initResp.AgentInfo.Version == "" || initResp.AgentInfo.Version == "dev" {
		t.Fatalf("AgentInfo = %#v, want actual version metadata", initResp.AgentInfo)
	}
	if !initResp.AgentCapabilities.PromptCapabilities.Image {
		t.Fatalf("PromptCapabilities.Image = false, want current model image support declared")
	}

	resp, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: workdir})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if resp.Modes == nil || resp.Modes.CurrentModeID != "auto-review" {
		t.Fatalf("Modes = %#v, want auto-review Caelis session modes", resp.Modes)
	}
	if got, want := modeIDs(resp.Modes.AvailableModes), []string{"auto-review", "manual"}; !slices.Equal(got, want) {
		t.Fatalf("mode ids = %#v, want %#v", got, want)
	}
	options := configOptionsByID(resp.ConfigOptions)
	modeOption, ok := options["mode"]
	if !ok {
		t.Fatalf("configOptions missing mode: %#v", resp.ConfigOptions)
	}
	if modeOption.Category != "mode" || modeOption.CurrentValue != "auto-review" {
		t.Fatalf("mode option = %#v, want category mode and current auto-review", modeOption)
	}
	if got, want := configOptionValues(modeOption.Options), []string{"auto-review", "manual"}; !slices.Equal(got, want) {
		t.Fatalf("mode values = %#v, want %#v", got, want)
	}
	modelOption, ok := options["model"]
	if !ok {
		t.Fatalf("configOptions missing model: %#v", resp.ConfigOptions)
	}
	if modelOption.Category != "model" || modelOption.CurrentValue != "openai/gpt-4o" {
		t.Fatalf("model option = %#v, want category model and current alias", modelOption)
	}
	reasoningOption, ok := options["reasoning_effort"]
	if !ok {
		t.Fatalf("configOptions missing reasoning_effort: %#v", resp.ConfigOptions)
	}
	if reasoningOption.Category != "thought_level" || reasoningOption.CurrentValue != "medium" {
		t.Fatalf("reasoning option = %#v, want thought_level medium", reasoningOption)
	}
	if got, want := configOptionValues(reasoningOption.Options), []string{"low", "medium", "high"}; !slices.Equal(got, want) {
		t.Fatalf("reasoning values = %#v, want %#v", got, want)
	}
	setModeResp, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: resp.SessionID,
		ConfigID:  "mode",
		Value:     "manual",
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption(mode) error = %v", err)
	}
	modeOption = configOptionsByID(setModeResp.ConfigOptions)["mode"]
	if modeOption.CurrentValue != "manual" {
		t.Fatalf("mode option after set = %#v, want current manual", modeOption)
	}
	state, err := stack.SessionRuntimeState(ctx, sdksession.SessionRef{
		AppName:   "caelis",
		UserID:    "acp-zed-test",
		SessionID: resp.SessionID,
	})
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("session mode = %q, want manual", state.SessionMode)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal(NewSessionResponse) error = %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("Unmarshal(NewSessionResponse) error = %v", err)
	}
	models, ok := envelope["models"].(map[string]any)
	if !ok {
		t.Fatalf("models = %#v, want ACP session model state", envelope["models"])
	}
	if got := models["currentModelId"]; got != "openai/gpt-4o" {
		t.Fatalf("models.currentModelId = %#v, want openai/gpt-4o", got)
	}
	available, _ := models["availableModels"].([]any)
	if len(available) == 0 {
		t.Fatalf("models.availableModels = %#v, want configured model aliases", models["availableModels"])
	}
}

func modeIDs(modes []acp.SessionMode) []string {
	out := make([]string, 0, len(modes))
	for _, mode := range modes {
		out = append(out, mode.ID)
	}
	return out
}

func configOptionsByID(options []acp.SessionConfigOption) map[string]acp.SessionConfigOption {
	out := make(map[string]acp.SessionConfigOption, len(options))
	for _, option := range options {
		out[option.ID] = option
	}
	return out
}

func configOptionValues(options []acp.SessionConfigSelectOption) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		out = append(out, option.Value)
	}
	return out
}
