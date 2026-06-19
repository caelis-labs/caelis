package controladapter

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/internal/winproc"
)

const gitWorkspaceStatusTimeout = 500 * time.Millisecond

type gitWorkspaceStatus struct {
	Branch string
	Dirty  bool
}

var readGitWorkspaceStatusForDisplay = readGitWorkspaceStatus

func workspaceStatusDisplay(ctx context.Context, cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	status, ok := readGitWorkspaceStatusForDisplay(ctx, cwd)
	if !ok {
		return compactHomePath(cwd, userHomeDir())
	}
	return formatWorkspaceStatusDisplay(cwd, status)
}

func formatWorkspaceStatusDisplay(cwd string, status gitWorkspaceStatus) string {
	return formatWorkspaceStatusDisplayWithHome(cwd, status, userHomeDir())
}

func formatWorkspaceStatusDisplayWithHome(cwd string, status gitWorkspaceStatus, home string) string {
	cwd = strings.TrimSpace(cwd)
	displayPath := compactHomePath(cwd, home)
	branch := strings.TrimSpace(status.Branch)
	if cwd == "" || branch == "" {
		return displayPath
	}
	if strings.Contains(displayPath, " [⎇ ") && strings.HasSuffix(displayPath, "]") {
		return displayPath
	}
	out := displayPath + " [⎇ " + branch
	if status.Dirty {
		out += "*"
	}
	return out + "]"
}

func compactHomePath(path string, home string) string {
	path = strings.TrimSpace(path)
	home = strings.TrimRight(strings.TrimSpace(home), `/\`)
	if path == "" || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	for _, sep := range []string{"/", "\\"} {
		prefix := home + sep
		if strings.HasPrefix(path, prefix) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
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
	winproc.ConfigureHiddenConsole(cmd)
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
