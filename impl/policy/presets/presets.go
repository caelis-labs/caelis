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
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const (
	ModeWorkspaceWrite = policy.ProfileWorkspaceWrite

	// ModeAutoReview and ModeManual are legacy policy names kept for callers
	// that still import them. They normalize to ModeWorkspaceWrite.
	ModeAutoReview = "auto-review"
	ModeManual     = "manual"

	// ModeDefault is the built-in fallback policy for omitted, legacy, or
	// otherwise unresolved local policy configuration.
	ModeDefault = ModeWorkspaceWrite
)

func NormalizeModeName(mode string) string {
	normalized := policy.NormalizeProfileName(mode)
	if strings.TrimSpace(normalized) == "" {
		return ModeDefault
	}
	return normalized
}

func NewRegistry() (*policy.MemoryRegistry, error) {
	return policy.NewMemory(
		WorkspaceWriteMode(),
		workspaceWriteAliasMode(ModeAutoReview),
		workspaceWriteAliasMode(ModeManual),
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
				if err := ensureReadPathsOutsideDefaultHiddenRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
				return allow(filesystemReadToolConstraints(def)), nil
			case "WRITE", "PATCH":
				if err := ensureWritePathsWithinRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
				return allow(def), nil
			case "TASK":
				return allow(def), nil
			case "RUN_COMMAND":
				return decideCommand(input, def, "workspace-write policy")
			case "WEB_SEARCH", "WEB_FETCH":
				return allow(def), nil
			default:
				if isMCPTool(input.Tool) || tool.IsToolSearchDefinition(input.Tool) {
					return allow(def), nil
				}
				return deny("tool is not allowed by workspace-write policy"), nil
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

func workspaceWriteAliasMode(id string) policy.Mode {
	base := WorkspaceWriteMode()
	return policy.NamedMode{
		ID:     strings.TrimSpace(id),
		Decide: base.DecideTool,
	}
}

func decideCommand(input policy.ToolContext, def sandbox.Constraints, modeName string) (policy.Decision, error) {
	command, err := commandArg(input)
	if err != nil {
		return policy.Decision{}, err
	}
	req, err := parseCommandSandboxRequest(input)
	if err != nil {
		return deny(err.Error()), nil
	}
	if reason := commandHardDenyReason(command); reason != "" {
		return deny(reason), nil
	}
	if gitControlMetadataCommand(command) && req.SandboxPermissions != commandSandboxPermissionRequireEscalated {
		return deny("Git control metadata command requires sandbox_permissions=require_escalated"), nil
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

func isMCPTool(def tool.Definition) bool {
	if len(def.Metadata) == 0 {
		return false
	}
	kind, _ := def.Metadata[tool.MetadataToolKind].(string)
	return strings.EqualFold(strings.TrimSpace(kind), tool.MetadataToolKindMCP)
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

func filesystemReadToolConstraints(in sandbox.Constraints) sandbox.Constraints {
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

func ensureReadPathsOutsideDefaultHiddenRoots(input policy.ToolContext) error {
	paths, err := candidatePaths(input)
	if err != nil {
		return err
	}
	return ensurePathsOutsideDefaultHiddenRoots(paths, approvedOverrideRoots(input.Options), "read")
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

func commandHardDenyReason(command string) string {
	compact := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(command)), ""))
	if compact == "" {
		return ""
	}
	switch {
	case strings.Contains(compact, ":(){"):
		return "dangerous shell command is blocked"
	case strings.Contains(compact, "yes>/dev/null"):
		return "dangerous shell command is blocked"
	case strings.Contains(compact, "/dev/tcp/"):
		return "dangerous network shell command is blocked"
	case strings.Contains(compact, "curl") && (strings.Contains(compact, "|bash") || strings.Contains(compact, "|sh")):
		return "remote script execution is blocked"
	case strings.Contains(compact, "wget") && (strings.Contains(compact, "|bash") || strings.Contains(compact, "|sh")):
		return "remote script execution is blocked"
	case commandContainsRecursiveDelete(command):
		return "recursive filesystem delete command is blocked"
	}
	if reason := hardDenyGitCommandReason(command); reason != "" {
		return reason
	}
	for _, payload := range shellCommandPayloads(command) {
		if reason := commandHardDenyReason(payload); reason != "" {
			return reason
		}
	}
	return ""
}

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

func hardDenyGitCommandReason(command string) string {
	for _, git := range gitCommands(command) {
		switch git.Subcommand {
		case "clean":
			if !gitCleanDryRun(git.Args) {
				return "git clean without dry-run is blocked"
			}
		case "reset":
			if hasGitFlag(git.Args, "--hard", "") {
				return "git reset --hard is blocked"
			}
		case "checkout":
			if gitCheckoutDiscardsWorktree(git.Args) {
				return "git checkout path restore is blocked"
			}
		case "restore":
			if gitRestoreDiscardsWorktree(git.Args) {
				return "git worktree restore is blocked"
			}
		case "push":
			if gitPushForce(git.Args) {
				return "forced git push is blocked"
			}
		}
	}
	return ""
}

func gitControlMetadataCommand(command string) bool {
	for _, git := range gitCommands(command) {
		switch git.Subcommand {
		case "add", "commit", "tag", "merge", "rebase", "cherry-pick", "stash", "push", "reset", "checkout", "restore":
			return true
		case "clean":
			if !gitCleanDryRun(git.Args) {
				return true
			}
		}
	}
	for _, payload := range shellCommandPayloads(command) {
		if gitControlMetadataCommand(payload) {
			return true
		}
	}
	return false
}

type gitCommand struct {
	Subcommand string
	Args       []string
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

func gitCleanDryRun(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(trimShellToken(arg))
		switch lower {
		case "-n", "--dry-run":
			return true
		}
		if strings.HasPrefix(lower, "-") && !strings.HasPrefix(lower, "--") && strings.Contains(lower, "n") {
			return true
		}
	}
	return false
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
	for _, arg := range args {
		lower := strings.ToLower(trimShellToken(arg))
		if lower == "--force" || strings.HasPrefix(lower, "--force-with-lease") || strings.HasPrefix(lower, "--force-if-includes") {
			return true
		}
		if strings.HasPrefix(lower, "-") && !strings.HasPrefix(lower, "--") && strings.Contains(lower, "f") {
			return true
		}
	}
	return false
}

func hasGitFlag(args []string, long string, short string) bool {
	for _, arg := range args {
		lower := strings.ToLower(trimShellToken(arg))
		if long != "" && lower == long {
			return true
		}
		if short != "" && strings.HasPrefix(lower, "-") && !strings.HasPrefix(lower, "--") && strings.Contains(lower, short) {
			return true
		}
	}
	return false
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
