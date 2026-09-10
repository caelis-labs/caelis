package acpsurface

import (
	"context"
	"strings"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
)

func TestNewFromClientsSetConfigOptionModelSelectorCompatibility(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	defaultModel := gatewayapp.ModelConfig{Provider: "deepseek", Model: "deepseek-v4-flash", Token: "test-key"}
	officeModel := gatewayapp.ModelConfig{Provider: "deepseek", EndpointID: "office", BaseURL: "https://deepseek-office.example/v1", Model: "deepseek-v4-flash", Token: "office-test-key"}
	apiCN := gatewayapp.ModelConfig{Provider: "xiaomi", Model: "mimo-v2.5-pro", BaseURL: "https://api.xiaomimimo.com/v1", Token: "test-key"}
	tokenPlan := gatewayapp.ModelConfig{Provider: "xiaomi", Model: "mimo-v2.5-pro", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", Token: "test-key"}

	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model:        defaultModel,
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	// Default and nondefault DeepSeek routes share an alias; Xiaomi has two
	// nondefault routes. Neither should prevent ACP Session creation.
	for _, cfg := range []gatewayapp.ModelConfig{officeModel, apiCN, tokenPlan} {
		if _, err := gatewayapptest.ConnectModel(ctx, stack, cfg); err != nil {
			t.Fatalf("ConnectModel(%s) error = %v", cfg.BaseURL, err)
		}
	}

	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatalf("newTestAgentFromStack() error = %v", err)
	}
	created, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: workspace})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	// Published model choices: short selector for the default endpoint,
	// endpoint-qualified selector for nondefault endpoints. The bare ambiguous
	// alias is never published.
	published := configOptionSelectValues(created.ConfigOptions, "model")
	const defaultSelector = "deepseek/deepseek-v4-flash"
	for _, want := range []string{
		defaultSelector,
		"deepseek@office/deepseek-v4-flash",
		"xiaomi@api-cn/mimo-v2.5-pro",
		"xiaomi@token-plan-cn/mimo-v2.5-pro",
	} {
		if !containsString(published, want) {
			t.Fatalf("published model options = %#v, want %q", published, want)
		}
	}
	if containsString(published, "xiaomi/mimo-v2.5-pro") {
		t.Fatalf("published model options = %#v, must not collapse the ambiguous visible alias", published)
	}

	sessionRoute := func() (modelID string, modelAlias string) {
		t.Helper()
		state, err := stack.ControlStatus().SessionRuntimeState(ctx, session.SessionRef{
			AppName:      stack.AppName(),
			UserID:       stack.UserID(),
			SessionID:    string(created.SessionId),
			WorkspaceKey: workspace,
		})
		if err != nil {
			t.Fatalf("SessionRuntimeState() error = %v", err)
		}
		return state.ModelID, state.ModelAlias
	}

	cases := []struct {
		name     string
		selector string
		wantID   string
		alias    string
	}{
		{"nondefault public selector", "xiaomi@api-cn/mimo-v2.5-pro", "xiaomi@api-cn/xiaomi/mimo-v2.5-pro", "xiaomi/mimo-v2.5-pro"},
		{"default short selector", defaultSelector, "deepseek@default/deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash"},
		{"additional deepseek endpoint", "deepseek@office/deepseek-v4-flash", "deepseek@office/deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash"},
		{"nondefault endpoint selector", "xiaomi@token-plan-cn/mimo-v2.5-pro", "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro", "xiaomi/mimo-v2.5-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/public", func(t *testing.T) {
			resp, err := agent.SetSessionConfigOption(ctx, setSessionConfigValueRequest(created.SessionId, "model", tc.selector))
			if err != nil {
				t.Fatalf("SetSessionConfigOption(model=%q) error = %v", tc.selector, err)
			}
			if got, ok := configOptionString(resp.ConfigOptions, "model"); !ok || got != tc.selector {
				t.Fatalf("currentValue = %q, %v; want %q", got, ok, tc.selector)
			}
			modelID, alias := sessionRoute()
			if modelID != tc.wantID || alias != tc.alias {
				t.Fatalf("persisted route = (%q, %q), want (%q, %q)", modelID, alias, tc.wantID, tc.alias)
			}
		})
		t.Run(tc.name+"/legacy-durable-id", func(t *testing.T) {
			// The durable Control config ID must keep selecting the same route
			// even though the Surface publishes the shorter selector.
			resp, err := agent.SetSessionConfigOption(ctx, setSessionConfigValueRequest(created.SessionId, "model", tc.wantID))
			if err != nil {
				t.Fatalf("SetSessionConfigOption(legacy %q) error = %v", tc.wantID, err)
			}
			if got, ok := configOptionString(resp.ConfigOptions, "model"); !ok || got != tc.selector {
				t.Fatalf("currentValue after legacy id = %q, %v; want %q", got, ok, tc.selector)
			}
			modelID, alias := sessionRoute()
			if modelID != tc.wantID || alias != tc.alias {
				t.Fatalf("persisted route = (%q, %q), want (%q, %q)", modelID, alias, tc.wantID, tc.alias)
			}
		})
	}

	beforeID, _ := sessionRoute()
	if _, err := agent.SetSessionConfigOption(ctx, setSessionConfigValueRequest(created.SessionId, "model", "xiaomi/mimo-v2.5-pro")); err == nil {
		t.Fatal("SetSessionConfigOption(ambiguous visible alias) succeeded, want rejection")
	}
	afterID, _ := sessionRoute()
	if afterID != beforeID {
		t.Fatalf("rejected selection mutated the persisted route: %q -> %q", beforeID, afterID)
	}
}

func configOptionSelectValues(options []acpsdk.SessionConfigOption, id string) []string {
	for _, option := range options {
		if option.Select == nil || strings.TrimSpace(string(option.Select.Id)) != id {
			continue
		}
		var values []string
		if option.Select.Options.Ungrouped != nil {
			for _, choice := range *option.Select.Options.Ungrouped {
				if value := strings.TrimSpace(string(choice.Value)); value != "" {
					values = append(values, value)
				}
			}
		}
		return values
	}
	return nil
}
