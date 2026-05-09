//go:build e2e

package acpbridge_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp"
)

func TestACPXExecE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	output := runACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec 'Reply with exactly: acpx adapter ok'`)
	if !strings.Contains(output, "acpx adapter ok") {
		t.Fatalf("acpx output = %q, want assistant reply", output)
	}
}

func TestACPXSessionsPromptE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	runACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" sessions new`)
	output := runACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" 'Reply with exactly: acpx session ok'`)
	if !strings.Contains(output, "acpx session ok") {
		t.Fatalf("acpx output = %q, want assistant reply", output)
	}
}

func TestACPXLoadSessionE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	runACPXCommand(t, repo, dir,
		`export SDK_ACP_STUB_REPLY="load replay ok"`+"\n"+
			`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --ttl 1 sessions new`)
	time.Sleep(2 * time.Second)
	output := runACPXCommand(t, repo, dir,
		`export SDK_ACP_STUB_REPLY="load replay ok"`+"\n"+
			`acpx --verbose --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --ttl 1 --timeout 180 'Reply with exactly: load replay ok'`)
	if !strings.Contains(output, "load replay ok") {
		t.Fatalf("acpx output = %q, want assistant reply", output)
	}
	if !strings.Contains(output, acp.MethodSessionLoad) {
		t.Fatalf("acpx verbose output = %q, want session/load call", output)
	}
}

func TestACPXToolAndPlanE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "facts.txt")
	if err := os.WriteFile(target, []byte("acpx tool loop ok\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	toolOutput := runACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec "Use the READ tool on `+target+` and reply with exactly the file content and nothing else."`)
	if !strings.Contains(toolOutput, "acpx tool loop ok") || !strings.Contains(toolOutput, "[tool] READ") {
		t.Fatalf("tool output = %q, want tool execution and final answer", toolOutput)
	}
	planOutput := runACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec 'Create a short 2-step plan, call the PLAN tool, then reply with exactly: acpx plan ok'`)
	if !strings.Contains(planOutput, "acpx plan ok") || !strings.Contains(planOutput, "[plan]") {
		t.Fatalf("plan output = %q, want plan update and final answer", planOutput)
	}
	if strings.Contains(planOutput, "Invalid params") {
		t.Fatalf("plan output = %q, want no ACP schema validation errors", planOutput)
	}
}

func TestACPXApprovalE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "approved.txt")
	output := runACPXCommand(t, repo, dir,
		`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --approve-all --timeout 180 exec "Use the BASH tool with sandbox_permissions=require_escalated and a short justification to create `+target+` containing exactly approved by acpx approval e2e, then reply with exactly: acpx approval ok"`)
	if !strings.Contains(output, "acpx approval ok") {
		t.Fatalf("acpx output = %q, want assistant reply", output)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if got := strings.TrimSpace(string(data)); !strings.Contains(got, "approved by acpx approval e2e") {
		t.Fatalf("written content = %q, want approved file content", got)
	}
}

func TestACPXAsyncBashE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	output := runACPXCommand(t, repo, dir,
		`export SDK_ACP_SCRIPTED_MODE="async_bash"`+"\n"+
			`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --approve-all --timeout 180 exec 'Run the scripted async bash flow'`)
	if !strings.Contains(output, "acpx async bash ok") {
		t.Fatalf("acpx output = %q, want final async bash answer", output)
	}
	if !strings.Contains(output, "[tool] BASH") || !strings.Contains(output, "[tool] TASK") {
		t.Fatalf("acpx output = %q, want BASH and TASK activity", output)
	}
}

func TestACPXSpawnSubagentE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	output := runACPXCommand(t, repo, dir,
		`export SDK_ACP_SCRIPTED_MODE="spawn"`+"\n"+
			`export SDK_ACP_ENABLE_SPAWN=1`+"\n"+
			`export SDK_ACP_SELF_AGENT_DESC="Spawn a sibling ACP child session"`+"\n"+
			`export SDK_ACP_SELF_AGENT_CMD="cd `+repo+` && SDK_ACP_STUB_REPLY='spawn child ok' SDK_ACP_STUB_DELAY_MS=60 SDK_ACP_SESSION_ROOT='$WORKDIR/child-sessions' SDK_ACP_TASK_ROOT='$WORKDIR/child-tasks' go run ./acpbridge/cmd/e2eagent"`+"\n"+
			`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec 'Run the scripted spawn flow'`)
	if !strings.Contains(output, "spawn child ok") {
		t.Fatalf("acpx output = %q, want final spawned answer", output)
	}
	if !strings.Contains(output, "[tool] SPAWN") || !strings.Contains(output, "[tool] TASK") {
		t.Fatalf("acpx output = %q, want SPAWN and TASK activity", output)
	}
}

func TestACPXSpawnSelfDisablesNestedSpawnE2E(t *testing.T) {
	requireACPXE2EPrereqs(t)
	repo := repoRoot(t)
	dir := t.TempDir()
	output := runACPXCommand(t, repo, dir,
		`export SDK_ACP_SCRIPTED_MODE="spawn_passthrough"`+"\n"+
			`export SDK_ACP_ENABLE_SPAWN=1`+"\n"+
			`export SDK_ACP_SELF_AGENT_DESC="Spawn a sibling ACP child session"`+"\n"+
			`export SDK_ACP_SELF_AGENT_CMD="cd `+repo+` && SDK_ACP_ENABLE_SPAWN=1 SDK_ACP_SCRIPTED_MODE='probe_spawn' SDK_ACP_SESSION_ROOT='$WORKDIR/child-sessions' SDK_ACP_TASK_ROOT='$WORKDIR/child-tasks' go run ./acpbridge/cmd/e2eagent"`+"\n"+
			`acpx --agent "$ACP_AGENT_CMD" --cwd "$WORKDIR" --timeout 180 exec 'Run the scripted spawn flow'`)
	if !strings.Contains(output, "spawn disabled") {
		t.Fatalf("acpx output = %q, want child to report spawn disabled", output)
	}
}

func requireACPXE2EPrereqs(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("SDK_RUN_ACPX_E2E")) != "1" {
		t.Skip("set SDK_RUN_ACPX_E2E=1 to run acpx integration tests")
	}
	if _, err := exec.LookPath("acpx"); err != nil {
		t.Skip("acpx is not installed")
	}
}

func repoRoot(t *testing.T) string {
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

func runACPXCommand(t *testing.T, repo string, workdir string, body string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	script := strings.Join([]string{
		`if [ -f "` + filepath.Join(repo, ".env") + `" ]; then set -a; source "` + filepath.Join(repo, ".env") + `"; set +a; fi`,
		`export SDK_E2E_PROVIDER=codefree`,
		`export CODEFREE_MODEL="${CODEFREE_MODEL:-GLM-5.1}"`,
		`export WORKDIR="` + workdir + `"`,
		`export ACP_AGENT_CMD="bash -lc 'cd ` + repo + ` && go run ./acpbridge/cmd/e2eagent'"`,
		body,
	}, "\n")
	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("acpx command failed: %v\n%s", err, string(output))
	}
	return string(output)
}
