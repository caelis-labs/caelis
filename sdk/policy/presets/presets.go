package presets

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

const (
	ModeAutoReview = "auto-review"
	ModeManual     = "manual"

	// Deprecated compatibility aliases. Legacy sessions and configs collapse
	// to auto-review instead of preserving the old sandbox/approval modes.
	ModePlan       = ModeAutoReview
	ModeDefault    = ModeAutoReview
	ModeFullAccess = ModeAutoReview
)

func NewRegistry() (*sdkpolicy.MemoryRegistry, error) {
	return sdkpolicy.NewMemory(
		AutoReviewMode(),
		ManualMode(),
	)
}

func PlanMode() sdkpolicy.Mode {
	return AutoReviewMode()
}

func DefaultMode() sdkpolicy.Mode {
	return AutoReviewMode()
}

func FullAccessMode() sdkpolicy.Mode {
	return AutoReviewMode()
}

func AutoReviewMode() sdkpolicy.Mode {
	return sdkpolicy.NamedMode{
		ID: ModeAutoReview,
		Decide: func(_ context.Context, input sdkpolicy.ToolContext) (sdkpolicy.Decision, error) {
			def := baseStrictConstraints(input.Options)
			switch toolName(input) {
			case "PLAN", "SPAWN", "REQUEST_PERMISSIONS":
				return allow(def), nil
			case "READ", "SEARCH", "LIST", "GLOB":
				if err := ensureReadPathsWithinRoots(input); err != nil {
					return deny(err.Error()), nil
				}
				return allow(def), nil
			case "WRITE", "PATCH":
				if err := ensureWritePathsWithinRoots(input); err != nil {
					return deny(err.Error()), nil
				}
				return allow(def), nil
			case "TASK":
				return allow(def), nil
			case "BASH":
				if commandLooksDangerous(commandArg(input)) {
					return deny("dangerous command is blocked even in auto-review mode"), nil
				}
				req, err := parseBashSandboxRequest(input)
				if err != nil {
					return deny(err.Error()), nil
				}
				switch req.SandboxPermissions {
				case bashSandboxPermissionRequireEscalated:
					return askEscalationApproval(input, req), nil
				case bashSandboxPermissionWithAdditionalPermissions:
					reason := "additional sandbox permissions require user approval"
					decision := askApproval(reason, applyBashAdditionalPermissions(def, req), input)
					decision.Metadata = req.approvalMetadata(reason)
					return decision, nil
				}
				return allow(def), nil
			default:
				return deny(fmt.Sprintf("tool %q is not allowed in auto-review mode", input.Tool.Name)), nil
			}
		},
	}
}

func ManualMode() sdkpolicy.Mode {
	return sdkpolicy.NamedMode{
		ID: ModeManual,
		Decide: func(_ context.Context, input sdkpolicy.ToolContext) (sdkpolicy.Decision, error) {
			def := baseStrictConstraints(input.Options)
			switch toolName(input) {
			case "PLAN", "SPAWN", "REQUEST_PERMISSIONS":
				return allow(def), nil
			case "READ", "SEARCH", "LIST", "GLOB":
				if err := ensureReadPathsWithinRoots(input); err != nil {
					return deny(err.Error()), nil
				}
				return allow(def), nil
			case "WRITE", "PATCH":
				if err := ensureWritePathsWithinRoots(input); err != nil {
					return deny(err.Error()), nil
				}
				return allow(def), nil
			case "TASK":
				return allow(def), nil
			case "BASH":
				if commandLooksDangerous(commandArg(input)) {
					return deny("dangerous command is blocked even in manual mode"), nil
				}
				req, err := parseBashSandboxRequest(input)
				if err != nil {
					return deny(err.Error()), nil
				}
				switch req.SandboxPermissions {
				case bashSandboxPermissionRequireEscalated:
					return askEscalationApproval(input, req), nil
				case bashSandboxPermissionWithAdditionalPermissions:
					reason := "additional sandbox permissions require user approval"
					decision := askApproval(reason, applyBashAdditionalPermissions(def, req), input)
					decision.Metadata = req.approvalMetadata(reason)
					return decision, nil
				}
				return allow(def), nil
			default:
				return deny(fmt.Sprintf("tool %q is not allowed in manual mode", input.Tool.Name)), nil
			}
		},
	}
}

func allow(constraints sdksandbox.Constraints) sdkpolicy.Decision {
	return sdkpolicy.Decision{
		Action:      sdkpolicy.ActionAllow,
		Constraints: constraints,
	}
}

func deny(reason string) sdkpolicy.Decision {
	return sdkpolicy.Decision{
		Action: sdkpolicy.ActionDeny,
		Reason: strings.TrimSpace(reason),
	}
}

func askApproval(reason string, constraints sdksandbox.Constraints, input sdkpolicy.ToolContext) sdkpolicy.Decision {
	name := strings.TrimSpace(strings.ToUpper(input.Tool.Name))
	call := sdkpolicy.CallArgs(input.Call)
	return sdkpolicy.Decision{
		Action:      sdkpolicy.ActionAskApproval,
		Reason:      strings.TrimSpace(reason),
		Constraints: constraints,
		Approval: &sdksession.ProtocolApproval{
			ToolCall: sdksession.ProtocolToolCall{
				ID:       strings.TrimSpace(input.Call.ID),
				Name:     name,
				Kind:     toolKind(name),
				Title:    approvalTitle(name, call),
				Status:   "pending",
				RawInput: call,
			},
			Options: []sdksession.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	}
}

func askEscalationApproval(input sdkpolicy.ToolContext, req bashSandboxRequest) sdkpolicy.Decision {
	reason := "host execution requires user approval"
	decision := askApproval(reason, sdksandbox.Constraints{
		Route:      sdksandbox.RouteHost,
		Backend:    sdksandbox.BackendHost,
		Permission: sdksandbox.PermissionFullAccess,
		Isolation:  sdksandbox.IsolationHost,
		Network:    sdksandbox.NetworkInherit,
	}, input)
	decision.Metadata = req.approvalMetadata(reason)
	return decision
}

func toolKind(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return "read"
	case "WRITE", "PATCH":
		return "edit"
	case "SEARCH", "GLOB", "LIST":
		return "search"
	case "BASH", "TASK", "SPAWN":
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
	case "BASH":
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

func baseStrictConstraints(opts sdkpolicy.ModeOptions) sdksandbox.Constraints {
	rules := make([]sdksandbox.PathRule, 0, 2+len(opts.ExtraWriteRoots)+len(opts.ExtraReadRoots))
	appendRule := func(path string, access sdksandbox.PathAccess) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		rules = append(rules, sdksandbox.PathRule{Path: path, Access: access})
	}
	appendRule(opts.WorkspaceRoot, sdksandbox.PathAccessReadWrite)
	appendRule(opts.TempRoot, sdksandbox.PathAccessReadWrite)
	for _, path := range opts.ExtraWriteRoots {
		appendRule(path, sdksandbox.PathAccessReadWrite)
	}
	for _, path := range opts.ExtraReadRoots {
		appendRule(path, sdksandbox.PathAccessReadOnly)
	}
	return sdksandbox.Constraints{
		Route:      sdksandbox.RouteSandbox,
		Permission: sdksandbox.PermissionWorkspaceWrite,
		Isolation:  sdksandbox.IsolationContainer,
		Network:    sdksandbox.NetworkInherit,
		PathRules:  rules,
	}
}

func toolName(input sdkpolicy.ToolContext) string {
	return strings.ToUpper(strings.TrimSpace(input.Tool.Name))
}

func ensureMarkdownOnly(input sdkpolicy.ToolContext) error {
	for _, one := range candidatePaths(input) {
		switch strings.ToLower(filepath.Ext(one)) {
		case ".md", ".mdx", ".markdown":
			continue
		default:
			return fmt.Errorf("plan mode only allows markdown writes, got %q", one)
		}
	}
	return nil
}

func ensureReadPathsWithinRoots(input sdkpolicy.ToolContext) error {
	return ensurePathsWithinRoots(candidatePaths(input), readableRoots(input.Options), "read")
}

func ensureWritePathsWithinRoots(input sdkpolicy.ToolContext) error {
	return ensurePathsWithinRoots(candidatePaths(input), writableRoots(input.Options), "write")
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

func readableRoots(opts sdkpolicy.ModeOptions) []string {
	roots := make([]string, 0, 2+len(opts.ExtraReadRoots)+len(opts.ExtraWriteRoots))
	roots = appendNonEmpty(roots, opts.WorkspaceRoot, opts.TempRoot)
	roots = appendNonEmpty(roots, opts.ExtraWriteRoots...)
	roots = appendNonEmpty(roots, opts.ExtraReadRoots...)
	return roots
}

func writableRoots(opts sdkpolicy.ModeOptions) []string {
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

func candidatePaths(input sdkpolicy.ToolContext) []string {
	args := sdkpolicy.CallArgs(input.Call)
	name := toolName(input)
	switch name {
	case "READ", "WRITE", "PATCH", "LIST", "SEARCH":
		return resolvePathsAgainstWorkspace(stringValues(args["path"]), input.Options.WorkspaceRoot)
	case "GLOB":
		return globRoots(stringValues(args["pattern"]), input.Options.WorkspaceRoot)
	default:
		return nil
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

func commandArg(input sdkpolicy.ToolContext) string {
	args := sdkpolicy.CallArgs(input.Call)
	command, _ := args["command"].(string)
	return strings.TrimSpace(command)
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
	case strings.Contains(compact, "rm-rf/") || strings.Contains(compact, "rm-rf~") || strings.Contains(compact, "rm-rf$home"):
		return true
	case strings.Contains(compact, "gitreset--hard"):
		return true
	case strings.Contains(compact, "gitpush--force") || strings.Contains(compact, "gitpush-f"):
		return true
	}
	return false
}
