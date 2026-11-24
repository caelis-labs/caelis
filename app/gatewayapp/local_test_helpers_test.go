package gatewayapp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/plugin"
)

func newGatewayAppTestStack(t *testing.T, cfg Config) (*Stack, error) {
	t.Helper()
	return newGatewayAppTestStackWithDefaults(t, cfg, plugin.WithAvailableProductContributions)
}

func newIsolatedGatewayAppTestStack(t *testing.T, cfg Config) (*Stack, error) {
	t.Helper()
	return newGatewayAppTestStackWithDefaults(t, cfg, func(optional plugin.Contributions, _ plugin.RuntimePaths, _ ...plugin.Config) plugin.Contributions {
		return optional
	})
}

func newGatewayAppTestStackWithDefaults(t *testing.T, cfg Config, defaults productPluginDefaults) (*Stack, error) {
	t.Helper()
	// Tests using a full production Stack intentionally remain serial: each
	// Stack opens SQLite and durable stores whose fsync barriers contend under
	// top-level test parallelism.
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	if cfg.SkillDirs == nil {
		cfg.SkillDirs = []string{t.TempDir()}
	}
	model := cfg.Model
	cfg.Model = ModelConfig{}
	if model.HTTPClient != nil {
		cfg.ResolveProviderHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
			return model.HTTPClient, nil
		}
	}
	stack, err := newLocalStack(cfg, defaults)
	if err == nil {
		// Release background workers and stores before this test's directories.
		t.Cleanup(func() { _ = stack.Close() })
	}
	if err != nil || !modelConfigSupplied(model) {
		return stack, err
	}
	if _, err := stack.connectTestModel(model); err != nil {
		_ = stack.Close()
		return nil, err
	}
	return stack, nil
}
