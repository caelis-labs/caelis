package presets

import (
	"strings"
)

type gitCommand struct {
	Subcommand string
	Args       []string
}

func gitCommandApprovalReason(command string) string {
	for _, git := range gitCommands(command) {
		if reason := classifyGitCommand(git); reason != "" {
			return reason
		}
	}
	for _, payload := range shellCommandPayloads(command) {
		if reason := gitCommandApprovalReason(payload); reason != "" {
			return reason
		}
	}
	return ""
}

func classifyGitCommand(git gitCommand) string {
	if gitHasFlag(git.Args, gitLongFlag("--help"), gitExactFlag("-h")) {
		return ""
	}
	switch git.Subcommand {
	case "clean":
		if !gitCleanDryRun(git.Args) {
			return "git clean without dry-run can irreversibly remove untracked files"
		}
	case "reset":
		if gitHasFlag(git.Args, gitLongFlag("--hard")) {
			return "git reset --hard discards index and worktree changes"
		}
	case "checkout":
		if gitCheckoutDiscardsWorktree(git.Args) {
			return "git checkout path restore discards worktree changes"
		}
	case "restore":
		if gitRestoreDiscardsWorktree(git.Args) {
			return "git restore discards worktree changes"
		}
	case "push":
		if gitPushForce(git.Args) {
			return "forced git push can rewrite remote history"
		}
		return "git push may update the remote before a later local Git metadata write fails in the sandbox"
	}
	return ""
}

func gitCommands(command string) []gitCommand {
	fields := shellishFields(command)
	var out []gitCommand
	for _, i := range commandStartIndexes(fields) {
		if !isGitCommand(fields[i]) {
			continue
		}
		subcommand, argsStart, ok := gitSubcommand(fields, i+1)
		if !ok {
			continue
		}
		args := []string{}
		for j := argsStart; j < len(fields); j++ {
			token := trimShellToken(fields[j])
			if token == "" || isShellCommandSeparator(token) {
				break
			}
			args = append(args, token)
		}
		out = append(out, gitCommand{Subcommand: subcommand, Args: args})
	}
	return out
}

func gitSubcommand(fields []string, start int) (string, int, bool) {
	for i := start; i < len(fields); i++ {
		token := trimShellToken(fields[i])
		if token == "" || isShellCommandSeparator(token) {
			return "", i, false
		}
		if isEnvAssignment(token) {
			continue
		}
		if token == "env" || executableBase(token) == "env" || executableBase(token) == "env.exe" {
			continue
		}
		if isGitGlobalOptionWithSeparateValue(token) {
			i++
			continue
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		return strings.ToLower(token), i + 1, true
	}
	return "", len(fields), false
}

func isGitGlobalOptionWithSeparateValue(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	if strings.Contains(lower, "=") {
		return false
	}
	switch lower {
	case "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
		return true
	default:
		return false
	}
}

func isGitCommand(token string) bool {
	base := executableBase(token)
	return base == "git" || base == "git.exe"
}

func gitCleanDryRun(args []string) bool {
	return gitHasFlag(args, gitExactFlag("-n"), gitShortClusterFlag("-n"), gitLongFlag("--dry-run"))
}

func gitCheckoutDiscardsWorktree(args []string) bool {
	afterDoubleDash := false
	for _, arg := range args {
		token := trimShellToken(arg)
		if token == "--" {
			afterDoubleDash = true
			continue
		}
		if afterDoubleDash && token != "" {
			return true
		}
		if token == "." || token == ":/" {
			return true
		}
	}
	return false
}

func gitRestoreDiscardsWorktree(args []string) bool {
	staged := false
	worktree := false
	hasPath := false
	for _, arg := range args {
		token := trimShellToken(arg)
		if token == "" {
			continue
		}
		switch token {
		case "--staged":
			staged = true
		case "--worktree":
			worktree = true
		case "--":
			continue
		default:
			if !strings.HasPrefix(token, "-") {
				hasPath = true
			}
		}
	}
	return hasPath && (!staged || worktree)
}

func gitPushForce(args []string) bool {
	return gitHasFlag(args,
		gitLongFlag("--force"),
		gitLongPrefixFlag("--force-with-lease"),
		gitLongPrefixFlag("--force-if-includes"),
		gitShortClusterFlag("-f"),
	)
}

type gitFlagMatcher func(string) bool

func gitExactFlag(name string) gitFlagMatcher {
	name = strings.ToLower(strings.TrimSpace(name))
	return func(token string) bool {
		return token == name
	}
}

func gitLongFlag(name string) gitFlagMatcher {
	name = strings.ToLower(strings.TrimSpace(name))
	return func(token string) bool {
		return token == name || strings.HasPrefix(token, name+"=")
	}
}

func gitLongPrefixFlag(name string) gitFlagMatcher {
	name = strings.ToLower(strings.TrimSpace(name))
	return func(token string) bool {
		return token == name || strings.HasPrefix(token, name+"=") || strings.HasPrefix(token, name)
	}
}

func gitShortClusterFlag(name string) gitFlagMatcher {
	short := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "-")
	return func(token string) bool {
		return short != "" && strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") && strings.Contains(strings.TrimPrefix(token, "-"), short)
	}
}

func gitHasFlag(args []string, matchers ...gitFlagMatcher) bool {
	for _, arg := range args {
		token := strings.ToLower(trimShellToken(arg))
		if token == "" {
			continue
		}
		for _, matcher := range matchers {
			if matcher != nil && matcher(token) {
				return true
			}
		}
	}
	return false
}
