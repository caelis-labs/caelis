//go:build e2e

package eval

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestCuratedACPModelCatalogE2E(t *testing.T) {
	tests := []struct {
		name         string
		commandEnv   string
		commandName  string
		models       []string
		configOption string
	}{
		{
			name: "codex", commandEnv: "CAELIS_CODEX_ACP_CATALOG_E2E_BIN", commandName: "codex-acp",
			models: []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}, configOption: "reasoning_effort",
		},
		{
			name: "claude", commandEnv: "CAELIS_CLAUDE_ACP_CATALOG_E2E_BIN", commandName: "claude-agent-acp",
			models: []string{"default", "opus", "sonnet", "haiku"}, configOption: "effort",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := strings.TrimSpace(os.Getenv(test.commandEnv))
			if source == "" {
				t.Skipf("set %s to the curated ACP executable", test.commandEnv)
			}
			binDir := t.TempDir()
			if err := os.Symlink(source, filepath.Join(binDir, test.commandName)); err != nil {
				t.Fatalf("Symlink(%s) error = %v", test.commandName, err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			workspace := t.TempDir()
			stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
				AppName: "caelis", UserID: "acp-catalog-e2e", StoreDir: t.TempDir(),
				WorkspaceKey: workspace, WorkspaceCWD: workspace, ApprovalMode: "auto-review",
				Model: gatewayapp.ModelConfig{Provider: "ollama", Model: "llama3"},
			})
			if err != nil {
				t.Fatalf("NewLocalStack() error = %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			snapshot, err := stack.DiscoverACPConnection(ctx, controlagents.ConnectRequest{
				AdapterID: test.name, Launcher: controlagents.LauncherChoiceGlobal, CWD: workspace,
			})
			if err != nil {
				t.Fatalf("DiscoverACPConnection(%s) error = %v", test.name, err)
			}
			modelIDs := make([]string, 0, len(snapshot.Models))
			for _, model := range snapshot.Models {
				modelIDs = append(modelIDs, model.ID)
			}
			for _, want := range test.models {
				if !slices.Contains(modelIDs, want) {
					t.Fatalf("%s models = %#v, want %q", test.name, modelIDs, want)
				}
			}
			foundOption := false
			for _, option := range snapshot.ConfigOptions {
				if option.ID == test.configOption && len(option.Options) > 1 {
					foundOption = true
					break
				}
			}
			if !foundOption {
				t.Fatalf("%s config options = %#v, want %q catalog", test.name, snapshot.ConfigOptions, test.configOption)
			}
		})
	}
}
