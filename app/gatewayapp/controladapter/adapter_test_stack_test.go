package controladapter

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
)

func newAdapterTestStack(t *testing.T, cfg gatewayapp.Config) (*gatewayapp.Stack, error) {
	t.Helper()
	return newAdapterTestStackWithOptions(t, cfg, adapterTestStackOptions{})
}

type adapterTestStackOptions struct {
	BuiltInAgentProfiles bool
}

func newAdapterTestStackWithOptions(t *testing.T, cfg gatewayapp.Config, opts adapterTestStackOptions) (*gatewayapp.Stack, error) {
	t.Helper()
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	if cfg.SkillDirs == nil {
		cfg.SkillDirs = []string{t.TempDir()}
	}
	if !opts.BuiltInAgentProfiles {
		cfg.DisableBuiltInAgentProfiles = true
	}
	return gatewayapp.NewLocalStack(cfg)
}
