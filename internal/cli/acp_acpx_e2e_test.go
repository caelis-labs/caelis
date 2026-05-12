//go:build e2e

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

func TestCLIACPXExecE2E(t *testing.T) {
	requireCLIACPXE2EPrereqs(t)
	repo := repoRootForCLIACPX(t)
	dir := t.TempDir()
	output := runCLIACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec 'Reply with exactly: caelis acp ok'`)
	if !strings.Contains(output, "caelis acp ok") {
		t.Fatalf("acpx output = %q, want assistant reply", output)
	}
}

func TestCLIACPXLoadSessionE2E(t *testing.T) {
	requireCLIACPXE2EPrereqs(t)
	repo := repoRootForCLIACPX(t)
	dir := t.TempDir()
	runCLIACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --ttl 1 sessions new`)
	time.Sleep(2 * time.Second)
	output := runCLIACPXCommand(t, repo, dir,
		`acpx --verbose --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --ttl 1 --timeout 180 'Reply with exactly: caelis load ok'`)
	if !strings.Contains(output, "caelis load ok") {
		t.Fatalf("acpx output = %q, want assistant reply", output)
	}
	if !strings.Contains(output, acp.MethodSessionLoad) {
		t.Fatalf("acpx verbose output = %q, want session/load call", output)
	}
}

func TestCLIACPXSpawnE2E(t *testing.T) {
	requireCLIACPXE2EPrereqs(t)
	repo := repoRootForCLIACPX(t)
	dir := t.TempDir()
	output := runCLIACPXCommand(t, repo, dir,
		`export CAELIS_ACP_SELF_AGENT_DESC="Spawn a bounded ACP child session"`+"\n"+
			`export CAELIS_ACP_SELF_AGENT_CMD="cd `+repo+` && SDK_ACP_STUB_REPLY='cli spawn child ok' SDK_ACP_STUB_DELAY_MS=60 SDK_ACP_SESSION_ROOT='$WORKDIR/child-sessions' SDK_ACP_TASK_ROOT='$WORKDIR/child-tasks' go run ./internal/acpe2eagent"`+"\n"+
			`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec 'Use SPAWN to ask the self child agent to reply with exactly: cli spawn child ok. Then TASK wait for the result and reply with exactly the child result.'`)
	if !strings.Contains(output, "cli spawn child ok") {
		t.Fatalf("acpx output = %q, want spawned child result", output)
	}
	if !strings.Contains(output, "[tool] SPAWN") || !strings.Contains(output, "[tool] TASK") {
		t.Fatalf("acpx output = %q, want SPAWN and TASK activity", output)
	}
}

func requireCLIACPXE2EPrereqs(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("SDK_RUN_ACPX_E2E")) != "1" {
		t.Skip("set SDK_RUN_ACPX_E2E=1 to run acpx integration tests")
	}
	if _, err := exec.LookPath("acpx"); err != nil {
		t.Skip("acpx is not installed")
	}
}

func repoRootForCLIACPX(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}

func runCLIACPXCommand(t *testing.T, repo string, workdir string, body string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	script := strings.Join([]string{
		`if [ -f "` + filepath.Join(repo, ".env") + `" ]; then set -a; source "` + filepath.Join(repo, ".env") + `"; set +a; fi`,
		`export WORKDIR="` + workdir + `"`,
		`export CODEFREE_E2E_MODEL="${CODEFREE_MODEL:-GLM-5.1}"`,
		`export ACP_AGENT_CMD="bash -lc 'cd ` + repo + ` && go run ./cmd/caelis acp -store-dir \"$WORKDIR/caelis\" -workspace-key acpx-cli -workspace-cwd \"$WORKDIR\" -provider codefree -model \"$CODEFREE_E2E_MODEL\"'"`,
		body,
	}, "\n")
	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("acpx command failed: %v\n%s", err, string(output))
	}
	return string(output)
}
