package presets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

const (
	ModeWorkspaceWrite = "workspace-write"

	// ModeAutoReview and ModeManual are legacy policy names kept for callers
	// that still import them. They normalize to ModeWorkspaceWrite.
	ModeAutoReview = "auto-review"
	ModeManual     = "manual"

	// ModeDefault is the built-in fallback policy for omitted, legacy, or
	// otherwise unresolved local policy configuration.
	ModeDefault = ModeWorkspaceWrite
)

func NormalizeModeName(mode string) string {
	trimmed := strings.TrimSpace(mode)
	switch strings.ToLower(trimmed) {
	case "", "workspace-write", "workspace_write", "workspacewrite":
		return ModeDefault
	case "auto", "auto-review", "auto_review", "autoreview", "default", "plan", "full_control", "full_access":
		return ModeDefault
	case "manual":
		return ModeDefault
	default:
		return trimmed
	}
}

func NewRegistry() (*policy.MemoryRegistry, error) {
	return policy.NewMemory(
		WorkspaceWriteMode(),
	)
}

func WorkspaceWriteMode() policy.Mode {
	return policy.NamedMode{
		ID: ModeWorkspaceWrite,
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			def := baseStrictConstraints(input.Options)
			switch toolName(input) {
			case "PLAN", "SPAWN":
				return allow(def), nil
			case "READ", "SEARCH", "LIST", "GLOB":
				return allow(filesystemReadConstraints(def)), nil
			case "WRITE", "PATCH":
				if err := ensureWritePathsWithinRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
				return allow(def), nil
			case "TASK":
				return allow(def), nil
			case "RUN_COMMAND":
				return decideCommand(input, def, "workspace-write policy")
			default:
				return deny(fmt.Sprintf("tool %q is not allowed in workspace-write policy", input.Tool.Name)), nil
			}
		},
	}
}

func AutoReviewMode() policy.Mode {
	return WorkspaceWriteMode()
}

func ManualMode() policy.Mode {
	return WorkspaceWriteMode()
}

func decideCommand(input policy.ToolContext, def sandbox.Constraints, modeName string) (policy.Decision, error) {
	command, err := commandArg(input)
	if err != nil {
		return policy.Decision{}, err
	}
	if commandLooksDangerous(command) {
		return deny("dangerous command is blocked even in " + strings.TrimSpace(modeName)), nil
	}
	req, err := parseCommandSandboxRequest(input)
	if err != nil {
		return deny(err.Error()), nil
	}
	if commandRequiresDestructiveApproval(command) {
		return askDestructiveCommandApproval(input, def, req)
	}
	switch req.SandboxPermissions {
	case commandSandboxPermissionRequireEscalated:
		return askEscalationApproval(input, req)
	}
	return allow(def), nil
}

func allow(constraints sandbox.Constraints) policy.Decision {
	return policy.Decision{
		Action:      policy.ActionAllow,
		Constraints: constraints,
	}
}

func deny(reason string) policy.Decision {
	return policy.Decision{
		Action: policy.ActionDeny,
		Reason: strings.TrimSpace(reason),
	}
}

func policyErrorOrDeny(err error) (policy.Decision, error) {
	var decodeErr *policy.ToolInputDecodeError
	if errors.As(err, &decodeErr) {
		return policy.Decision{}, err
	}
	return deny(err.Error()), nil
}

func askApproval(reason string, constraints sandbox.Constraints, input policy.ToolContext) (policy.Decision, error) {
	name := strings.TrimSpace(strings.ToUpper(input.Tool.Name))
	call, err := policy.CallArgs(input.Call)
	if err != nil {
		return policy.Decision{}, err
	}
	return policy.Decision{
		Action:      policy.ActionAskApproval,
		Reason:      strings.TrimSpace(reason),
		Constraints: constraints,
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       strings.TrimSpace(input.Call.ID),
				Name:     name,
				Kind:     toolKind(name),
				Title:    approvalTitle(name, call),
				Status:   "pending",
				RawInput: call,
			},
			Options: []session.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	}, nil
}

func askEscalationApproval(input policy.ToolContext, req commandSandboxRequest) (policy.Decision, error) {
	reason := "host execution requires approval"
	decision, err := askApproval(reason, hostExecutionConstraints(), input)
	if err != nil {
		return policy.Decision{}, err
	}
	decision.Metadata = req.approvalMetadata(reason)
	return decision, nil
}

func askDestructiveCommandApproval(input policy.ToolContext, def sandbox.Constraints, req commandSandboxRequest) (policy.Decision, error) {
	reason := "destructive filesystem command requires approval"
	constraints := def
	metadata := req.approvalMetadata(reason)
	metadata["destructive_command"] = true
	switch req.SandboxPermissions {
	case commandSandboxPermissionRequireEscalated:
		reason = "destructive host command requires approval"
		constraints = hostExecutionConstraints()
		metadata = req.approvalMetadata(reason)
		metadata["destructive_command"] = true
	}
	decision, err := askApproval(reason, constraints, input)
	if err != nil {
		return policy.Decision{}, err
	}
	decision.Metadata = metadata
	return decision, nil
}

func hostExecutionConstraints() sandbox.Constraints {
	return sandbox.Constraints{
		Route:      sandbox.RouteHost,
		Backend:    sandbox.BackendHost,
		Permission: sandbox.PermissionFullAccess,
		Isolation:  sandbox.IsolationHost,
		Network:    sandbox.NetworkInherit,
	}
}

func toolKind(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return "read"
	case "WRITE", "PATCH":
		return "edit"
	case "SEARCH", "GLOB", "LIST":
		return "search"
	case "RUN_COMMAND", "TASK", "SPAWN":
		return "execute"
	default:
		return "other"
	}
}

func approvalTitle(name string, call map[string]any) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ", "WRITE", "PATCH", "SEARCH", "LIST", "GLOB":
		if path, _ := call["path"].(string); strings.TrimSpace(path) != "" {
			return strings.TrimSpace(name + " " + path)
		}
	case "RUN_COMMAND":
		if command, _ := call["command"].(string); strings.TrimSpace(command) != "" {
			return strings.TrimSpace(name + " " + command)
		}
	case "TASK":
		if action, _ := call["action"].(string); strings.TrimSpace(action) != "" {
			if taskID, _ := call["task_id"].(string); strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(name + " " + action + " " + taskID)
			}
			return strings.TrimSpace(name + " " + action)
		}
	case "SPAWN":
		if agent, _ := call["agent"].(string); strings.TrimSpace(agent) != "" {
			return strings.TrimSpace(name + " " + agent)
		}
	}
	return name
}

func baseStrictConstraints(opts policy.ModeOptions) sandbox.Constraints {
	devWriteRoots := defaultDeveloperWritableRoots()
	rules := make([]sandbox.PathRule, 0, 2+len(devWriteRoots)+len(opts.ExtraWriteRoots)+len(opts.ExtraReadRoots))
	appendRule := func(path string, access sandbox.PathAccess) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		rules = append(rules, sandbox.PathRule{Path: path, Access: access})
	}
	appendRule(opts.WorkspaceRoot, sandbox.PathAccessReadWrite)
	if runtime.GOOS != "windows" {
		appendRule(opts.TempRoot, sandbox.PathAccessReadWrite)
	}
	for _, path := range devWriteRoots {
		appendRule(path, sandbox.PathAccessReadWrite)
	}
	for _, path := range opts.ExtraWriteRoots {
		appendRule(path, sandbox.PathAccessReadWrite)
	}
	for _, path := range opts.ExtraReadRoots {
		appendRule(path, sandbox.PathAccessReadOnly)
	}
	return sandbox.Constraints{
		Route:      sandbox.RouteSandbox,
		Permission: sandbox.PermissionWorkspaceWrite,
		Isolation:  sandbox.IsolationContainer,
		Network:    defaultNetworkPolicy(opts),
		PathRules:  rules,
	}
}

func defaultNetworkPolicy(opts policy.ModeOptions) sandbox.Network {
	if opts.NetworkEnabled != nil && !*opts.NetworkEnabled {
		return sandbox.NetworkDisabled
	}
	return sandbox.NetworkEnabled
}

func filesystemReadConstraints(in sandbox.Constraints) sandbox.Constraints {
	if len(in.PathRules) == 0 {
		return in
	}
	rules := make([]sandbox.PathRule, 0, len(in.PathRules))
	for _, rule := range in.PathRules {
		if rule.Access == sandbox.PathAccessReadOnly {
			continue
		}
		rules = append(rules, rule)
	}
	in.PathRules = rules
	return in
}

func toolName(input policy.ToolContext) string {
	return strings.ToUpper(strings.TrimSpace(input.Tool.Name))
}

func ensureWritePathsWithinRoots(input policy.ToolContext) error {
	paths, err := candidatePaths(input)
	if err != nil {
		return err
	}
	if err := ensurePathsOutsideDefaultHiddenRoots(paths, approvedOverrideRoots(input.Options), "write"); err != nil {
		return err
	}
	return ensurePathsWithinRoots(paths, writableRoots(input.Options), "write")
}

func ensurePathsWithinRoots(paths []string, roots []string, action string) error {
	for _, one := range paths {
		if strings.TrimSpace(one) == "" {
			continue
		}
		if !withinAnyRoot(one, roots) {
			return fmt.Errorf("%s target %q is outside allowed roots", action, one)
		}
	}
	return nil
}

func writableRoots(opts policy.ModeOptions) []string {
	roots := make([]string, 0, 2+len(opts.ExtraWriteRoots))
	roots = appendNonEmpty(roots, opts.WorkspaceRoot, opts.TempRoot)
	roots = appendNonEmpty(roots, opts.ExtraWriteRoots...)
	return roots
}

func appendNonEmpty(dst []string, values ...string) []string {
	for _, one := range values {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			dst = append(dst, filepath.Clean(trimmed))
		}
	}
	return dst
}

func approvedOverrideRoots(opts policy.ModeOptions) []string {
	roots := make([]string, 0, len(opts.ExtraReadRoots)+len(opts.ExtraWriteRoots))
	roots = appendNonEmpty(roots, opts.ExtraWriteRoots...)
	roots = appendNonEmpty(roots, opts.ExtraReadRoots...)
	return roots
}

func ensurePathsOutsideDefaultHiddenRoots(paths []string, approvedRoots []string, action string) error {
	for _, one := range paths {
		target := normalizeTarget(one)
		if target == "" {
			continue
		}
		if withinAnyRoot(target, approvedRoots) {
			continue
		}
		if withinAnyRoot(target, defaultHiddenUserRoots()) {
			return fmt.Errorf("%s target %q is under a sensitive user configuration path; rerun the specific operation with sandbox_permissions=require_escalated if it is required", action, one)
		}
	}
	return nil
}

func defaultHiddenUserRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, ".config", "gcloud"),
	}
}

func withinAnyRoot(target string, roots []string) bool {
	target = normalizeTarget(target)
	if target == "" {
		return true
	}
	for _, root := range roots {
		root = normalizeTarget(root)
		if root == "" {
			continue
		}
		if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func normalizeTarget(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(string(filepath.Separator), value)
	}
	return filepath.Clean(value)
}

func candidatePaths(input policy.ToolContext) ([]string, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return nil, err
	}
	name := toolName(input)
	switch name {
	case "READ", "WRITE", "PATCH", "LIST", "SEARCH":
		return resolvePathsAgainstWorkspace(stringValues(args["path"]), input.Options.WorkspaceRoot), nil
	case "GLOB":
		return globRoots(stringValues(args["pattern"]), input.Options.WorkspaceRoot), nil
	default:
		return nil, nil
	}
}

func resolvePathsAgainstWorkspace(paths []string, workspaceRoot string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if resolved := resolvePolicyPath(path, workspaceRoot); resolved != "" {
			out = append(out, resolved)
		}
	}
	return out
}

func resolvePolicyPath(value string, workspaceRoot string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	base := strings.TrimSpace(workspaceRoot)
	if base == "" {
		base = string(filepath.Separator)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func globRoots(patterns []string, workspaceRoot string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		pattern = resolvePolicyPath(pattern, workspaceRoot)
		root := pattern
		for i, r := range pattern {
			if r == '*' || r == '?' || r == '[' {
				root = pattern[:i]
				break
			}
		}
		root = strings.TrimRight(root, string(filepath.Separator))
		if root == "" {
			root = string(filepath.Separator)
		}
		out = append(out, root)
	}
	return out
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, stringValues(item)...)
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	return nil
}

func commandArg(input policy.ToolContext) (string, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return "", err
	}
	command, _ := args["command"].(string)
	return strings.TrimSpace(command), nil
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
