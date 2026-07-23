package gatewayapp

import (
	"strings"
	"testing"
)

func newGatewayAppTestStack(t *testing.T, cfg Config) (*Stack, error) {
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
	return NewLocalStack(cfg)
}
