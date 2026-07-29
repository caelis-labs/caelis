package controladapter

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func newAdapterTestStack(t *testing.T, cfg gatewayapp.Config) (*gatewayapp.Stack, error) {
	t.Helper()
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	if cfg.SkillDirs == nil {
		cfg.SkillDirs = []string{t.TempDir()}
	}
	model := cfg.Model
	cfg.Model = gatewayapp.ModelConfig{}
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil || strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" {
		return stack, err
	}
	if _, err := stack.Connect(model); err != nil {
		_ = stack.Close()
		return nil, err
	}
	return stack, nil
}
