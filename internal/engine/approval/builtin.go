package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func BuiltinToolsPolicy() Policy {
	return PolicyFunc(func(ctx context.Context, req Request) (Decision, error) {
		switch normalizeToolName(req.Call.Name) {
		case "write_file", "patch_file":
			return AskAll().ReviewToolCall(ctx, req)
		case "run_command":
			return reviewBuiltinCommand(req)
		default:
			return Decision{}, nil
		}
	})
}

func reviewBuiltinCommand(req Request) (Decision, error) {
	command, ok, err := commandInput(req.Call.Input)
	if err != nil {
		return Decision{Verdict: VerdictDeny, Reason: err.Error()}, nil
	}
	if !ok || command == "" {
		return Decision{}, nil
	}
	if commandLooksDangerous(command) {
		return Decision{
			Verdict: VerdictDeny,
			Reason:  "dangerous command is blocked in " + NormalizeMode(req.Mode) + " mode",
			Meta: map[string]any{
				"dangerous_command": true,
			},
		}, nil
	}
	if commandRequiresDestructiveApproval(command) {
		return Decision{
			Verdict: VerdictAsk,
			Reason:  "destructive filesystem command requires approval",
			Options: oneShotOptions(),
			Meta: map[string]any{
				"destructive_command": true,
			},
		}, nil
	}
	return Decision{}, nil
}

func commandInput(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, fmt.Errorf("run_command input must be a JSON object")
	}
	rawCommand, ok := args["command"]
	if !ok || rawCommand == nil {
		return "", false, nil
	}
	command, ok := rawCommand.(string)
	if !ok {
		return "", false, fmt.Errorf("run_command.command must be a string")
	}
	return strings.TrimSpace(command), true, nil
}

func commandLooksDangerous(command string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(command)), ""))
	if compact == "" {
		return false
	}
	switch {
	case strings.Contains(compact, ":(){"):
		return true
	case strings.Contains(compact, "yes>/dev/null"):
		return true
	case strings.Contains(compact, "/dev/tcp/"):
		return true
	case strings.Contains(compact, "curl") && (strings.Contains(compact, "|bash") || strings.Contains(compact, "|sh")):
		return true
	case strings.Contains(compact, "wget") && (strings.Contains(compact, "|bash") || strings.Contains(compact, "|sh")):
		return true
	case commandRemovesProtectedTarget(command):
		return true
	case strings.Contains(compact, "gitreset--hard"):
		return true
	case strings.Contains(compact, "gitpush--force") || strings.Contains(compact, "gitpush-f"):
		return true
	}
	return false
}

func commandRequiresDestructiveApproval(command string) bool {
	return len(recursiveForceRemoveTargets(command)) > 0
}

func commandRemovesProtectedTarget(command string) bool {
	for _, target := range recursiveForceRemoveTargets(command) {
		if destructiveTargetIsProtected(target) {
			return true
		}
	}
	return false
}

func recursiveForceRemoveTargets(command string) []string {
	fields := shellishFields(command)
	targets := []string{}
	for i := 0; i < len(fields); i++ {
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
			if !afterDoubleDash && strings.HasPrefix(token, "-") && token != "-" {
				if strings.ContainsAny(token, "rR") || strings.Contains(token, "recursive") {
					recursive = true
				}
				if strings.Contains(token, "f") || strings.Contains(token, "force") {
					force = true
				}
				continue
			}
			operands = append(operands, token)
		}
		if recursive && force {
			targets = append(targets, operands...)
		}
	}
	return targets
}

func shellishFields(command string) []string {
	command = strings.NewReplacer(
		"&&", " && ",
		"||", " || ",
		";", " ; ",
		"|", " | ",
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
	case ";", "&&", "||", "|":
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
	return token == "rm" || filepath.Base(token) == "rm"
}

func destructiveTargetIsProtected(target string) bool {
	target = trimShellToken(target)
	if target == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimRight(target, "/"))
	switch lower {
	case "", "/", "/.", "/..", "/*", "~", "$home", "${home}":
		return true
	}
	if strings.HasPrefix(lower, "~/") || strings.HasPrefix(lower, "$home/") || strings.HasPrefix(lower, "${home}/") {
		return true
	}
	return filepath.Clean(target) == string(filepath.Separator)
}
