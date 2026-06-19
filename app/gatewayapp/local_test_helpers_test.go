package gatewayapp

import (
	"strings"
	"testing"
)

func newGatewayAppTestStack(t *testing.T, cfg Config) (*Stack, error) {
	t.Helper()
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	if cfg.SkillDirs == nil {
		cfg.SkillDirs = []string{t.TempDir()}
	}
	cfg.DisableBuiltInAgentProfiles = true
	return NewLocalStack(cfg)
}
