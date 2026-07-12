package presets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// commandClass is one classified RUN_COMMAND outcome. Zero Action means the
// scanner did not match; decideCommand treats that as continue/allow.
type commandClass struct {
	Action      policy.Action
	Reason      string
	RiskClass   string
	Constraints sandbox.Constraints
}

func decideCommand(input policy.ToolContext, def sandbox.Constraints) (policy.Decision, error) {
	command, err := commandArg(input)
	if err != nil {
		return policy.Decision{}, err
	}
	req, err := parseCommandSandboxRequest(input)
	if err != nil {
		return deny(err.Error()), nil
	}
	class := classifyRUNCommand(command, input.Options, req, input.Sandbox)
	switch class.Action {
	case policy.ActionDeny:
		return deny(class.Reason), nil
	case policy.ActionAskApproval:
		return askCommandApproval(input, req, class)
	default:
		return allow(def), nil
	}
}

func classifyRUNCommand(command string, opts policy.ModeOptions, req commandSandboxRequest, desc sandbox.Descriptor) commandClass {
	if class := scanCommandTree(command, opts, classifyMachineHardDeny); class.Action == policy.ActionDeny {
		return class
	}
	reason, riskClass := "", ""
	if gitReason := gitCommandApprovalReason(command); gitReason != "" {
		reason, riskClass = gitReason, riskClassVCSDestructive
	} else if pathReason := outOfRootsRecursiveDeleteReason(command, opts); pathReason != "" {
		reason, riskClass = pathReason, riskClassPathEscape
	}
	if class, gated := hostGatedCommand(reason, riskClass, req, desc); gated {
		return class
	}
	return commandClass{}
}

func hostGatedCommand(reason string, riskClass string, req commandSandboxRequest, desc sandbox.Descriptor) (commandClass, bool) {
	hostExecution := commandHostApprovalRequired(req, desc)
	if reason == "" && !hostExecution {
		return commandClass{}, false
	}
	if hostExecution {
		if denyReason := req.explicitEscalationDenyReason(); denyReason != "" {
			return commandClass{Action: policy.ActionDeny, Reason: denyReason, RiskClass: riskClassHostExec}, true
		}
		if reason == "" {
			reason = "host execution requested"
		}
		if riskClass == "" {
			riskClass = riskClassHostExec
		}
		return commandClass{
			Action:      policy.ActionAskApproval,
			Reason:      reason,
			RiskClass:   riskClass,
			Constraints: hostExecutionConstraints(),
		}, true
	}
	return commandClass{
		Action: policy.ActionDeny,
		Reason: reason + "; retry this exact command with sandbox_permissions=require_escalated and a concrete justification because policy requires Host review",
	}, true
}

func scanCommandTree(command string, opts policy.ModeOptions, classify func(string, policy.ModeOptions) commandClass) commandClass {
	if class := classify(command, opts); class.Action != "" {
		return class
	}
	for _, payload := range shellCommandPayloads(command) {
		if class := classify(payload, opts); class.Action != "" {
			return class
		}
	}
	return commandClass{}
}

func classifyMachineHardDeny(command string, opts policy.ModeOptions) commandClass {
	compact := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(command)), ""))
	if compact == "" {
		return commandClass{}
	}
	switch {
	case strings.Contains(compact, ":(){"):
		return commandClass{Action: policy.ActionDeny, Reason: "dangerous shell command is blocked", RiskClass: riskClassMachine}
	case strings.Contains(compact, "yes>/dev/null"):
		return commandClass{Action: policy.ActionDeny, Reason: "dangerous shell command is blocked", RiskClass: riskClassMachine}
	case strings.Contains(compact, "/dev/tcp/"):
		return commandClass{Action: policy.ActionDeny, Reason: "dangerous network shell command is blocked", RiskClass: riskClassMachine}
	case strings.Contains(compact, "curl") && (strings.Contains(compact, "|bash") || strings.Contains(compact, "|sh")):
		return commandClass{Action: policy.ActionDeny, Reason: "remote script execution is blocked", RiskClass: riskClassMachine}
	case strings.Contains(compact, "wget") && (strings.Contains(compact, "|bash") || strings.Contains(compact, "|sh")):
		return commandClass{Action: policy.ActionDeny, Reason: "remote script execution is blocked", RiskClass: riskClassMachine}
	case commandLooksLikeDeviceWipe(compact):
		return commandClass{Action: policy.ActionDeny, Reason: "device or filesystem wipe command is blocked", RiskClass: riskClassMachine}
	}
	if reason := catastrophicRecursiveDeleteReason(command, opts); reason != "" {
		return commandClass{Action: policy.ActionDeny, Reason: reason, RiskClass: riskClassMachine}
	}
	return commandClass{}
}

func askCommandApproval(input policy.ToolContext, req commandSandboxRequest, class commandClass) (policy.Decision, error) {
	reason := strings.TrimSpace(class.Reason)
	if reason == "" {
		reason = "host execution requires approval"
	}
	decision, err := askApproval(reason, class.Constraints, input)
	if err != nil {
		return policy.Decision{}, err
	}
	decision.Metadata = req.approvalMetadata(reason)
	if decision.Metadata == nil {
		decision.Metadata = map[string]any{}
	}
	if risk := strings.TrimSpace(class.RiskClass); risk != "" {
		decision.Metadata["risk_class"] = risk
	} else {
		decision.Metadata["risk_class"] = riskClassHostExec
	}
	return decision, nil
}

func commandHostApprovalRequired(req commandSandboxRequest, desc sandbox.Descriptor) bool {
	return req.SandboxPermissions == commandSandboxPermissionRequireEscalated ||
		sandbox.DescriptorImpliesHostExecution(desc)
}

func commandArg(input policy.ToolContext) (string, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return "", err
	}
	command, _ := args["command"].(string)
	return strings.TrimSpace(command), nil
}

func commandLooksLikeDeviceWipe(compact string) bool {
	if strings.Contains(compact, "mkfs") || strings.Contains(compact, "diskpart") {
		return true
	}
	if strings.Contains(compact, "cipher/w") || strings.Contains(compact, "cipher/w:") {
		return true
	}
	if !strings.Contains(compact, "dd") {
		return false
	}
	return strings.Contains(compact, "of=/dev/") || strings.Contains(compact, "of=\\\\.\\")
}

func catastrophicRecursiveDeleteReason(command string, opts policy.ModeOptions) string {
	if !commandContainsRecursiveDelete(command) {
		return ""
	}
	for _, target := range recursiveDeleteTargets(command, opts) {
		if isCatastrophicDeleteTarget(target, opts) {
			return "recursive filesystem delete of a system or home root is blocked"
		}
	}
	return ""
}

func outOfRootsRecursiveDeleteReason(command string, opts policy.ModeOptions) string {
	if !commandContainsRecursiveDelete(command) {
		return ""
	}
	targets := recursiveDeleteTargets(command, opts)
	if len(targets) == 0 {
		return "recursive filesystem delete has unresolved targets"
	}
	roots := writableRoots(opts)
	for _, target := range targets {
		if isCatastrophicDeleteTarget(target, opts) {
			continue
		}
		if !withinAnyRoot(target, roots) {
			return "recursive filesystem delete targets paths outside allowed roots"
		}
	}
	return ""
}

func recursiveDeleteTargets(command string, opts policy.ModeOptions) []string {
	raw := append([]string{}, recursiveRemoveTargets(command)...)
	raw = append(raw, windowsRecursiveDeleteTargets(command)...)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, one := range raw {
		if resolved := resolvePolicyPath(expandDeleteTarget(one), opts.WorkspaceRoot); resolved != "" {
			out = append(out, resolved)
		}
	}
	return out
}

func windowsRecursiveDeleteTargets(command string) []string {
	fields := shellishFields(command)
	var out []string
	for _, i := range commandStartIndexes(fields) {
		base := executableBase(fields[i])
		switch base {
		case "remove-item", "remove-item.exe", "ri", "ri.exe":
			if !commandSegmentHasFlag(fields[i+1:], "-recurse", "-recursive") {
				continue
			}
			out = append(out, commandSegmentPathOperands(fields[i+1:])...)
		case "del", "del.exe", "erase", "erase.exe", "rd", "rd.exe", "rmdir", "rmdir.exe":
			if !commandSegmentHasSlashFlag(fields[i+1:], "/s") {
				continue
			}
			out = append(out, commandSegmentPathOperands(fields[i+1:])...)
		}
	}
	return out
}

func commandSegmentPathOperands(fields []string) []string {
	var out []string
	for _, field := range fields {
		token := trimShellToken(field)
		if token == "" || isShellCommandSeparator(token) {
			break
		}
		if strings.HasPrefix(token, "-") || strings.HasPrefix(token, "/") {
			continue
		}
		out = append(out, token)
	}
	return out
}

func expandDeleteTarget(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		switch {
		case value == "~":
			return home
		case strings.HasPrefix(value, "~/"):
			return filepath.Join(home, value[2:])
		case strings.EqualFold(value, "$home") || strings.EqualFold(value, "%userprofile%"):
			return home
		case strings.HasPrefix(strings.ToLower(value), "$home/"):
			return filepath.Join(home, value[len("$home/"):])
		case strings.HasPrefix(strings.ToLower(value), "%userprofile%"+string(filepath.Separator)):
			return filepath.Join(home, value[len("%userprofile%")+1:])
		case strings.HasPrefix(strings.ToLower(value), "%userprofile%/"):
			return filepath.Join(home, value[len("%userprofile%/"):])
		}
	}
	return value
}

func isCatastrophicDeleteTarget(target string, opts policy.ModeOptions) bool {
	target = normalizeTarget(expandDeleteTarget(target))
	if target == "" {
		return false
	}
	if withinAnyRoot(target, writableRoots(opts)) {
		return false
	}
	if target == string(filepath.Separator) {
		return true
	}
	if runtime.GOOS == "windows" {
		vol := filepath.VolumeName(target)
		if vol != "" && target == normalizeTarget(vol+string(filepath.Separator)) {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		home = normalizeTarget(home)
		if target == home || target == normalizeTarget(filepath.Dir(home)) {
			return true
		}
	}
	for _, root := range catastrophicSystemRoots() {
		if target == normalizeTarget(root) {
			return true
		}
	}
	return false
}

func catastrophicSystemRoots() []string {
	if runtime.GOOS == "windows" {
		return []string{`C:\Windows`, `C:\Windows\System32`, `C:\Program Files`, `C:\Program Files (x86)`}
	}
	return []string{"/bin", "/sbin", "/usr", "/etc", "/var", "/boot", "/dev", "/proc", "/sys", "/System", "/Library"}
}
