//go:build e2e

package eval

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/impl/agent/acp/subagent"
	"github.com/caelis-labs/caelis/ports/delegation"
	"github.com/caelis-labs/caelis/ports/subagent"
)

func TestRunnerSpawnChildSurvivesCallerContextCancelAfterYield(t *testing.T) {
	repo := repoRootForRunnerTest(t)
	root := t.TempDir()
	childBin := filepath.Join(t.TempDir(), "e2eagent")
	build := exec.Command("go", "build", "-o", childBin, "./internal/acpe2eagent")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build e2eagent: %v\n%s", err, string(output))
	}
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:        "self",
		Description: "self child",
		Command:     childBin,
		WorkDir:     repo,
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY":    "child survived caller cancel",
			"SDK_ACP_STUB_DELAY_MS": "150",
			"SDK_ACP_SESSION_ROOT":  filepath.Join(root, "child-sessions"),
		},
	}})
	if err != nil {
		t.Fatalf("acpsubagent.NewRegistry() error = %v", err)
	}
	runner, err := acp.NewRunner(acp.RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("acpsubagent.NewRunner() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	anchor, result, err := runner.Spawn(ctx, subagent.SpawnContext{
		TaskID: "task-cancel",
		CWD:    t.TempDir(),
	}, delegation.Request{
		Agent:  "self",
		Prompt: "Reply exactly: child survived caller cancel",
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if !result.Running {
		t.Fatalf("Spawn() result = %+v, want yielded running task", result)
	}
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer waitCancel()
	result, err = runner.Wait(waitCtx, anchor, 10_000)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.Running || result.State != delegation.StateCompleted {
		t.Fatalf("Wait() result = %+v, want completed child", result)
	}
	if result.Result != "child survived caller cancel" {
		t.Fatalf("Wait() result text = %q, want child reply", result.Result)
	}
}
