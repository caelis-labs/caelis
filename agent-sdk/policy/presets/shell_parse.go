package presets

import (
	"path/filepath"
	"strings"
)

func recursiveRemoveTargets(command string) []string {
	return rmTargets(command, false)
}

func rmTargets(command string, requireForce bool) []string {
	fields := shellishFields(command)
	targets := []string{}
	for _, i := range commandStartIndexes(fields) {
		if !isRmCommand(fields[i]) {
			continue
		}
		recursive := false
		force := false
		operands := []string{}
		afterDoubleDash := false
		for j := i + 1; j < len(fields); j++ {
			token := trimShellToken(fields[j])
			if token == "" || isShellCommandSeparator(token) {
				break
			}
			if !afterDoubleDash && token == "--" {
				afterDoubleDash = true
				continue
			}
			lower := strings.ToLower(token)
			if !afterDoubleDash && strings.HasPrefix(token, "-") && token != "-" {
				if strings.ContainsAny(token, "rR") || strings.Contains(lower, "recursive") {
					recursive = true
				}
				if strings.Contains(lower, "f") || strings.Contains(lower, "force") {
					force = true
				}
				continue
			}
			operands = append(operands, token)
		}
		if recursive && (!requireForce || force) {
			targets = append(targets, operands...)
		}
	}
	return targets
}

func shellishFields(command string) []string {
	command = strings.NewReplacer(
		"\r\n", " ; ",
		"\n", " ; ",
		"\r", " ; ",
		"&&", " && ",
		"||", " || ",
		";", " ; ",
		"|", " | ",
		"&", " & ",
	).Replace(command)
	out := []string{}
	for _, field := range strings.Fields(command) {
		field = trimShellToken(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func trimShellToken(token string) string {
	return strings.Trim(strings.TrimSpace(token), `"'`)
}

func isShellCommandSeparator(token string) bool {
	switch token {
	case ";", "&&", "||", "|", "&":
		return true
	default:
		return false
	}
}

func isRmCommand(token string) bool {
	token = trimShellToken(token)
	if token == "" {
		return false
	}
	base := executableBase(token)
	return base == "rm" || base == "rm.exe"
}

func commandContainsRecursiveDelete(command string) bool {
	if len(recursiveRemoveTargets(command)) > 0 {
		return true
	}
	fields := shellishFields(command)
	for _, i := range commandStartIndexes(fields) {
		token := executableBase(fields[i])
		switch token {
		case "remove-item", "remove-item.exe", "ri", "ri.exe":
			if commandSegmentHasFlag(fields[i+1:], "-recurse", "-recursive") {
				return true
			}
		case "del", "del.exe", "erase", "erase.exe", "rd", "rd.exe", "rmdir", "rmdir.exe":
			if commandSegmentHasSlashFlag(fields[i+1:], "/s") {
				return true
			}
		}
	}
	return false
}

func commandStartIndexes(fields []string) []int {
	var out []int
	for i := 0; i < len(fields); {
		for i < len(fields) && isShellCommandSeparator(trimShellToken(fields[i])) {
			i++
		}
		if i >= len(fields) {
			break
		}
		if idx := commandStartIndexInSegment(fields, i); idx >= 0 {
			out = append(out, idx)
		}
		for i < len(fields) && !isShellCommandSeparator(trimShellToken(fields[i])) {
			i++
		}
	}
	return out
}

func commandStartIndexInSegment(fields []string, start int) int {
	inEnv := false
	for i := start; i < len(fields); i++ {
		token := trimShellToken(fields[i])
		if token == "" {
			continue
		}
		if isShellCommandSeparator(token) {
			return -1
		}
		base := executableBase(token)
		switch base {
		case "env", "command":
			inEnv = base == "env"
			continue
		case "sudo":
			i = skipSudoPrefix(fields, i+1) - 1
			continue
		}
		if isEnvAssignment(token) {
			continue
		}
		if inEnv && strings.HasPrefix(token, "-") {
			continue
		}
		return i
	}
	return -1
}

func skipSudoPrefix(fields []string, start int) int {
	for i := start; i < len(fields); i++ {
		token := trimShellToken(fields[i])
		if token == "" {
			continue
		}
		if isShellCommandSeparator(token) {
			return i
		}
		if token == "--" {
			return i + 1
		}
		if isEnvAssignment(token) {
			continue
		}
		if !strings.HasPrefix(token, "-") || token == "-" {
			return i
		}
		if sudoOptionNeedsValue(token) && !sudoOptionHasInlineValue(token) {
			i++
		}
	}
	return len(fields)
}

func sudoOptionNeedsValue(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	if lower == "" || lower == "-" {
		return false
	}
	if strings.HasPrefix(lower, "--") {
		name := lower
		if idx := strings.Index(name, "="); idx >= 0 {
			name = name[:idx]
		}
		switch name {
		case "--user", "--group", "--host", "--prompt", "--close-from", "--chdir", "--role", "--type", "--login-class", "--command-timeout", "--other-user":
			return true
		default:
			return false
		}
	}
	if !strings.HasPrefix(lower, "-") {
		return false
	}
	return sudoShortValueOptionIndex(lower) >= 0
}

func sudoOptionHasInlineValue(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	if strings.Contains(lower, "=") {
		return true
	}
	if strings.HasPrefix(lower, "--") {
		return false
	}
	idx := sudoShortValueOptionIndex(lower)
	return idx >= 0 && len(lower) > idx+1
}

func sudoShortValueOptionIndex(lower string) int {
	if !strings.HasPrefix(lower, "-") || strings.HasPrefix(lower, "--") {
		return -1
	}
	for i := 1; i < len(lower); i++ {
		switch lower[i] {
		case 'u', 'g', 'h', 'p', 'c', 'd', 'r', 't':
			return i
		}
	}
	return -1
}

func shellCommandPayloads(command string) []string {
	fields := shellishFields(command)
	var out []string
	for _, i := range commandStartIndexes(fields) {
		if start, ok := shellPayloadStart(fields, i); ok && start < len(fields) {
			payload := strings.TrimSpace(strings.Join(fields[start:], " "))
			if payload != "" && payload != strings.TrimSpace(command) {
				out = append(out, payload)
			}
		}
	}
	return out
}

func shellPayloadStart(fields []string, commandIndex int) (int, bool) {
	if commandIndex < 0 || commandIndex >= len(fields) {
		return 0, false
	}
	base := executableBase(fields[commandIndex])
	switch base {
	case "sh", "sh.exe", "bash", "bash.exe", "zsh", "zsh.exe", "dash", "dash.exe", "ksh", "ksh.exe", "fish", "fish.exe", "ash", "ash.exe":
		return posixShellPayloadStart(fields, commandIndex+1)
	case "cmd", "cmd.exe":
		return cmdPayloadStart(fields, commandIndex+1)
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return powershellPayloadStart(fields, commandIndex+1)
	default:
		return 0, false
	}
}

func posixShellPayloadStart(fields []string, start int) (int, bool) {
	for i := start; i < len(fields); i++ {
		token := trimShellToken(fields[i])
		if token == "" {
			continue
		}
		if isShellCommandSeparator(token) {
			return 0, false
		}
		if strings.HasPrefix(token, "-") && token != "-" {
			if strings.Contains(strings.TrimLeft(strings.ToLower(token), "-"), "c") {
				return i + 1, true
			}
			if shellOptionNeedsValue(token) {
				i++
			}
			continue
		}
		return 0, false
	}
	return 0, false
}

func shellOptionNeedsValue(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	switch lower {
	case "-o", "--option":
		return true
	default:
		return false
	}
}

func cmdPayloadStart(fields []string, start int) (int, bool) {
	for i := start; i < len(fields); i++ {
		token := strings.ToLower(trimShellToken(fields[i]))
		if token == "" {
			continue
		}
		if isShellCommandSeparator(token) {
			return 0, false
		}
		if token == "/c" || token == "-c" {
			return i + 1, true
		}
	}
	return 0, false
}

func powershellPayloadStart(fields []string, start int) (int, bool) {
	for i := start; i < len(fields); i++ {
		token := strings.ToLower(trimShellToken(fields[i]))
		if token == "" {
			continue
		}
		if isShellCommandSeparator(token) {
			return 0, false
		}
		switch token {
		case "-command", "-c", "/c":
			return i + 1, true
		case "-file", "-f":
			return 0, false
		}
		if powershellOptionNeedsValue(token) {
			i++
		}
	}
	return 0, false
}

func powershellOptionNeedsValue(token string) bool {
	switch token {
	case "-executionpolicy", "-encodedcommand", "-ec", "-configurationname", "-workingdirectory", "-inputformat", "-outputformat", "-windowstyle":
		return true
	default:
		return false
	}
}

func executableBase(token string) string {
	token = strings.ToLower(trimShellToken(token))
	token = strings.ReplaceAll(token, "\\", "/")
	token = strings.TrimSuffix(token, ".cmd")
	token = strings.TrimSuffix(token, ".bat")
	base := pathBase(token)
	return strings.TrimSpace(base)
}

func pathBase(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return filepath.Base(path)
}

func isEnvAssignment(token string) bool {
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	idx := strings.Index(token, "=")
	return idx > 0
}

func commandSegmentHasFlag(fields []string, names ...string) bool {
	wanted := map[string]struct{}{}
	for _, name := range names {
		wanted[strings.ToLower(name)] = struct{}{}
	}
	for _, field := range fields {
		token := strings.ToLower(trimShellToken(field))
		if token == "" || isShellCommandSeparator(token) {
			return false
		}
		if _, ok := wanted[token]; ok {
			return true
		}
	}
	return false
}

func commandSegmentHasSlashFlag(fields []string, flag string) bool {
	flag = strings.ToLower(strings.TrimSpace(flag))
	flagName := strings.TrimPrefix(flag, "/")
	for _, field := range fields {
		token := strings.ToLower(trimShellToken(field))
		if token == "" || isShellCommandSeparator(token) {
			return false
		}
		if token == flag {
			return true
		}
		if strings.HasPrefix(token, "/") {
			for _, part := range strings.Split(strings.TrimLeft(token, "/"), "/") {
				if part == flagName {
					return true
				}
			}
		}
	}
	return false
}
