package presets

import (
	"fmt"
	"strings"
)

type gitCommandPolicy int

const (
	gitPolicyAllow gitCommandPolicy = iota
	gitPolicyDenyAlways
	gitPolicyDenyInSandbox
)

type gitCommandClassification struct {
	Policy gitCommandPolicy
	Reason string
}

type gitCommand struct {
	Subcommand string
	Args       []string
	Display    string
}

func hardDenyGitCommandReason(command string) string {
	return gitCommandDenyReason(command, gitPolicyDenyAlways)
}

func vcsSandboxDenyReason(command string) string {
	return gitCommandDenyReason(command, gitPolicyDenyInSandbox)
}

func gitCommandDenyReason(command string, policy gitCommandPolicy) string {
	for _, git := range gitCommands(command) {
		classification := classifyGitCommand(git)
		if classification.Policy == policy {
			return classification.Reason
		}
	}
	for _, payload := range shellCommandPayloads(command) {
		if reason := gitCommandDenyReason(payload, policy); reason != "" {
			return reason
		}
	}
	return ""
}

func classifyGitCommand(git gitCommand) gitCommandClassification {
	switch git.Subcommand {
	case "clean":
		if !gitCleanDryRun(git.Args) {
			return gitCommandClassification{Policy: gitPolicyDenyAlways, Reason: "git clean without dry-run is blocked"}
		}
	case "reset":
		if gitHasFlag(git.Args, gitLongFlag("--hard")) {
			return gitCommandClassification{Policy: gitPolicyDenyAlways, Reason: "git reset --hard is blocked"}
		}
	case "checkout":
		if gitCheckoutDiscardsWorktree(git.Args) {
			return gitCommandClassification{Policy: gitPolicyDenyAlways, Reason: "git checkout path restore is blocked"}
		}
	case "restore":
		if gitRestoreDiscardsWorktree(git.Args) {
			return gitCommandClassification{Policy: gitPolicyDenyAlways, Reason: "git worktree restore is blocked"}
		}
	case "push":
		if gitPushForce(git.Args) {
			return gitCommandClassification{Policy: gitPolicyDenyAlways, Reason: "forced git push is blocked"}
		}
	}
	if gitMayWriteMetadata(git) {
		return gitCommandClassification{
			Policy: gitPolicyDenyInSandbox,
			Reason: fmt.Sprintf(
				"Denied: %s may write Git metadata under .git. Retry the same command with sandbox_permissions=require_escalated and a non-empty justification if it is required.",
				gitDisplayCommand(git),
			),
		}
	}
	return gitCommandClassification{}
}

func gitDisplayCommand(git gitCommand) string {
	display := strings.TrimSpace(git.Display)
	if display != "" {
		return display
	}
	return strings.TrimSpace("git " + git.Subcommand + " " + strings.Join(git.Args, " "))
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
		out = append(out, gitCommand{Subcommand: subcommand, Args: args, Display: commandSegmentDisplay(fields, i)})
	}
	return out
}

func commandSegmentDisplay(fields []string, start int) string {
	if start < 0 || start >= len(fields) {
		return ""
	}
	var segment []string
	for i := start; i < len(fields); i++ {
		token := trimShellToken(fields[i])
		if token == "" {
			continue
		}
		if isShellCommandSeparator(token) {
			break
		}
		segment = append(segment, token)
	}
	return strings.TrimSpace(strings.Join(segment, " "))
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

func gitMayWriteMetadata(git gitCommand) bool {
	if gitArgsRequestHelp(git.Args) {
		return false
	}
	switch git.Subcommand {
	case "add", "commit", "push", "pull", "fetch", "init", "checkout", "switch", "merge", "rebase", "cherry-pick", "revert", "reset", "submodule", "worktree":
		return true
	case "restore":
		return gitHasFlag(git.Args, gitLongFlag("--staged"), gitShortClusterFlag("-s"))
	case "stash":
		return gitStashWrites(git.Args)
	case "tag":
		return gitTagWrites(git.Args)
	case "branch":
		return gitBranchWrites(git.Args)
	case "remote":
		return gitRemoteWrites(git.Args)
	case "config":
		return gitConfigWrites(git.Args)
	default:
		return false
	}
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

func gitStashWrites(args []string) bool {
	subcommand := firstNonOptionGitArg(args)
	return subcommand != "list" && subcommand != "show"
}

func gitArgsRequestHelp(args []string) bool {
	return gitHasFlag(args, gitLongFlag("--help"), gitExactFlag("-h"))
}

func gitTagWrites(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if gitHasFlag(args, gitLongFlag("--delete"), gitShortClusterFlag("-d")) {
		return true
	}
	if gitHasFlag(args,
		gitLongFlag("--list"),
		gitExactFlag("-l"),
		gitLongFlag("--points-at"),
		gitLongFlag("--contains"),
		gitLongFlag("--no-contains"),
		gitLongFlag("--merged"),
		gitLongFlag("--no-merged"),
		gitLongFlag("--sort"),
		gitLongFlag("--format"),
		gitLongFlag("--column"),
		gitLongFlag("--no-column"),
		gitLongFlag("--ignore-case"),
		gitShortPrefixFlag("-n"),
		gitLongFlag("--verify"),
		gitExactFlag("-v"),
	) {
		return false
	}
	return firstNonOptionGitArg(args) != ""
}

func gitBranchWrites(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if gitHasFlag(args,
		gitLongFlag("--delete"),
		gitExactFlag("-d"),
		gitExactFlag("-D"),
		gitLongFlag("--move"),
		gitExactFlag("-m"),
		gitExactFlag("-M"),
		gitLongFlag("--copy"),
		gitExactFlag("-c"),
		gitExactFlag("-C"),
		gitLongFlag("--set-upstream-to"),
		gitExactFlag("-u"),
		gitLongFlag("--unset-upstream"),
		gitLongFlag("--track"),
		gitLongFlag("--set-upstream"),
		gitLongFlag("--edit-description"),
	) {
		return true
	}
	if gitHasFlag(args,
		gitLongFlag("--list"),
		gitLongFlag("--show-current"),
		gitLongFlag("--all"),
		gitExactFlag("-a"),
		gitLongFlag("--remotes"),
		gitExactFlag("-r"),
		gitLongFlag("--contains"),
		gitLongFlag("--no-contains"),
		gitLongFlag("--merged"),
		gitLongFlag("--no-merged"),
		gitLongFlag("--points-at"),
		gitLongFlag("--format"),
		gitLongFlag("--sort"),
		gitLongFlag("--column"),
		gitLongFlag("--no-column"),
		gitLongFlag("--verbose"),
		gitExactFlag("-v"),
		gitExactFlag("-vv"),
	) {
		return false
	}
	return firstNonOptionGitArg(args) != ""
}

func gitRemoteWrites(args []string) bool {
	switch firstNonOptionGitArg(args) {
	case "add", "remove", "rm", "rename", "set-url", "set-head", "set-branches", "prune", "update":
		return true
	default:
		return false
	}
}

func gitConfigWrites(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if gitHasFlag(args,
		gitLongFlag("--unset"),
		gitLongFlag("--unset-all"),
		gitLongFlag("--add"),
		gitLongFlag("--replace-all"),
		gitLongFlag("--rename-section"),
		gitLongFlag("--remove-section"),
		gitLongFlag("--edit"),
		gitExactFlag("-e"),
	) {
		return true
	}
	if gitHasFlag(args,
		gitLongFlag("--get"),
		gitLongFlag("--get-all"),
		gitLongFlag("--get-regexp"),
		gitLongFlag("--get-urlmatch"),
		gitLongFlag("--list"),
		gitExactFlag("-l"),
		gitLongFlag("--null"),
		gitExactFlag("-z"),
		gitLongFlag("--name-only"),
		gitLongFlag("--show-origin"),
		gitLongFlag("--show-scope"),
	) {
		return false
	}
	return countNonOptionGitArgs(args) >= 2
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

func gitShortPrefixFlag(name string) gitFlagMatcher {
	name = strings.ToLower(strings.TrimSpace(name))
	return func(token string) bool {
		return strings.HasPrefix(token, name)
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

func firstNonOptionGitArg(args []string) string {
	for _, arg := range args {
		token := trimShellToken(arg)
		if token == "" || token == "--" {
			continue
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		return strings.ToLower(token)
	}
	return ""
}

func countNonOptionGitArgs(args []string) int {
	count := 0
	for _, arg := range args {
		token := trimShellToken(arg)
		if token == "" || token == "--" || strings.HasPrefix(token, "-") {
			continue
		}
		count++
	}
	return count
}
