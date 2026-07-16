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
	return gatewayapp.NewLocalStack(cfg)
}
