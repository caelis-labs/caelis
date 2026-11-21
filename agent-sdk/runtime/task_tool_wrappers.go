package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/sandboxmeta"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/commanddiag"
)

func (r *Runtime) wrapToolsForRuntime(activeSession session.Session, ref session.SessionRef, spec agent.AgentSpec, toolCtx runtimeToolContext) []tool.Tool {
	out := make([]tool.Tool, 0, len(spec.Tools)+2)
	hasCommand := false
	hasTask := false
	hasCodeMode := false
	for _, one := range spec.Tools {
		if one == nil {
			continue
		}
		if declaration, ok := one.(*codemode.Tool); ok && declaration.Name == codemode.ToolName {
			hasCodeMode = true
		}
		switch one.(type) {
		case *shell.RunCommandTool:
			hasCommand = true
			if runtime, ok := sandboxRuntimeFromTool(one); ok {
				r.tasks.registerSandboxRuntime(runtime)
			}
			out = append(out, runtimeCommandTool{
				base:       one,
				session:    session.CloneSession(activeSession),
				sessionRef: session.NormalizeSessionRef(ref),
				tasks:      r.tasks,
			})
		case spawn.Tool, *spawn.Tool:
			resolver, _ := one.(spawn.Resolver)
			out = append(out, runtimeSpawnTool{
				runtime:      r,
				base:         one,
				resolver:     resolver,
				session:      session.CloneSession(activeSession),
				sessionRef:   session.NormalizeSessionRef(ref),
				tasks:        r.tasks,
				runner:       r.subagents,
				mode:         strings.TrimSpace(toolCtx.mode),
				approvalMode: strings.TrimSpace(toolCtx.approvalMode),
				approval:     toolCtx.approvalRequester,
			})
		case tasktool.Tool, *tasktool.Tool:
			hasTask = true
			out = append(out, runtimeTaskTool{
				base:       one,
				sessionRef: session.NormalizeSessionRef(ref),
				tasks:      r.tasks,
			})
		case sendmessage.Tool, *sendmessage.Tool:
			out = append(out, runtimeSendMessageTool{
				base: one, runtime: r, session: session.CloneSession(activeSession),
				sessionRef: session.NormalizeSessionRef(ref), external: toolCtx.inputSender,
			})
		default:
			out = append(out, one)
		}
	}
	if (hasCommand || hasCodeMode) && !hasTask {
		out = append(out, runtimeTaskTool{
			base:       tasktool.New(),
			sessionRef: session.NormalizeSessionRef(ref),
			tasks:      r.tasks,
		})
	}

	return out
}

type runtimeSendMessageTool struct {
	base       tool.Tool
	runtime    *Runtime
	session    session.Session
	sessionRef session.SessionRef
	external   agent.AgentInputSender
}

func (t runtimeSendMessageTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}

func (t runtimeSendMessageTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	args, err := decodeJSONMap(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := sendmessage.ValidateArgs(args); err != nil {
		return tool.Result{}, err
	}
	to, _ := stringArg(args, "to")
	text, _ := stringArg(args, "message")
	input := agent.CloneAgentInput(agent.AgentInput{Target: to, Input: text})
	localTarget := sessionHasSubagentTarget(t.session, input.Target)
	switch {
	case strings.EqualFold(input.Target, agent.AgentInputParent):
		if t.external == nil {
			return tool.Result{}, fmt.Errorf("the main Agent has no parent target")
		}
		err = t.external.SendAgentInput(ctx, input)
	case localTarget:
		if t.runtime == nil {
			return tool.Result{}, fmt.Errorf("target Agent messaging is unavailable")
		}
		_, err = t.runtime.SubmitChildInput(ctx, t.sessionRef, agent.ChildInputCommand{
			Target: input.Target, Source: session.ControllerExecutor(t.session.Controller),
			Input: input.Input, DisplayInput: input.DisplayInput, ContentParts: input.ContentParts,
		})
	case t.external != nil:
		err = t.external.SendAgentInput(ctx, input)
	default:
		if t.runtime == nil {
			return tool.Result{}, fmt.Errorf("target Agent messaging is unavailable")
		}
		_, err = t.runtime.SubmitChildInput(ctx, t.sessionRef, agent.ChildInputCommand{
			Target: input.Target, Source: session.ControllerExecutor(t.session.Controller),
			Input: input.Input, DisplayInput: input.DisplayInput, ContentParts: input.ContentParts,
		})
	}
	if err != nil {
		return tool.Result{}, err
	}
	return sendMessageToolResult(ctx, input.Target), nil
}

func sendMessageToolResult(ctx context.Context, target string) tool.Result {
	toolbinding.Observed(ctx).MessageTo = strings.TrimSpace(target)
	return tool.Result{Content: []tool.ContentBlock{tool.Text("Message sent.")}}
}

func sessionHasSubagentTarget(activeSession session.Session, target string) bool {
	targetHandle := taskapi.NormalizeHandle(target)
	targetID := strings.TrimSpace(target)
	if targetHandle == "" {
		return false
	}
	for _, participant := range activeSession.Participants {
		if participant.Kind != session.ParticipantKindSubagent {
			continue
		}
		if taskapi.NormalizeHandle(participant.Label) == targetHandle ||
			(targetID != "" && strings.EqualFold(strings.TrimSpace(participant.DelegationID), targetID)) {
			return true
		}
	}
	return false
}

func (tm *taskRuntime) registerSandboxRuntime(runtime sandbox.Runtime) {
	if tm == nil || runtime == nil {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if provider, ok := runtime.(sandboxRuntimeBackends); ok && provider != nil {
		for _, backend := range provider.SupportedBackends() {
			if backend == "" {
				continue
			}
			tm.backends[backend] = runtime
		}
	}
	desc := runtime.Describe()
	backend := desc.Backend
	if backend == "" {
		backend = sandbox.BackendHost
	}
	tm.backends[backend] = runtime
}

type runtimeCommandTool struct {
	base       tool.Tool
	session    session.Session
	sessionRef session.SessionRef
	tasks      *taskRuntime
}

func (runtimeCommandTool) RuntimeTaskResultSource(toolbinding.Token) bool { return true }

func (t runtimeCommandTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}

func (t runtimeCommandTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	runtime, ok := sandboxRuntimeFromTool(t.base)
	if !ok || runtime == nil {
		return t.base.Call(ctx, call)
	}
	req, err := t.commandRequest(ctx, call, runtime)
	if err != nil {
		return tool.Result{}, err
	}
	command := req.Command
	_, approved := ctx.Value(taskStartPermitKey{}).(taskStartPermit)
	if approved {
		req.Yield = 0
	} else if started, ok := ctx.Value(taskInvocationStartKey{}).(taskInvocationStart); ok {
		req.Yield = max(time.Until(started.time.Add(req.Yield)), 0)
	}
	snapshot, err := t.tasks.StartCommand(ctx, t.session, t.sessionRef, runtime, req)
	if err != nil {
		if strings.TrimSpace(snapshot.Ref.TaskID) != "" {
			payload := taskToolPayload(snapshot)
			if diag, ok := commanddiag.Best(commanddiag.Input{
				ToolName: shell.RunCommandToolName, Command: command,
				Error: strings.TrimSpace(err.Error()), ExitCode: 1,
			}); ok {
				if hint := strings.TrimSpace(diag.Hint); hint != "" {
					payload["system_hint"] = hint
				}
			}
			result, _ := taskSnapshotToolResultWithPayload(ctx, snapshot, payload)
			if approved && !isCommandToolError(err) {
				// The approval continuation must see claim or persistence errors
				// even when StartCommand could also return a Task snapshot.
				return result, err
			}
			return result, tool.FailedReply(err.Error())
		}
		if approved {
			return tool.Result{}, err
		}
		if result, ok := commandStartDiagnosticToolResult(t.base.Definition().Name, command, err); ok {
			return result, tool.FailedReply(err.Error())
		}
		return tool.Result{}, err
	}
	return taskSnapshotToolResult(ctx, snapshot)
}

func (t runtimeCommandTool) commandRequest(ctx context.Context, call tool.Call, runtime sandbox.Runtime) (taskapi.CommandStartRequest, error) {
	args, err := decodeJSONMap(call.Input)
	if err != nil {
		return taskapi.CommandStartRequest{}, err
	}
	if err := shell.ValidateRunCommandArgs(args); err != nil {
		return taskapi.CommandStartRequest{}, err
	}
	command, ok := stringArg(args, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return taskapi.CommandStartRequest{}, fmt.Errorf("arg %q is required", "command")
	}
	workdir, _ := stringArg(args, "workdir")
	tty := false
	if parsed := optionalBoolArg(args, "tty"); parsed != nil {
		tty = *parsed
	}
	if strings.TrimSpace(workdir) == "" && runtime.FileSystem() != nil {
		workdir, _ = runtime.FileSystem().Getwd()
	}
	yieldMS := int(defaultCommandYield / time.Millisecond)
	if parsed := optionalIntArg(args, "yield_time_ms"); parsed != nil {
		yieldMS = *parsed
	}
	if yieldMS < 0 {
		yieldMS = 0
	}
	constraints, _, _ := sandboxmeta.DecodeConstraints(call.Metadata)
	req := taskapi.CommandStartRequest{OperationID: toolTaskOperationID(ctx, call),
		Command:     strings.TrimSpace(command),
		Workdir:     strings.TrimSpace(workdir),
		TTY:         tty,
		Timeout:     commandTimeoutFromTool(t.base),
		Yield:       time.Duration(yieldMS) * time.Millisecond,
		ParentCall:  strings.TrimSpace(call.ID),
		Constraints: constraints,
		Observer:    taskToolObserver{ctx: ctx},
	}
	return req, nil
}

func commandStartDiagnosticToolResult(name, command string, err error) (tool.Result, bool) {
	if err == nil {
		return tool.Result{}, false
	}
	diag, ok := commanddiag.Best(commanddiag.Input{
		ToolName: shell.RunCommandToolName,
		Command:  command,
		Error:    strings.TrimSpace(err.Error()),
		ExitCode: 1,
	})
	if !ok {
		return tool.Result{}, false
	}
	payload := map[string]any{
		"state":     "failed",
		"error":     strings.TrimSpace(err.Error()),
		"tool_name": strings.TrimSpace(name),
	}
	if hint := strings.TrimSpace(diag.Hint); hint != "" {
		payload["system_hint"] = hint
	}
	raw, _ := json.Marshal(payload)
	return tool.Result{
		Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}},
	}, true
}

type runtimeSpawnTool struct {
	runtime      *Runtime
	base         tool.Tool
	resolver     spawn.Resolver
	session      session.Session
	sessionRef   session.SessionRef
	tasks        *taskRuntime
	runner       subagent.Runner
	mode         string
	approvalMode string
	approval     agent.ApprovalRequester
}

// StartThread returns conversation identity; producer completion owns its output.

func (t runtimeSpawnTool) Definition() tool.Definition {
	def := tool.CloneDefinition(t.base.Definition())
	if t.runtime == nil || t.runtime.controllerContextRouter == nil {
		hideSpawnIncludeContext(&def)
	}
	return def
}

func (t runtimeSpawnTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.runner == nil {
		return tool.Result{}, fmt.Errorf("subagent execution is unavailable")
	}
	if admission, ok := t.base.(interface{ Admit(context.Context) error }); ok {
		if err := admission.Admit(ctx); err != nil {
			return tool.Result{}, err
		}
	}
	args, err := decodeJSONMap(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := spawn.ValidateArgs(args); err != nil {
		return tool.Result{}, err
	}
	prompt, ok := stringArg(args, "prompt")
	if !ok || strings.TrimSpace(prompt) == "" {
		return tool.Result{}, fmt.Errorf("arg %q is required", "prompt")
	}
	var requestedHandle string
	if _, exists := args["handle"]; exists {
		handle, ok := stringArg(args, "handle")
		if !ok || handle == "" {
			return tool.Result{}, fmt.Errorf("arg %q must be a non-empty string", "handle")
		}
		requestedHandle = handle
	}
	requested, _ := stringArg(args, "agent")
	requested, err = resolveRuntimeSpawnToolAgent(t.base.Definition(), requested)
	if err != nil {
		return tool.Result{}, err
	}
	resolver := t.resolver
	if resolver == nil {
		resolver, _ = t.base.(spawn.Resolver)
	}
	if resolver == nil {
		return tool.Result{}, fmt.Errorf("Spawn target resolution is unavailable")
	}
	target, err := resolver.ResolveTarget(requested)
	if err != nil {
		return tool.Result{}, err
	}
	var (
		includeContext     bool
		contextTransfer    agent.ContextTransfer
		contextUnsupported bool
	)
	if parsed := optionalBoolArg(args, "include_context"); parsed != nil && *parsed {
		includeContext = true
		contextTransfer, contextUnsupported = t.resolveDelegatedSpawnContext(ctx)
	}
	snapshot, err := t.tasks.StartSubagentTarget(ctx, t.session, t.sessionRef, t.runner, delegation.NormalizeTarget(target), taskapi.SubagentStartRequest{
		SpawnID:            firstNonEmpty(toolTaskOperationID(ctx, call), strings.TrimSpace(call.ID)),
		Prompt:             strings.TrimSpace(prompt),
		Handle:             requestedHandle,
		Context:            contextTransfer,
		IncludeContext:     includeContext,
		ContextUnsupported: contextUnsupported,
		ParentCall:         strings.TrimSpace(call.ID),
		Role:               session.ParticipantRoleDelegated,
		Source:             "agent_tool",
		Mode:               strings.TrimSpace(t.mode),
		ApprovalMode:       strings.TrimSpace(t.approvalMode),
		Approval:           newSubagentApprovalRequester(t.runtime, t.mode, t.approval, t.session, t.sessionRef),
	})
	if err != nil {
		return tool.Result{}, err
	}
	payload := map[string]any{"handle": snapshot.Handle, "state": snapshot.State}
	payload["supports_steering"] = taskSpecBool(snapshot.Metadata, "supports_steering")
	if taskSpecBool(snapshot.Metadata, "context_unsupported") {
		payload["system_hint"] = spawnContextUnsupportedHint
	}
	result, _ := taskSnapshotToolResultWithPayload(ctx, snapshot, payload)
	return result, nil
}

func (t runtimeSpawnTool) resolveDelegatedSpawnContext(ctx context.Context) (agent.ContextTransfer, bool) {
	if t.runtime == nil || t.runtime.controllerContextRouter == nil {
		return agent.ContextTransfer{}, true
	}
	transfer, err := t.runtime.buildParticipantPromptContext(ctx, t.sessionRef, 0)
	if err != nil {
		return agent.ContextTransfer{}, true
	}
	return transfer, false
}

const spawnContextUnsupportedHint = "Current host does not support parent context transfer; the child received only the spawn prompt."

func hideSpawnIncludeContext(def *tool.Definition) {
	if def == nil {
		return
	}
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		return
	}
	delete(props, "include_context")
}

func resolveRuntimeSpawnToolAgent(def tool.Definition, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	enum := spawnAgentEnum(def)
	if len(enum) == 0 {
		if requested != "" && !strings.EqualFold(requested, "self") {
			return "", fmt.Errorf("Spawn agent %q is not available", requested)
		}
		return normalizeSpawnAgent(requested), nil
	}
	if requested == "" {
		for _, allowed := range enum {
			if strings.EqualFold(allowed, "self") {
				return strings.TrimSpace(allowed), nil
			}
		}
		return "", fmt.Errorf("Spawn agent default is not available")
	}
	for _, allowed := range enum {
		if strings.EqualFold(requested, allowed) {
			return strings.TrimSpace(allowed), nil
		}
	}
	return "", fmt.Errorf("Spawn agent %q is not available", requested)
}

func spawnAgentEnum(def tool.Definition) []string {
	props, _ := def.InputSchema["properties"].(map[string]any)
	agentProp, _ := props["agent"].(map[string]any)
	raw, _ := agentProp["enum"].([]string)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if name := strings.TrimSpace(item); name != "" {
			out = append(out, name)
		}
	}
	return out
}

type runtimeTaskTool struct {
	base       tool.Tool
	sessionRef session.SessionRef
	tasks      *taskRuntime
}

func (runtimeTaskTool) RuntimeTaskResultSource(toolbinding.Token) bool { return true }

type subagentApprovalRequester struct {
	runtime    *Runtime
	mode       string
	requester  agent.ApprovalRequester
	session    session.Session
	sessionRef session.SessionRef
}

func newSubagentApprovalRequester(
	runtime *Runtime,
	mode string,
	requester agent.ApprovalRequester,
	activeSession session.Session,
	sessionRef session.SessionRef,
) subagent.ApprovalRequester {
	if requester == nil && runtime != nil {
		requester = runtime.childApprovalRequester
	}
	if requester == nil && (runtime == nil || normalizePolicyMode(mode) != presets.ModeDangerFullAccess) {
		return nil
	}
	return subagentApprovalRequester{
		runtime:    runtime,
		mode:       strings.TrimSpace(mode),
		requester:  requester,
		session:    session.CloneSession(activeSession),
		sessionRef: session.NormalizeSessionRef(sessionRef),
	}
}

func (r subagentApprovalRequester) RequestSubagentApproval(
	ctx context.Context,
	req subagent.ApprovalRequest,
) (subagent.ApprovalResponse, error) {
	options := make([]session.ProtocolApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, session.ProtocolApprovalOption{
			ID:   item.ID,
			Name: strings.TrimSpace(item.Name),
			Kind: item.Kind,
		})
	}
	toolName := firstNonEmpty(req.ToolCall.Name, req.ToolCall.Kind)
	approvalToolName := toolName
	if req.ToolCall.NamePresent != nil && *req.ToolCall.NamePresent {
		approvalToolName = req.ToolCall.Name
	}
	rawInput := session.CloneState(req.ToolCall.RawInput)
	var callInput json.RawMessage
	if len(rawInput) > 0 {
		if data, err := json.Marshal(rawInput); err == nil {
			callInput = data
		}
	}
	origin := req.Origin
	origin.Role = agent.ApprovalRoleSubagent
	origin.ParentSessionID = r.sessionRef.SessionID
	origin.TaskID = req.TaskID
	origin.ParentCallID = req.ParentCallID
	origin.ToolCallID = req.ToolCall.ID
	origin.Agent = req.Agent
	runtimeRequest := agent.ApprovalRequest{
		Origin:     &origin,
		SessionRef: r.sessionRef,
		Session:    session.CloneSession(r.session),
		Tool: tool.Definition{
			Name: toolName,
		},
		Call: tool.Call{
			ID:    strings.TrimSpace(req.ToolCall.ID),
			Name:  toolName,
			Input: callInput,
		},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:          strings.TrimSpace(req.ToolCall.ID),
				Name:        approvalToolName,
				NamePresent: req.ToolCall.NamePresent,
				Kind:        strings.TrimSpace(req.ToolCall.Kind),
				Title:       strings.TrimSpace(req.ToolCall.Title),
				Status:      strings.TrimSpace(req.ToolCall.Status),
				RawInput:    rawInput,
				RawOutput:   session.CloneState(req.ToolCall.RawOutput),
				Content:     session.CloneProtocolToolCallContent(req.ToolCall.Content),
			},
			Options: options,
		},
		Metadata: map[string]any{
			"subagent":       true,
			"scope":          "subagent",
			"scope_id":       strings.TrimSpace(req.TaskID),
			"task_id":        strings.TrimSpace(req.TaskID),
			"agent":          strings.TrimSpace(req.Agent),
			"parent_call_id": strings.TrimSpace(req.ParentCallID),
			"parent_tool":    spawn.ToolName,
		},
	}
	scope := lifecycleScopeFromContext(ctx)
	runtimeRequest.RunID = firstNonEmpty(scope.runID, "task:"+req.TaskID)
	runtimeRequest.TurnID = firstNonEmpty(scope.turnID, "task:"+req.TaskID)
	runtimeRequest.OperationTaskID = req.TaskID
	runtimeRequest = preserveEndpointApproval(runtimeRequest, req.Approval)
	if r.runtime != nil {
		mode := firstNonEmpty(req.Mode, r.mode)
		if response, handled, err := r.runtime.resolveEndpointApprovalByPolicy(ctx, mode, runtimeRequest); handled {
			return response, err
		}
	}
	if r.requester == nil {
		return subagent.ApprovalResponse{}, nil
	}
	permission := cloneApproval(runtimeRequest.Approval)
	var resp agent.ApprovalResponse
	var err error
	if r.runtime != nil {
		resp, err = r.runtime.requestDurableApproval(ctx, runtimeRequest, r.requester)
	} else {
		resp, err = r.requester.RequestApproval(ctx, runtimeRequest)
	}
	if err != nil {
		return subagent.ApprovalResponse{}, err
	}
	resp, err = approval.ResolveNativeResponse(permission, resp)
	if err != nil {
		return subagent.ApprovalResponse{}, err
	}
	return subagent.ApprovalResponse{
		Outcome:  strings.TrimSpace(resp.Outcome),
		OptionID: strings.TrimSpace(resp.OptionID),
		Approved: resp.Approved,
		Reason:   resp.Reason, ReviewText: resp.ReviewText,
	}, nil
}

func (t runtimeTaskTool) Definition() tool.Definition {
	return t.base.Definition()
}

func (t runtimeTaskTool) RuntimeNativeSchedule(_ toolbinding.Token, ctx context.Context, call tool.Call) toolbinding.NativeSchedule {
	ordered := toolbinding.NativeSchedule{Ordered: true}
	args, err := decodeJSONMap(call.Input)
	if err != nil || tasktool.ValidateArgs(args) != nil {
		return ordered
	}
	handle, _ := stringArg(args, "handle")
	handles := splitTaskControlHandles(handle)
	for _, handle := range handles {
		identity, err := t.resolveJobHandle(ctx, handle)
		if err != nil {
			return ordered
		}
		if identity.kind == taskapi.KindCodeMode {
			if len(handles) != 1 || !t.hasActiveScript(identity.taskID) {
				return ordered
			}
			action, _ := stringArg(args, "action")
			switch strings.ToLower(action) {
			case "wait":
				if yield := optionalIntArg(args, "yield_time_ms"); yield == nil || *yield > 0 {
					ordered.ScriptWait = identity.taskID
				}
			case "cancel":
				ordered.ScriptCancel = identity.taskID
			}
			return ordered
		}
	}
	return toolbinding.NativeSchedule{}
}

func (t runtimeTaskTool) hasActiveScript(id string) bool {
	t.tasks.mu.RLock()
	script := t.tasks.codeTasks[id]
	t.tasks.mu.RUnlock()
	if script == nil || script.sessionRef.SessionID != t.sessionRef.SessionID {
		return false
	}
	select {
	case <-script.done:
		return false
	default:
		return true
	}
}

func (t runtimeTaskTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	args, err := decodeJSONMap(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := tasktool.ValidateArgs(args); err != nil {
		return tool.Result{}, err
	}
	action, ok := stringArg(args, "action")
	if !ok || strings.TrimSpace(action) == "" {
		return tool.Result{}, fmt.Errorf("arg %q is required", "action")
	}
	handle, ok := stringArg(args, "handle")
	if !ok || strings.TrimSpace(handle) == "" {
		return tool.Result{}, fmt.Errorf("arg %q is required", "handle")
	}
	handles := splitTaskControlHandles(handle)
	if len(handles) == 0 {
		return tool.Result{}, fmt.Errorf("arg %q is required", "handle")
	}
	yield := time.Duration(0)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		yield = time.Minute
		if value := optionalIntArg(args, "yield_time_ms"); value != nil {
			yield = time.Duration(*value) * time.Millisecond
		}
	}
	if parent := tool.ParentFromContext(ctx); parent != nil && parent.Kind == codemode.ToolKind {
		for _, handle := range handles {
			identity, err := t.resolveJobHandle(ctx, handle)
			if err != nil {
				return tool.Result{}, err
			}
			if strings.EqualFold(action, "write") && identity.kind == taskapi.KindCommand {
				return tool.Result{}, codemode.NewPublicPermissionError("native command stdin is prohibited from JavaScript")
			}
			if identity.kind == taskapi.KindCodeMode && (yield > 0 || strings.EqualFold(action, "cancel")) {
				entry, ok := t.tasks.storedTaskEntryByID(ctx, t.sessionRef, identity.taskID)
				if ok && taskStringValue(entry.Metadata["parent_call"]) == parent.ID {
					return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "a script cannot wait on or cancel its own Task")
				}
			}
		}
	}
	input, _ := rawStringArg(args, "input")
	appendNewline := optionalBoolArg(args, "append_newline")
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	switch normalizedAction {
	case "wait", "write", "cancel":
	default:
		return tool.Result{}, fmt.Errorf("invalid Task action %q", action)
	}
	if len(handles) > 1 && normalizedAction == "write" {
		return tool.Result{}, fmt.Errorf("task action %q accepts exactly one handle", normalizedAction)
	}
	if normalizedAction == "wait" && len(handles) > 1 {
		return t.callBatchTaskWaitAny(ctx, handles, yield)
	}
	if len(handles) > 1 {
		return t.callBatchTaskCancel(ctx, handles)
	}
	identity, err := t.resolveJobHandle(ctx, handles[0])
	if err != nil {
		return tool.Result{}, err
	}

	if normalizedAction == "write" && identity.kind == taskapi.KindCommand {
		yield = taskWriteOutputWait
	}
	req := taskapi.ControlRequest{
		TaskID:        identity.taskID,
		Yield:         yield,
		Input:         input,
		AppendNewline: appendNewline,
		Principal:     session.ActorKindTool,
		Source:        "agent_tool",
	}
	var snapshot taskapi.Snapshot
	actualWaitMS := 0
	started := time.Now()
	snapshot, controlErr := t.callTaskControl(ctx, normalizedAction, req)
	if normalizedAction == "wait" {
		actualWaitMS = durationMillis(time.Since(started))
	}
	if controlErr != nil {
		return tool.Result{}, controlErr
	}

	result, resultErr := taskControlSnapshotToolResult(ctx, snapshot, normalizedAction, actualWaitMS)
	observation := toolbinding.Observed(ctx)
	observation.TaskEvent = taskToolResultEventMeta(observation.TaskEvent, normalizedAction, input, yield, actualWaitMS, snapshot)
	return result, resultErr
}

func durationMillis(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int(value / time.Millisecond)
}

func (t runtimeTaskTool) callBatchTaskCancel(ctx context.Context, handles []string) (tool.Result, error) {
	items := make([]taskBatchControlItem, 0, len(handles))
	for _, handle := range handles {
		identity, resolveErr := t.resolveJobHandle(ctx, handle)
		if resolveErr != nil {
			items = append(items, taskBatchControlItem{Handle: handle, Err: resolveErr})
			continue
		}

		req := taskapi.ControlRequest{
			TaskID:    identity.taskID,
			Principal: session.ActorKindTool,
			Source:    "agent_tool",
		}
		snapshot, err := t.tasks.Cancel(ctx, t.sessionRef, req)

		if err != nil {
			items = append(items, taskBatchControlItem{Handle: handle, Err: err})
			continue
		}

		items = append(items, taskBatchControlItem{Handle: handle, Snapshot: snapshot})
	}

	result, resultErr := taskBatchControlToolResult(items, "cancel", 0)
	observation := toolbinding.Observed(ctx)
	observation.TaskEvent = taskBatchToolResultEventMeta(observation.TaskEvent, "cancel", 0, 0, items)
	return result, resultErr
}

func (t runtimeTaskTool) callBatchTaskWaitAny(ctx context.Context, handles []string, yield time.Duration) (tool.Result, error) {
	type target struct {
		handle   string
		identity taskControlIdentity
	}
	type outcome struct {
		target
		snapshot taskapi.Snapshot
		err      error
		waitMS   int
	}
	itemsByHandle := make(map[string]taskBatchControlItem, len(handles))
	targets := make([]target, 0, len(handles))
	for _, handle := range handles {
		identity, err := t.resolveJobHandle(ctx, handle)
		if err != nil {
			itemsByHandle[handle] = taskBatchControlItem{Handle: handle, Err: err}
			continue
		}
		targets = append(targets, target{handle: handle, identity: identity})
	}
	started := time.Now()
	if len(targets) > 0 {
		waitCtx, cancel := context.WithCancel(ctx)
		results := make(chan outcome, len(targets))
		for _, item := range targets {
			go func(item target) {
				itemStarted := time.Now()
				snapshot, err := t.callTaskControl(waitCtx, "wait", taskapi.ControlRequest{
					TaskID: item.identity.taskID, Yield: yield,
					Principal: session.ActorKindTool, Source: "agent_tool",
				})
				results <- outcome{target: item, snapshot: snapshot, err: err, waitMS: durationMillis(time.Since(itemStarted))}
			}(item)
		}
		var winner outcome
		outcomes := make(map[string]outcome, len(targets))
		received := 0
		for received < len(targets) {
			result := <-results
			received++
			outcomes[result.handle] = result
			// A running snapshot is not a wait-any winner. It may be an
			// observation taken while another lifecycle owner is committing,
			// or a normal yield expiry. Keep the other waits alive until one
			// target is terminal or every target has returned.
			if yield > 0 && result.err == nil && !result.snapshot.Running {
				winner = result
				break
			}
		}
		cancel()
		for received < len(targets) {
			result := <-results
			received++
			outcomes[result.handle] = result
		}
		for _, item := range targets {
			waited := outcomes[item.handle]
			if waited.err == nil {

				itemsByHandle[item.handle] = taskBatchControlItem{Handle: item.handle, Snapshot: waited.snapshot, ActualWaitMS: waited.waitMS}
				continue
			}
			if winner.handle == "" || !errors.Is(waited.err, context.Canceled) {
				itemsByHandle[item.handle] = taskBatchControlItem{Handle: item.handle, Err: waited.err, ActualWaitMS: waited.waitMS}
				continue
			}
			snapshot, err := t.tasks.Read(ctx, t.sessionRef, taskapi.ControlRequest{
				TaskID: item.identity.taskID, Principal: session.ActorKindTool, Source: "agent_tool",
			})

			itemsByHandle[item.handle] = taskBatchControlItem{Handle: item.handle, Snapshot: snapshot, Err: err}
		}
	}
	items := make([]taskBatchControlItem, 0, len(handles))
	for _, handle := range handles {
		items = append(items, itemsByHandle[handle])
	}
	actualWaitMS := durationMillis(time.Since(started))
	result, resultErr := taskBatchControlToolResult(items, "wait", actualWaitMS)
	observation := toolbinding.Observed(ctx)
	observation.TaskEvent = taskBatchToolResultEventMeta(observation.TaskEvent, "wait", yield, actualWaitMS, items)
	return result, resultErr
}

func (t runtimeTaskTool) callTaskControl(ctx context.Context, action string, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	normalizedReq := normalizeTaskControlRequest(req)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait":
		if req.Yield == 0 {
			return t.tasks.Read(ctx, t.sessionRef, normalizedReq)
		}
		return t.tasks.Wait(ctx, t.sessionRef, normalizedReq)
	case "write":
		return t.tasks.Write(ctx, t.sessionRef, normalizedReq)
	case "cancel":
		return t.tasks.Cancel(ctx, t.sessionRef, normalizedReq)
	default:
		return taskapi.Snapshot{}, fmt.Errorf("invalid Task action %q", action)
	}
}

func splitTaskControlHandles(handle string) []string {
	parts := strings.Split(handle, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func taskToolResultEventMeta(existing map[string]any, action string, input string, yield time.Duration, actualWaitMS int, snapshot taskapi.Snapshot) map[string]any {
	out := session.CloneState(existing)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := toolbinding.RuntimeSection(out, "tool")
	toolMeta["name"] = tasktool.ToolName
	toolMeta["action"] = strings.ToLower(strings.TrimSpace(action))
	toolMeta["target_kind"] = strings.TrimSpace(string(snapshot.Kind))
	toolMeta["target_handle"] = taskPublicHandle(snapshot)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		toolMeta["effective_yield_time_ms"] = durationMillis(yield)
		toolMeta["actual_wait_time_ms"] = actualWaitMS
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		toolMeta["input"] = strings.TrimSpace(input)
	}
	return out
}

func taskBatchToolResultEventMeta(existing map[string]any, action string, yield time.Duration, actualWaitMS int, items []taskBatchControlItem) map[string]any {
	out := session.CloneState(existing)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := toolbinding.RuntimeSection(out, "tool")
	toolMeta["name"] = tasktool.ToolName
	toolMeta["action"] = strings.ToLower(strings.TrimSpace(action))
	toolMeta["target_handles"] = taskBatchVisibleHandles(items)
	toolMeta["target_count"] = len(items)
	if failed := taskBatchErrorCount(items); failed > 0 {
		toolMeta["failed_count"] = failed
	}
	if kind, ok := commonTaskBatchKind(items); ok {
		toolMeta["target_kind"] = strings.TrimSpace(string(kind))
	}
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		toolMeta["effective_yield_time_ms"] = durationMillis(yield)
		toolMeta["actual_wait_time_ms"] = actualWaitMS
	}
	taskMeta := toolbinding.RuntimeSection(out, "task")
	taskMeta["handles"] = taskBatchVisibleHandles(items)
	taskMeta["count"] = len(items)
	if failed := taskBatchErrorCount(items); failed > 0 {
		taskMeta["failed_count"] = failed
	}
	if kind, ok := commonTaskBatchKind(items); ok {
		taskMeta["kind"] = strings.TrimSpace(string(kind))
	}
	return out
}

func taskBatchVisibleHandles(items []taskBatchControlItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		handle := strings.TrimSpace(item.Handle)
		if handle == "" && item.Err == nil {
			handle = taskPublicHandle(item.Snapshot)
		}
		if handle != "" {
			out = append(out, handle)
		}
	}
	return out
}

func commonTaskBatchKind(items []taskBatchControlItem) (taskapi.Kind, bool) {
	var kind taskapi.Kind
	for _, item := range items {
		if item.Err != nil {
			continue
		}
		if strings.TrimSpace(string(item.Snapshot.Kind)) == "" {
			return "", false
		}
		if strings.TrimSpace(string(kind)) == "" {
			kind = item.Snapshot.Kind
			continue
		}
		if kind != item.Snapshot.Kind {
			return "", false
		}
	}
	return kind, strings.TrimSpace(string(kind)) != ""
}

func (t runtimeTaskTool) resolveJobHandle(ctx context.Context, handle string) (taskControlIdentity, error) {
	identity, err := t.tasks.resolveTaskHandle(ctx, t.sessionRef, handle)
	if err == nil && identity.kind == taskapi.KindSubagent {
		return taskControlIdentity{}, errorcode.New(errorcode.Unsupported, "participant handles require ReadThread or WaitThread; use SendMessage for input")
	}
	return identity, err
}
