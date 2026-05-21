package presets

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func applyDefaultCommandAllowances(base sandbox.Constraints, input policy.ToolContext, command string) sandbox.Constraints {
	out := sandbox.NormalizeConstraints(base)
	if defaultGitMetadataWriteCommand(command) {
		if root := strings.TrimSpace(input.Options.WorkspaceRoot); root != "" {
			out.PathRules = mergePathRules(out.PathRules, []sandbox.PathRule{
				{Path: filepath.Join(root, ".git"), Access: sandbox.PathAccessReadWrite},
			})
		}
	}
	return out
}

func defaultGitMetadataWriteCommand(command string) bool {
	segments, ok := simpleCommandSegments(command, true)
	if !ok || len(segments) == 0 {
		return false
	}
	for _, fields := range segments {
		fields = trimLeadingEnvAssignments(fields)
		if len(fields) == 0 {
			return false
		}
		if _, ok := trustedBareExecutableName(fields[0], "git"); !ok {
			return false
		}
		subcommand := gitSubcommand(fields[1:])
		switch subcommand {
		case "add", "commit", "tag":
		default:
			return false
		}
	}
	return true
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		switch arg {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			i++
			continue
		}
		if strings.HasPrefix(arg, "--git-dir=") ||
			strings.HasPrefix(arg, "--work-tree=") ||
			strings.HasPrefix(arg, "--namespace=") {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

func simpleCommandSegments(command string, allowSequential bool) ([][]string, bool) {
	if strings.Contains(command, "`") || strings.Contains(command, "$(") ||
		strings.Contains(command, "\n") || strings.ContainsAny(command, "<>") ||
		hasUnsupportedAmpersand(command) {
		return nil, false
	}
	fields := shellishFields(command)
	segments := [][]string{}
	current := []string{}
	for _, field := range fields {
		if !isShellCommandSeparator(field) {
			current = append(current, field)
			continue
		}
		if field == "|" || !allowSequential {
			return nil, false
		}
		if len(current) == 0 {
			return nil, false
		}
		segments = append(segments, current)
		current = []string{}
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	if len(segments) == 0 {
		return nil, false
	}
	return segments, true
}

func trimLeadingEnvAssignments(fields []string) []string {
	for len(fields) > 0 && isSafeEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) == 0 || fields[0] != "env" {
		return fields
	}
	fields = fields[1:]
	for len(fields) > 0 && isSafeEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	return fields
}

func isSafeEnvAssignment(field string) bool {
	name, _, ok := strings.Cut(field, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return name != "PATH"
}

func trustedBareExecutableName(field string, allowed ...string) (string, bool) {
	field = strings.TrimSpace(field)
	if field == "" || strings.ContainsAny(field, `/\`) {
		return "", false
	}
	for _, name := range allowed {
		if field == name {
			return field, true
		}
	}
	return "", false
}

func hasUnsupportedAmpersand(command string) bool {
	for i := 0; i < len(command); i++ {
		if command[i] != '&' {
			continue
		}
		if i+1 < len(command) && command[i+1] == '&' {
			i++
			continue
		}
		return true
	}
	return false
}
