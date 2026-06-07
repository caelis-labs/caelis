// Package presets provides built-in policy profiles.
package presets

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/sandbox"
)

// WorkspaceWrite is a policy profile that allows all tool calls
// with workspace-write safety constraints.
type WorkspaceWrite struct{}

func (p *WorkspaceWrite) Name() string { return "workspace-write" }

func (p *WorkspaceWrite) Evaluate(_ context.Context, req policy.Request) (policy.Decision, error) {
	switch req.ToolName {
	case "READ", "SEARCH", "LIST", "GLOB":
		return p.evaluateFileRead(req)
	case "WRITE", "PATCH":
		return p.evaluateFileWrite(req)
	case "RUN_COMMAND":
		return p.evaluateCommand(req)
	case "PLAN", "SPAWN", "TASK":
		return policy.Decision{Outcome: policy.OutcomeAllow}, nil
	default:
		return policy.Decision{
			Outcome: policy.OutcomeDeny,
			Reason:  fmt.Sprintf("tool %q is not allowed in workspace-write policy", req.ToolName),
		}, nil
	}
}

func (p *WorkspaceWrite) evaluateFileRead(req policy.Request) (policy.Decision, error) {
	path := extractPath(req.ToolArgs)
	if path != "" && IsSensitivePath(path) {
		return policy.Decision{
			Outcome: policy.OutcomeApprovalNeeded,
			Reason:  fmt.Sprintf("target %q is under a sensitive user configuration path", path),
		}, nil
	}
	return policy.Decision{
		Outcome: policy.OutcomeAllow,
		Constraints: &policy.SandboxConstraints{
			Permission: policy.SandboxPermDefault,
			Paths:      []policy.PathRule{{Path: path, Access: sandbox.PathAccessRead}},
		},
	}, nil
}

func (p *WorkspaceWrite) evaluateFileWrite(req policy.Request) (policy.Decision, error) {
	path := extractPath(req.ToolArgs)
	if path != "" && IsSensitivePath(path) {
		return policy.Decision{
			Outcome: policy.OutcomeDeny,
			Reason:  fmt.Sprintf("write to sensitive path %q is blocked", path),
		}, nil
	}
	if path != "" && req.Metadata != nil {
		if root, ok := req.Metadata["workspace_root"].(string); ok && root != "" {
			if !isWithinRoot(path, root) {
				return policy.Decision{
					Outcome: policy.OutcomeDeny,
					Reason:  fmt.Sprintf("write target %q is outside workspace root %q", path, root),
				}, nil
			}
		}
	}
	return policy.Decision{
		Outcome: policy.OutcomeAllow,
		Constraints: &policy.SandboxConstraints{
			Permission: policy.SandboxPermDefault,
			Paths:      []policy.PathRule{{Path: path, Access: sandbox.PathAccessWrite}},
		},
	}, nil
}

func (p *WorkspaceWrite) evaluateCommand(req policy.Request) (policy.Decision, error) {
	command := extractCommand(req.ToolArgs)
	if command == "" {
		return policy.Decision{
			Outcome: policy.OutcomeDeny,
			Reason:  "command is required",
		}, nil
	}

	if reason := CommandDenyReason(command); reason != "" {
		return policy.Decision{
			Outcome: policy.OutcomeDeny,
			Reason:  reason,
		}, nil
	}

	if GitControlMetadata(command) {
		if req.SandboxPerm != policy.SandboxPermRequireEscalated {
			return policy.Decision{
				Outcome: policy.OutcomeDeny,
				Reason:  "git control metadata command requires sandbox_permissions=require_escalated",
			}, nil
		}
		return policy.Decision{
			Outcome: policy.OutcomeApprovalNeeded,
			Reason:  "escalated sandbox permission requires approval",
		}, nil
	}

	if req.SandboxPerm == policy.SandboxPermRequireEscalated {
		return policy.Decision{
			Outcome: policy.OutcomeApprovalNeeded,
			Reason:  "escalated sandbox permission requires approval",
		}, nil
	}

	return policy.Decision{
		Outcome: policy.OutcomeAllow,
		Constraints: &policy.SandboxConstraints{
			Permission: policy.SandboxPermDefault,
		},
	}, nil
}

// ReadOnly is a policy profile that denies all write operations.
type ReadOnly struct{}

func (p *ReadOnly) Name() string { return "read-only" }

func (p *ReadOnly) Evaluate(_ context.Context, req policy.Request) (policy.Decision, error) {
	switch req.ToolName {
	case "WRITE", "PATCH", "RUN_COMMAND", "SPAWN":
		return policy.Decision{
			Outcome: policy.OutcomeDeny,
			Reason:  "read-only policy denies " + req.ToolName,
		}, nil
	default:
		return policy.Decision{Outcome: policy.OutcomeAllow}, nil
	}
}

// AutoApprove is a policy profile that approves everything.
type AutoApprove struct{}

func (p *AutoApprove) Name() string { return "auto-approve" }

func (p *AutoApprove) Evaluate(_ context.Context, _ policy.Request) (policy.Decision, error) {
	return policy.Decision{Outcome: policy.OutcomeAllow}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────

func extractPath(args map[string]any) string {
	if args == nil {
		return ""
	}
	if p, ok := args["path"].(string); ok {
		return p
	}
	if p, ok := args["root"].(string); ok {
		return p
	}
	return ""
}

func extractCommand(args map[string]any) string {
	if args == nil {
		return ""
	}
	if c, ok := args["command"].(string); ok {
		return strings.TrimSpace(c)
	}
	return ""
}

func isWithinRoot(path, root string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	return strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) || absPath == absRoot
}

// Compile-time interface checks.
var (
	_ policy.Profile = (*WorkspaceWrite)(nil)
	_ policy.Profile = (*ReadOnly)(nil)
	_ policy.Profile = (*AutoApprove)(nil)
)
