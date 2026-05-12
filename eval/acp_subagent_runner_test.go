//go:build e2e

package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/subagent"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
)

func TestRunnerCodexACPWebSearchLiveE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAELIS_CODEX_ACP_E2E")) != "1" {
		t.Skip("set CAELIS_CODEX_ACP_E2E=1 to run local Codex ACP live E2E")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx is not installed")
	}

	repo := repoRootForRunnerTest(t)
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:        "codex",
		Description: "local Codex ACP",
		Command:     "npx",
		Args:        []string{"--yes", "@zed-industries/codex-acp@^0.12.0"},
		WorkDir:     repo,
		Env: map[string]string{
			"npm_config_cache": "/tmp/caelis-npm-cache",
		},
	}})
	if err != nil {
		t.Fatalf("acpsubagent.NewRegistry() error = %v", err)
	}
	sink := &recordingStreams{}
	runner, err := acp.NewRunner(acp.RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("acpsubagent.NewRunner() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	anchor, first, err := runner.Spawn(ctx, subagent.SpawnContext{
		TaskID:  "live-codex-weather",
		CWD:     repo,
		Streams: sink,
	}, delegation.Request{
		Agent:  "codex",
		Prompt: "查询一下上海今天的天气",
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if !first.Running {
		t.Fatalf("Spawn() result = %+v, want yielded running task", first)
	}
	result, err := runner.Wait(ctx, anchor, 180_000)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.State != delegation.StateCompleted {
		t.Fatalf("Wait() result = %+v, want completed", result)
	}

	var sawFetchTool bool
	var sawAssistant bool
	for i, frame := range sink.frames {
		if frame.Event != nil && frame.Event.Protocol != nil && frame.Event.Protocol.ToolCall != nil {
			call := frame.Event.Protocol.ToolCall
			t.Logf("frame[%d] tool text=%q name=%q kind=%q title=%q status=%q", i, frame.Text, call.Name, call.Kind, call.Title, call.Status)
			if strings.EqualFold(strings.TrimSpace(call.Kind), "fetch") {
				sawFetchTool = true
			}
			if frame.Text != "" {
				t.Fatalf("frame[%d] structured tool text = %q, want empty text fallback", i, frame.Text)
			}
			continue
		}
		if frame.Text != "" {
			t.Logf("frame[%d] text stream=%q text=%q", i, frame.Stream, frame.Text)
			if strings.Contains(frame.Text, "Searching the Web") || strings.Contains(frame.Text, "Searching for:") {
				t.Fatalf("frame[%d] rendered ACP tool activity as text: %q", i, frame.Text)
			}
			if strings.Contains(frame.Text, "上海") {
				sawAssistant = true
			}
		}
	}
	if !sawFetchTool {
		t.Fatalf("live frames did not include a structured fetch tool event; frames=%#v", sink.frames)
	}
	if !sawAssistant || !strings.Contains(result.Result, "上海") {
		t.Fatalf("live result = %+v, sawAssistant=%v", result, sawAssistant)
	}
}

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
			"SDK_ACP_TASK_ROOT":     filepath.Join(root, "child-tasks"),
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
