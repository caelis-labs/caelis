package presets

import (
	"os"
	"path/filepath"
	"strings"
)

// ─── Sensitive path protection ───────────────────────────────────────

// hiddenUserRoots are sensitive home-relative paths that all file tools
// must block unless explicitly approved.
var hiddenUserRoots = []string{
	".ssh", ".gnupg", ".aws", ".kube", ".docker",
	".netrc", ".npmrc", ".config/gh", ".config/gcloud",
}

// IsSensitivePath checks if a path falls under a sensitive user config root.
// Matches both directory prefixes (e.g., ~/.ssh/id_rsa) and exact file
// paths (e.g., ~/.netrc).
func IsSensitivePath(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range hiddenUserRoots {
		sensitive := filepath.Join(home, root)
		if strings.HasPrefix(abs, sensitive+string(filepath.Separator)) || abs == sensitive {
			return true
		}
	}
	return false
}

// ─── Command safety checks ──────────────────────────────────────────

// CommandDenyReason returns a non-empty reason if the command must be
// hard-denied (cannot be bypassed even with require_escalated).
// Returns empty string if the command is not hard-denied.
func CommandDenyReason(command string) string {
	cmd := strings.TrimSpace(command)

	// Extract shell payloads recursively.
	payloads := extractShellPayloads(cmd)
	allCmds := append([]string{cmd}, payloads...)

	for _, c := range allCmds {
		if r := checkHardDeny(c); r != "" {
			return r
		}
	}
	return ""
}

// GitControlMetadata checks if a command modifies git metadata and
// therefore requires sandbox_permissions=require_escalated.
func GitControlMetadata(command string) bool {
	cmd := strings.TrimSpace(command)
	payloads := extractShellPayloads(cmd)
	allCmds := append([]string{cmd}, payloads...)

	for _, c := range allCmds {
		if isGitMetadataCommand(c) {
			return true
		}
	}
	return false
}

// ─── Internal checks ────────────────────────────────────────────────

func checkHardDeny(cmd string) string {
	// Shell fork bomb.
	if strings.Contains(cmd, ":(){") {
		return "dangerous shell command is blocked"
	}
	// yes>/dev/null.
	if strings.Contains(cmd, "yes>") || strings.Contains(cmd, "yes >") {
		return "dangerous shell command is blocked"
	}
	// /dev/tcp/
	if strings.Contains(cmd, "/dev/tcp/") {
		return "dangerous network shell command is blocked"
	}
	// curl/wget pipe to shell.
	if (strings.Contains(cmd, "curl") || strings.Contains(cmd, "wget")) &&
		(strings.Contains(cmd, "| bash") || strings.Contains(cmd, "| sh") ||
			strings.Contains(cmd, "|bash") || strings.Contains(cmd, "|sh")) {
		return "remote script execution is blocked"
	}
	// Recursive delete.
	if isRecursiveDelete(cmd) {
		return "recursive filesystem delete command is blocked"
	}

	// Git hard-deny.
	if r := gitHardDenyReason(cmd); r != "" {
		return r
	}

	return ""
}

func isRecursiveDelete(cmd string) bool {
	// rm -rf, rm -r (not rm -ri)
	if strings.Contains(cmd, "rm ") {
		args := strings.Fields(cmd)
		for i, a := range args {
			if a == "rm" && i+1 < len(args) {
				flags := args[i+1]
				if strings.HasPrefix(flags, "-") &&
					strings.Contains(flags, "r") &&
					(strings.Contains(flags, "f") || !strings.Contains(flags, "i")) {
					return true
				}
			}
		}
	}
	// Windows: del /s, rd /s, remove-item -recurse
	lower := strings.ToLower(cmd)
	if strings.Contains(lower, "del /s") || strings.Contains(lower, "rd /s") {
		return true
	}
	if strings.Contains(lower, "remove-item") && strings.Contains(lower, "-recurse") {
		return true
	}
	return false
}

func gitHardDenyReason(cmd string) string {
	if !isGitCommand(cmd) {
		return ""
	}
	sub := gitSubcommand(cmd)
	args := gitArgs(cmd)

	switch sub {
	case "clean":
		if !hasDryRun(args) {
			return "git clean without dry-run is blocked"
		}
	case "reset":
		if containsFlag(args, "--hard") {
			return "git reset --hard is blocked"
		}
	case "checkout":
		if hasPathOperand(args) {
			return "git checkout path restore is blocked"
		}
	case "restore":
		// git restore with any non-flag argument touches the worktree.
		hasPath := false
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				hasPath = true
				break
			}
		}
		if hasPath && !containsFlag(args, "--staged-only") &&
			!containsFlag(args, "--staged") {
			return "git worktree restore is blocked"
		}
	case "push":
		if containsFlag(args, "--force") ||
			containsFlagPrefix(args, "--force-with-lease") ||
			containsFlagPrefix(args, "--force-if-includes") ||
			containsShortFlag(args, "f") {
			return "forced git push is blocked"
		}
	}
	return ""
}

func isGitMetadataCommand(cmd string) bool {
	if !isGitCommand(cmd) {
		return false
	}
	sub := gitSubcommand(cmd)
	switch sub {
	case "add", "commit", "tag", "merge", "rebase", "cherry-pick",
		"stash", "push", "reset", "checkout", "restore":
		return true
	case "clean":
		args := gitArgs(cmd)
		return !hasDryRun(args)
	}
	return false
}

// ─── Git command parsing ────────────────────────────────────────────

func isGitCommand(cmd string) bool {
	return gitSubcommand(cmd) != ""
}

func gitSubcommand(cmd string) string {
	fields := shellFields(cmd)
	// Find "git" executable.
	for i, f := range fields {
		if executableBase(f) == "git" && i+1 < len(fields) {
			// Skip global options and their arguments.
			j := i + 1
			for j < len(fields) {
				f := fields[j]
				if strings.HasPrefix(f, "-") {
					// Options like -C take an argument.
					if f == "-C" || f == "-c" || f == "--git-dir" || f == "--work-tree" {
						j += 2 // skip option + value
						continue
					}
					j++
					continue
				}
				if strings.Contains(f, "=") && strings.HasPrefix(f, "-") {
					j++
					continue
				}
				break
			}
			if j < len(fields) {
				sub := fields[j]
				if sub == "--help" || sub == "--version" || sub == "version" {
					return ""
				}
				return sub
			}
		}
	}
	return ""
}

func gitArgs(cmd string) []string {
	fields := shellFields(cmd)
	for i, f := range fields {
		if executableBase(f) == "git" && i+1 < len(fields) {
			// Skip to after subcommand.
			j := i + 1
			for j < len(fields) && strings.HasPrefix(fields[j], "-") {
				j++
			}
			if j < len(fields) {
				return fields[j+1:]
			}
		}
	}
	return nil
}

func executableBase(name string) string {
	return strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
}

func shellFields(cmd string) []string {
	// Simple tokenization — handles basic shell commands with quoting.
	fields := splitShellArgs(cmd)
	if len(fields) >= 2 {
		exe := executableBase(fields[0])
		if exe == "sh" || exe == "bash" {
			// Skip the executable and any flags, find the -c flag.
			for i := 1; i < len(fields); i++ {
				f := fields[i]
				if f == "-c" && i+1 < len(fields) {
					return splitShellArgs(fields[i+1])
				}
				// Handle compound flags like -lc, -al -c.
				if strings.HasPrefix(f, "-") && strings.Contains(f, "c") && f != "-c" {
					// -lc or -al-c: the next arg is the command.
					if i+1 < len(fields) {
						return splitShellArgs(fields[i+1])
					}
				}
				// Skip other flags.
				if strings.HasPrefix(f, "-") && len(f) > 1 {
					continue
				}
			}
		}
	}
	return fields
}

// splitShellArgs splits a shell command string respecting single and double quotes.
func splitShellArgs(s string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func extractShellPayloads(cmd string) []string {
	var payloads []string
	fields := splitShellArgs(cmd)
	// Look for sh/bash anywhere in the args (e.g., sudo sh -c ..., env bash -lc ...).
	for i, f := range fields {
		exe := executableBase(f)
		if exe == "sh" || exe == "bash" {
			for j := i + 1; j < len(fields); j++ {
				p := fields[j]
				if (p == "-c" || (strings.HasPrefix(p, "-") && strings.Contains(p, "c"))) && j+1 < len(fields) {
					inner := fields[j+1]
					if inner != "" {
						payloads = append(payloads, inner)
						payloads = append(payloads, extractShellPayloads(inner)...)
					}
					break
				}
			}
		}
	}
	return payloads
}

func hasDryRun(args []string) bool {
	for _, a := range args {
		if a == "-n" || a == "--dry-run" {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			if strings.Contains(a, "n") {
				return true
			}
		}
	}
	return false
}

func containsFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func containsFlagPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func containsShortFlag(args []string, flag string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			if strings.Contains(a, flag) {
				return true
			}
		}
	}
	return false
}

func hasPathOperand(args []string) bool {
	// Check for paths after --, or . or :/ operands.
	afterDash := false
	for _, a := range args {
		if a == "--" {
			afterDash = true
			continue
		}
		if afterDash {
			return true
		}
		if a == "." || strings.Contains(a, ":/") {
			return true
		}
	}
	return false
}
