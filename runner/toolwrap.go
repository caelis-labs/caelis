package runner

import (
	"encoding/json"
	"fmt"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

// ─── Policy wrapper ──────────────────────────────────────────────────

// policyWrappedTool wraps a tool with policy evaluation. It is the
// outermost decorator in the tool chain.
type policyWrappedTool struct {
	inner    tool.Tool
	engine   policy.Engine
	approver agent.ApprovalRequester
	session  sessionSnapshot
}

type sessionSnapshot struct {
	ref   string
	state map[string]string
}

func (w *policyWrappedTool) Definition() tool.Definition {
	return w.inner.Definition()
}

func (w *policyWrappedTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	if w.engine == nil {
		return w.inner.Run(ctx, call)
	}

	// Evaluate policy.
	meta := map[string]any{}
	if tc, ok := ctx.(*toolContext); ok && tc.workspaceRoot != "" {
		meta["workspace_root"] = tc.workspaceRoot
	}
	decision, err := w.engine.Evaluate(ctx, policy.Request{
		ToolName:     call.Name,
		ToolArgs:     call.Args,
		AgentName:    ctx.AgentName(),
		SessionID:    ctx.SessionRef(),
		InvocationID: ctx.InvocationID(),
		SandboxPerm:  sandboxPermFromArgs(call.Args),
		Metadata:     meta,
	})
	if err != nil {
		return tool.Result{
			Output:  fmt.Sprintf("policy evaluation error: %v", err),
			IsError: true,
		}, nil
	}

	switch decision.Outcome {
	case policy.OutcomeAllow:
		// Inject policy constraints into tool context and call metadata.
		if tc, ok := ctx.(*toolContext); ok && decision.Constraints != nil {
			tc.SetConstraints(decision.Constraints)
		}
		enriched := call.WithMetadata("policy_decision", decision)
		return w.inner.Run(ctx, enriched)

	case policy.OutcomeApprovalNeeded:
		if w.approver == nil {
			return tool.Result{
				Output:  fmt.Sprintf("approval required but no approver configured: %s", decision.Reason),
				IsError: true,
			}, nil
		}
		// Request approval.
		resp, err := w.approver.RequestApproval(ctx, agent.ApprovalRequest{
			ToolName: call.Name,
			CallID:   call.CallID,
			Args:     call.Args,
			Reason:   decision.Reason,
			RunID:    ctx.InvocationID(),
		})
		if err != nil {
			return tool.Result{
				Output:  fmt.Sprintf("approval request error: %v", err),
				IsError: true,
			}, nil
		}
		if !resp.Approved {
			return denyResult(call.Name, "approval denied: "+resp.Reason), nil
		}
		// Approved — inject decision and constraints, then proceed.
		if tc, ok := ctx.(*toolContext); ok && decision.Constraints != nil {
			tc.SetConstraints(decision.Constraints)
		}
		enriched := call.WithMetadata("policy_decision", decision)
		return w.inner.Run(ctx, enriched)

	case policy.OutcomeDeny:
		return denyResult(call.Name, decision.Reason), nil

	default:
		return denyResult(call.Name, "unknown policy outcome"), nil
	}
}

func denyResult(toolName, reason string) tool.Result {
	msg := map[string]any{
		"error":      reason,
		"error_code": "permission_denied",
		"tool_name":  toolName,
	}
	data, _ := json.Marshal(msg)
	return tool.Result{
		Output:  string(data),
		IsError: true,
	}
}

// sandboxPermFromArgs extracts and validates sandbox_permissions from tool args.
func sandboxPermFromArgs(args map[string]any) policy.SandboxPermission {
	if args == nil {
		return policy.SandboxPermDefault
	}
	v, ok := args["sandbox_permissions"].(string)
	if !ok || v == "" {
		return policy.SandboxPermDefault
	}
	perm := policy.SandboxPermission(v)
	switch perm {
	case policy.SandboxPermDefault, policy.SandboxPermRequireEscalated:
		return perm
	default:
		// Unknown values are rejected — normalize to default.
		return policy.SandboxPermDefault
	}
}

// ─── Observer wrapper ────────────────────────────────────────────────

// observerWrappedTool wraps a tool to notify an observer before and
// after execution. Observer events are transient (UI-only).
type observerWrappedTool struct {
	inner    tool.Tool
	observer tool.Observer
}

func (w *observerWrappedTool) Definition() tool.Definition {
	return w.inner.Definition()
}

func (w *observerWrappedTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	if w.observer != nil {
		w.observer.BeforeTool(call)
	}
	result, err := w.inner.Run(ctx, call)
	if w.observer != nil {
		w.observer.AfterTool(call, result, err)
	}
	return result, err
}

// ─── Truncation wrapper ──────────────────────────────────────────────

// truncationWrappedTool wraps a tool to truncate large results.
type truncationWrappedTool struct {
	inner  tool.Tool
	policy tool.TruncationPolicy
}

func (w *truncationWrappedTool) Definition() tool.Definition {
	return w.inner.Definition()
}

func (w *truncationWrappedTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	result, err := w.inner.Run(ctx, call)
	if err != nil {
		return result, err
	}
	truncated, info := tool.TruncateResult(result, w.policy)
	if info.Truncated {
		if truncated.Metadata == nil {
			truncated.Metadata = make(map[string]any)
		}
		truncated.Metadata["truncation_info"] = info.String()
	}
	return truncated, nil
}

// AugmentTools performs runner-owned tool augmentation:
//   - Wraps RUN_COMMAND to use TaskManager for async execution
//   - Injects TASK tool when RUN_COMMAND or SPAWN is present
//
// Called before WrapTools to prepare the raw tool list.
func AugmentTools(tools []tool.Tool, tm *TaskManager) []tool.Tool {
	hasRUN_COMMAND := false
	hasSPAWN := false
	hasTASK := false

	var result []tool.Tool
	for _, t := range tools {
		switch t.Definition().Name {
		case "RUN_COMMAND":
			hasRUN_COMMAND = true
			if tm != nil {
				result = append(result, &taskAwareShellTool{inner: t, manager: tm})
			} else {
				result = append(result, t)
			}
		case "SPAWN":
			hasSPAWN = true
			result = append(result, t)
		case "TASK":
			hasTASK = true
			result = append(result, t)
		default:
			result = append(result, t)
		}
	}

	// Auto-inject TASK if RUN_COMMAND or SPAWN present but TASK missing.
	if (hasRUN_COMMAND || hasSPAWN) && !hasTASK && tm != nil {
		result = append(result, newTaskTool(tm))
	}

	return result
}

// ─── Task-aware RUN_COMMAND wrapper ─────────────────────────────────

type taskAwareShellTool struct {
	inner   tool.Tool
	manager *TaskManager
}

func (t *taskAwareShellTool) Definition() tool.Definition {
	def := t.inner.Definition()
	if def.Metadata == nil {
		def.Metadata = make(map[string]any)
	}
	def.Metadata["task_aware"] = true
	return def
}

func (t *taskAwareShellTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	command, _ := call.Args["command"].(string)
	if command == "" {
		return tool.Result{Output: "command is required", IsError: true}, nil
	}
	workdir, _ := call.Args["workdir"].(string)

	// Read policy constraints from tool context.
	var constraints sandbox.Constraints
	if tc, ok := ctx.(interface {
		Constraints() *policy.SandboxConstraints
	}); ok {
		if c := tc.Constraints(); c != nil {
			constraints = toSandboxConstraints(c)
		}
	}

	req := sandbox.CommandRequest{
		Command:     command,
		Dir:         workdir,
		Timeout:     300,
		Constraints: constraints,
	}

	taskID, err := t.manager.StartCommand(ctx, req)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}, nil
	}

	// Wait for completion.
	snap, err := t.manager.Wait(ctx, taskID)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}, nil
	}

	var output string
	if snap.Output != "" {
		output = snap.Output
	}
	if snap.Error != "" {
		if output != "" {
			output += "\n--- stderr ---\n"
		}
		output += snap.Error
	}

	result := tool.Result{
		Output: output,
		Metadata: map[string]any{
			"task_id":   snap.TaskID,
			"exit_code": snap.ExitCode,
		},
	}
	if snap.State == TaskStateFailed {
		result.IsError = true
	}
	return result, nil
}

// ─── Runner-owned TASK tool ─────────────────────────────────────────

// taskTool implements the TASK tool backed by the runner's TaskManager.
type taskTool struct {
	manager *TaskManager
}

func newTaskTool(tm *TaskManager) tool.Tool {
	return &taskTool{manager: tm}
}

func (*taskTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "TASK",
		Description: "Control async tasks (wait, write, cancel).",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"action":  {Type: "string", Enum: []any{"wait", "write", "cancel"}},
				"task_id": {Type: "string"},
				"input":   {Type: "string"},
			},
			Required: []string{"action", "task_id"},
		},
	}
}

func (t *taskTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	action, _ := call.Args["action"].(string)
	taskID, _ := call.Args["task_id"].(string)
	if action == "" || taskID == "" {
		return tool.Result{Output: "action and task_id are required", IsError: true}, nil
	}

	switch action {
	case "wait":
		snap, err := t.manager.Wait(ctx, taskID)
		if err != nil {
			return tool.Result{Output: err.Error(), IsError: true}, nil
		}
		return tool.Result{
			Output: snap.Output,
			Metadata: map[string]any{
				"task_id": snap.TaskID, "state": snap.State,
				"exit_code": snap.ExitCode,
			},
			IsError: snap.State == TaskStateFailed,
		}, nil
	case "cancel":
		if err := t.manager.Cancel(ctx, taskID); err != nil {
			return tool.Result{Output: err.Error(), IsError: true}, nil
		}
		return tool.Result{Output: "cancelled"}, nil
	case "write":
		input, _ := call.Args["input"].(string)
		if err := t.manager.Write(ctx, taskID, input); err != nil {
			return tool.Result{Output: err.Error(), IsError: true}, nil
		}
		return tool.Result{Output: "ok"}, nil
	default:
		return tool.Result{Output: fmt.Sprintf("unknown action: %s", action), IsError: true}, nil
	}
}

// ─── Tool chain assembly ─────────────────────────────────────────────

// WrapTools applies the decorator chain to a list of tools.
// Order (outermost → innermost):
//  1. policy — authorization, constraint injection, approval
//  2. observer — transient event callbacks
//  3. truncation — result size limits
//  4. base tool — actual execution
func WrapTools(tools []tool.Tool, engine policy.Engine, approver agent.ApprovalRequester, observer tool.Observer) []tool.Tool {
	wrapped := make([]tool.Tool, len(tools))
	for i, t := range tools {
		var w tool.Tool = t
		// Innermost: truncation.
		w = &truncationWrappedTool{inner: w, policy: tool.DefaultTruncationPolicy()}
		// Middle: observer.
		if observer != nil {
			w = &observerWrappedTool{inner: w, observer: observer}
		}
		// Outermost: policy.
		if engine != nil {
			w = &policyWrappedTool{
				inner:    w,
				engine:   engine,
				approver: approver,
			}
		}
		wrapped[i] = w
	}
	return wrapped
}
