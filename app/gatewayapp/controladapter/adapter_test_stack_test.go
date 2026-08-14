package controladapter

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
)

func TestMain(m *testing.M) {
	readGitWorkspaceStatusForDisplay = func(context.Context, string) (gitWorkspaceStatus, bool) {
		return gitWorkspaceStatus{}, false
	}
	os.Exit(m.Run())
}

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
	cfg.ResolveProviderHTTPClient = gatewayapptest.StaticProviderHTTPClient(model.HTTPClient)
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil || strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" {
		return stack, err
	}
	if _, err := gatewayapptest.ConnectModel(context.Background(), stack, model); err != nil {
		_ = stack.Close()
		return nil, err
	}
	return stack, nil
}
