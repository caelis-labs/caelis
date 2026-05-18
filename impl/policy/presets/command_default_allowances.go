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
	if defaultNetworkCommand(command) {
		out.Network = sandbox.NetworkEnabled
	}
	if defaultGitMetadataWriteCommand(command) {
		if root := strings.TrimSpace(input.Options.WorkspaceRoot); root != "" {
			out.PathRules = mergePathRules(out.PathRules, []sandbox.PathRule{
				{Path: filepath.Join(root, ".git"), Access: sandbox.PathAccessReadWrite},
			})
		}
	}
	return out
}

func defaultNetworkCommand(command string) bool {
	fields, ok := singleSimpleCommandFields(command)
	if !ok || len(fields) == 0 {
		return false
	}
	exe, ok := trustedBareExecutableName(fields[0], "go", "cargo", "npm", "pnpm", "yarn", "yarnpkg", "pip", "pip3", "uv")
	if !ok {
		return false
	}
	args := fields[1:]
	switch exe {
	case "go":
		return goDependencyNetworkCommand(args)
	case "cargo":
		return firstArgIn(args, "fetch", "update")
	case "npm":
		return npmDependencyNetworkCommand(args)
	case "pnpm":
		return firstArgIn(args, "fetch") ||
			(firstArgIn(args, "install", "i", "add") && hasFlag(args, "--ignore-scripts"))
	case "yarn", "yarnpkg":
		return firstArgIn(args, "install", "add") &&
			(hasFlag(args, "--ignore-scripts") || hasFlagValue(args, "--mode", "skip-builds"))
	case "pip", "pip3":
		return firstArgIn(args, "download")
	case "uv":
		return len(args) >= 2 && args[0] == "pip" && args[1] == "download"
	default:
		return false
	}
}

func goDependencyNetworkCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "get" {
		return true
	}
	return len(args) >= 2 && args[0] == "mod" && (args[1] == "download" || args[1] == "tidy")
}

func npmDependencyNetworkCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "ci":
		return hasFlag(args, "--ignore-scripts")
	case "install", "i", "add":
		return hasFlag(args, "--ignore-scripts") || hasFlag(args, "--package-lock-only")
	case "update":
		return hasFlag(args, "--package-lock-only")
	default:
		return false
	}
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

func singleSimpleCommandFields(command string) ([]string, bool) {
	segments, ok := simpleCommandSegments(command, false)
	if !ok || len(segments) != 1 {
		return nil, false
	}
	fields := segments[0]
	fields = trimLeadingEnvAssignments(fields)
	if len(fields) == 0 {
		return nil, false
	}
	return fields, true
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

func firstArgIn(args []string, values ...string) bool {
	if len(args) == 0 {
		return false
	}
	for _, value := range values {
		if args[0] == value {
			return true
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func hasFlagValue(args []string, flag string, value string) bool {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
		if strings.HasPrefix(arg, flag+"=") && strings.TrimPrefix(arg, flag+"=") == value {
			return true
		}
	}
	return false
}
