// Package gatewayapptest provides product-path setup helpers for tests outside
// app/gatewayapp. It intentionally writes through the focused Configuration
// command capability so tests cannot depend on a second Stack mutation API.
package gatewayapptest

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// StaticProviderHTTPClient adapts a fixture transport into Stack construction
// without putting HTTP clients on the Configuration wire contract.
func StaticProviderHTTPClient(client *http.Client) func(context.Context, gatewayapp.ModelConfig) (*http.Client, error) {
	if client == nil {
		return nil
	}
	return func(context.Context, gatewayapp.ModelConfig) (*http.Client, error) {
		return client, nil
	}
}

// BindAgentBinding configures one Host Agent handle through the authoritative
// shared command path and returns the observed binding projection.
func BindAgentBinding(ctx context.Context, stack *gatewayapp.Stack, binding agentbinding.Binding) (agentbinding.Status, error) {
	if stack == nil {
		return agentbinding.Status{}, fmt.Errorf("gatewayapptest: stack is unavailable")
	}
	revision, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	result, err := stack.AgentCommands().BindAgentBinding(ctx, appserver.Principal{ID: stack.UserID()}, appserver.BindAgentBindingRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "gatewayapptest-agent-binding-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		Binding: binding,
	})
	if err != nil {
		return agentbinding.Status{}, err
	}
	if result.Outcome != appserver.OutcomeCommitted {
		return agentbinding.Status{}, fmt.Errorf("gatewayapptest: Agent binding outcome is %q", result.Outcome)
	}
	return stack.AgentBindings().AgentBindingStatus(ctx)
}

// CreateAgentRole configures one custom Host Agent role through the
// authoritative shared command path and returns the observed projection.
func CreateAgentRole(
	ctx context.Context,
	stack *gatewayapp.Stack,
	role agentbinding.Role,
	binding agentbinding.Binding,
) (agentbinding.Status, error) {
	if stack == nil {
		return agentbinding.Status{}, fmt.Errorf("gatewayapptest: stack is unavailable")
	}
	revision, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	result, err := stack.AgentCommands().CreateAgentRole(ctx, appserver.Principal{ID: stack.UserID()}, appserver.CreateAgentRoleRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "gatewayapptest-agent-role-create-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		Role: role, Binding: binding,
	})
	if err != nil {
		return agentbinding.Status{}, err
	}
	if result.Outcome != appserver.OutcomeCommitted {
		return agentbinding.Status{}, fmt.Errorf("gatewayapptest: Agent role creation outcome is %q", result.Outcome)
	}
	return stack.AgentBindings().AgentBindingStatus(ctx)
}

// ConnectModel configures one Host model through the authoritative command
// path and returns its deterministic provider profile identity.
func ConnectModel(ctx context.Context, stack *gatewayapp.Stack, cfg gatewayapp.ModelConfig) (string, error) {
	if stack == nil {
		return "", fmt.Errorf("gatewayapptest: stack is unavailable")
	}
	revision, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		return "", err
	}
	result, err := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: stack.UserID()}, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "gatewayapptest-model-connect-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		Config: connectConfig(cfg),
	})
	if err != nil {
		return "", err
	}
	if result.Outcome != appserver.OutcomeCommitted {
		return "", fmt.Errorf("gatewayapptest: model connection outcome is %q", result.Outcome)
	}
	normalized := modelconfig.NormalizeConfig(cfg)
	return modelprofile.BuildProviderID(normalized.ID), nil
}

// UseHostModel selects the Host default model through the authoritative
// Configuration command path.
func UseHostModel(ctx context.Context, stack *gatewayapp.Stack, alias string) error {
	if stack == nil {
		return fmt.Errorf("gatewayapptest: stack is unavailable")
	}
	revision, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		return err
	}
	result, err := stack.ConfigurationCommands().UseModel(ctx, appserver.Principal{ID: stack.UserID()}, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "gatewayapptest-model-use-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		Model: alias,
	})
	if err != nil {
		return err
	}
	if result.Outcome != appserver.OutcomeCommitted {
		return fmt.Errorf("gatewayapptest: model selection outcome is %q", result.Outcome)
	}
	return nil
}

func connectConfig(cfg gatewayapp.ModelConfig) appserver.ConnectConfig {
	return appserver.ConnectConfig{
		Provider:                       cfg.Provider,
		EndpointID:                     cfg.EndpointID,
		Model:                          cfg.Model,
		BaseURL:                        cfg.BaseURL,
		TimeoutSeconds:                 durationSeconds(cfg.Timeout),
		StreamFirstEventTimeoutSeconds: durationSeconds(cfg.StreamFirstEventTimeout),
		APIKey:                         cfg.Token,
		AuthType:                       string(cfg.AuthType),
		ContextWindowTokens:            cfg.ContextWindowTokens,
		MaxOutputTokens:                cfg.MaxOutputTok,
		ReasoningEffort:                cfg.ReasoningEffort,
		ReasoningLevels:                append([]string(nil), cfg.ReasoningLevels...),
		ImageInput:                     cfg.ImageInput,
	}
}

func durationSeconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	seconds := int(value / time.Second)
	if seconds == 0 {
		return 1
	}
	return seconds
}
