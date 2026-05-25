package gatewaydriver

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const gitWorkspaceStatusTimeout = 500 * time.Millisecond

type gitWorkspaceStatus struct {
	Branch string
	Dirty  bool
}

func workspaceStatusDisplay(ctx context.Context, cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	status, ok := readGitWorkspaceStatus(ctx, cwd)
	if !ok {
		return cwd
	}
	return formatWorkspaceStatusDisplay(cwd, status)
}

func formatWorkspaceStatusDisplay(cwd string, status gitWorkspaceStatus) string {
	cwd = strings.TrimSpace(cwd)
	branch := strings.TrimSpace(status.Branch)
	if cwd == "" || branch == "" {
		return cwd
	}
	if strings.Contains(cwd, " [⎇ ") && strings.HasSuffix(cwd, "]") {
		return cwd
	}
	out := cwd + " [⎇ " + branch
	if status.Dirty {
		out += "*"
	}
	return out + "]"
}

func readGitWorkspaceStatus(ctx context.Context, cwd string) (gitWorkspaceStatus, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return gitWorkspaceStatus{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	statusCtx, cancel := context.WithTimeout(ctx, gitWorkspaceStatusTimeout)
	defer cancel()
	cmd := exec.CommandContext(statusCtx, "git", "-C", cwd, "status", "--short", "--branch", "--porcelain=v1", "--untracked-files=no")
	output, err := cmd.Output()
	if err != nil {
		return gitWorkspaceStatus{}, false
	}
	return parseGitWorkspaceStatusOutput(string(output))
}

func parseGitWorkspaceStatusOutput(output string) (gitWorkspaceStatus, bool) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(output, "\r\n", "\n"), "\r", "\n"), "\n")
	if len(lines) == 0 {
		return gitWorkspaceStatus{}, false
	}
	branch, ok := parseGitWorkspaceStatusBranch(lines[0])
	if !ok {
		return gitWorkspaceStatus{}, false
	}
	dirty := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) != "" {
			dirty = true
			break
		}
	}
	return gitWorkspaceStatus{Branch: branch, Dirty: dirty}, true
}

func parseGitWorkspaceStatusBranch(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "##") {
		return "", false
	}
	branch := strings.TrimSpace(strings.TrimPrefix(line, "##"))
	branch = strings.TrimPrefix(branch, "No commits yet on ")
	if strings.HasPrefix(branch, "HEAD ") {
		return "HEAD", true
	}
	if before, _, ok := strings.Cut(branch, "..."); ok {
		branch = before
	}
	if before, _, ok := strings.Cut(branch, " ["); ok {
		branch = before
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", false
	}
	return branch, true
}
