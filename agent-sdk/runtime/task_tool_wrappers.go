package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
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
	"github.com/caelis-labs/caelis/agent-sdk/tool/commanddiag"
)

func (r *Runtime) wrapToolsForRuntime(activeSession session.Session, ref session.SessionRef, spec agent.AgentSpec, toolCtx runtimeToolContext) []tool.Tool {
	out := make([]tool.Tool, 0, len(spec.Tools)+2)
	hasCommand := false
	hasSpawn := false
	hasTask := false
	hasSendMessage := false
	for _, one := range spec.Tools {
		if one == nil {
			continue
		}
		switch {
		case isBuiltinRunCommandTool(one):
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
		case isBuiltinSpawnTool(one):
			hasSpawn = true
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
		case isBuiltinTaskTool(one):
			hasTask = true
			out = append(out, runtimeTaskTool{
				base:       one,
				sessionRef: session.NormalizeSessionRef(ref),
				tasks:      r.tasks,
			})
		case isBuiltinSendMessageTool(one):
			hasSendMessage = true
			out = append(out, runtimeSendMessageTool{
				base: one, runtime: r, session: session.CloneSession(activeSession),
				sessionRef: session.NormalizeSessionRef(ref), external: toolCtx.inputSender,
			})
		default:
			out = append(out, one)
		}
	}
	if (hasCommand || hasSpawn) && !hasTask {
		out = append(out, runtimeTaskTool{
			base:       tasktool.New(),
			sessionRef: session.NormalizeSessionRef(ref),
			tasks:      r.tasks,
		})
	}
	// Product Agents assemble SendMessage explicitly with Spawn so AgentSpec is
	// auditable. A hosted child receives its parent/sibling transport only at
	// runtime through context, so that SDK host boundary remains the sole
	// intentional augmentation.
	if toolCtx.inputSender != nil && !hasSendMessage {
		out = append(out, runtimeSendMessageTool{
			base: sendmessage.New(), runtime: r, session: session.CloneSession(activeSession),
			sessionRef: session.NormalizeSessionRef(ref), external: toolCtx.inputSender,
		})
	}
	return out
}

func isBuiltinRunCommandTool(candidate tool.Tool) bool {
	_, ok := candidate.(*shell.RunCommandTool)
	return ok
}

func isBuiltinSpawnTool(candidate tool.Tool) bool {
	switch candidate.(type) {
	case spawn.Tool, *spawn.Tool:
		return true
	default:
		return false
	}
}

func isBuiltinTaskTool(candidate tool.Tool) bool {
	switch candidate.(type) {
	case tasktool.Tool, *tasktool.Tool:
		return true
	default:
		return false
	}
}

func isBuiltinSendMessageTool(candidate tool.Tool) bool {
	switch candidate.(type) {
	case sendmessage.Tool, *sendmessage.Tool:
		return true
	default:
		return false
	}
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
	return sendMessageToolResult(call, input.Target), nil
}

func sendMessageToolResult(call tool.Call, target string) tool.Result {
	meta := map[string]any{}
	messageMeta := taskRuntimeMetaSection(meta, "message")
	messageMeta["to"] = strings.TrimSpace(target)
	return tool.Result{
		ID:       strings.TrimSpace(call.ID),
		Name:     sendmessage.ToolName,
		Content:  []model.Part{model.NewTextPart("Message sent.")},
		Metadata: meta,
	}
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
	args, err := decodeJSONMap(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := shell.ValidateRunCommandArgs(args); err != nil {
		return tool.Result{}, err
	}
	command, ok := stringArg(args, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return tool.Result{}, fmt.Errorf("arg %q is required", "command")
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
	req := taskapi.CommandStartRequest{
		Command:     strings.TrimSpace(command),
		Workdir:     strings.TrimSpace(workdir),
		TTY:         tty,
		Timeout:     commandTimeoutFromTool(t.base),
		Yield:       time.Duration(yieldMS) * time.Millisecond,
		ParentCall:  strings.TrimSpace(call.ID),
		ParentTool:  strings.TrimSpace(call.Name),
		Constraints: constraintsFromMetadata(call.Metadata),
		Observer: taskToolObserver{
			call:     call,
			def:      t.base.Definition(),
			observer: call.Observer,
		},
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
			result := taskSnapshotToolResultWithPayload(call, t.base.Definition(), snapshot, payload)
			result.IsError = true
			return result, nil
		}
		if result, ok := commandStartDiagnosticToolResult(call, t.base.Definition(), command, err); ok {
			return result, nil
		}
		return tool.Result{}, err
	}
	return taskSnapshotToolResult(call, t.base.Definition(), snapshot), nil
}

func commandStartDiagnosticToolResult(call tool.Call, def tool.Definition, command string, err error) (tool.Result, bool) {
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
		"tool_name": strings.TrimSpace(def.Name),
	}
	if hint := strings.TrimSpace(diag.Hint); hint != "" {
		payload["system_hint"] = hint
	}
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(def.Name),
		IsError: true,
		Content: []model.Part{model.NewJSONPart(raw)},
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

func (runtimeSpawnTool) RuntimeTaskResultSource(toolbinding.Token) bool { return true }

func (t runtimeSpawnTool) Definition() tool.Definition {
	def := tool.CloneDefinition(t.base.Definition())
	// Runtime serializes only the short participant-binding commit. External
	// child startup and stream production are independent per Task.
	def.Capabilities.ParallelSafe = true
	if t.runtime == nil || t.runtime.controllerContextRouter == nil {
		hideSpawnIncludeContext(&def)
	}
	return def
}

func (t runtimeSpawnTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.runner == nil {
		return tool.Result{}, fmt.Errorf("subagent execution is unavailable")
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
	requested, err = resolveRuntimeSpawnToolAgent(t.base.Definition(), t.session, requested)
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
		SpawnID:            strings.TrimSpace(call.ID),
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
	result := taskSnapshotToolResult(call, t.base.Definition(), snapshot)
	if spawnContextUnsupported(snapshot) {
		payload := taskToolPayload(snapshot)
		payload["system_hint"] = spawnContextUnsupportedHint
		result = taskSnapshotToolResultWithPayload(call, t.base.Definition(), snapshot, payload)
	}
	t.tasks.markSubagentFinalResponseObserved(snapshot)
	return result, nil
}

func (t runtimeSpawnTool) resolveDelegatedSpawnContext(ctx context.Context) (agent.ContextTransfer, bool) {
	if t.runtime == nil || t.runtime.controllerContextRouter == nil {
		return agent.ContextTransfer{}, true
	}
	transfer, _, err := t.runtime.buildDelegatedSpawnPromptContext(ctx, t.session, t.sessionRef)
	if err != nil {
		return agent.ContextTransfer{}, true
	}
	return transfer, false
}

func spawnContextUnsupported(snapshot taskapi.Snapshot) bool {
	return taskSpecBool(snapshot.Metadata, "context_unsupported")
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

func resolveRuntimeSpawnToolAgent(def tool.Definition, activeSession session.Session, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	enum := spawnAgentEnum(def)
	if len(enum) == 0 {
		if requested != "" && !strings.EqualFold(requested, "self") {
			return "", fmt.Errorf("Spawn agent %q is not available", requested)
		}
		return resolveSpawnAgent(activeSession, requested)
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
	rawInput := session.CloneState(req.ToolCall.RawInput)
	var callInput json.RawMessage
	if len(rawInput) > 0 {
		if data, err := json.Marshal(rawInput); err == nil {
			callInput = data
		}
	}
	runtimeRequest := agent.ApprovalRequest{
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
				ID:        strings.TrimSpace(req.ToolCall.ID),
				Name:      toolName,
				Kind:      strings.TrimSpace(req.ToolCall.Kind),
				Title:     strings.TrimSpace(req.ToolCall.Title),
				Status:    strings.TrimSpace(req.ToolCall.Status),
				RawInput:  rawInput,
				RawOutput: session.CloneState(req.ToolCall.RawOutput),
				Content:   session.CloneProtocolToolCallContent(req.ToolCall.Content),
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
	if r.runtime != nil {
		mode := firstNonEmpty(req.Mode, r.mode)
		if response, handled, err := r.runtime.resolveEndpointApprovalByPolicy(ctx, mode, runtimeRequest); handled {
			return response, err
		}
	}
	if r.requester == nil {
		return subagent.ApprovalResponse{}, nil
	}
	resp, err := r.requester.RequestApproval(ctx, runtimeRequest)
	if err != nil {
		return subagent.ApprovalResponse{}, err
	}
	return subagent.ApprovalResponse{
		Outcome:  strings.TrimSpace(resp.Outcome),
		OptionID: strings.TrimSpace(resp.OptionID),
		Approved: resp.Approved,
	}, nil
}

func (t runtimeTaskTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
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
	input, _ := rawStringArg(args, "input")
	appendNewline := optionalBoolArg(args, "append_newline")
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	switch normalizedAction {
	case "wait", "read", "write", "cancel":
	default:
		return tool.Result{}, fmt.Errorf("invalid Task action %q", action)
	}
	if len(handles) > 1 && (normalizedAction == "read" || normalizedAction == "write") {
		return tool.Result{}, fmt.Errorf("task action %q accepts exactly one handle", normalizedAction)
	}
	if len(handles) > 1 {
		result := t.callBatchTaskControl(ctx, call, normalizedAction, handles, input)
		return result, nil
	}
	identity, err := t.tasks.resolveTaskHandle(ctx, t.sessionRef, handles[0])
	if err != nil {
		return tool.Result{}, err
	}
	if normalizedAction == "write" && identity.kind != taskapi.KindCommand {
		return tool.Result{}, fmt.Errorf("task write accepts RunCommand handles only; use SendMessage for subagent %q", handles[0])
	}
	yield := time.Duration(0)
	switch normalizedAction {
	case "wait":
		yield = taskWaitMaxYield
	case "write":
		if identity.kind == taskapi.KindCommand {
			yield = taskWriteOutputWait
		}
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
	if identity.kind == taskapi.KindSubagent && (normalizedAction == "read" || normalizedAction == "wait") {
		snapshot = t.tasks.consumeSubagentFinalResponses(snapshot)
	}
	result := taskControlSnapshotToolResult(call, t.base.Definition(), snapshot, normalizedAction, actualWaitMS)
	result.Metadata = taskToolResultEventMeta(result.Metadata, normalizedAction, input, actualWaitMS, snapshot)
	return result, nil
}

func durationMillis(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int(value / time.Millisecond)
}

func (t runtimeTaskTool) callBatchTaskControl(ctx context.Context, call tool.Call, action string, handles []string, input string) tool.Result {
	if strings.EqualFold(action, "wait") {
		return t.callBatchTaskWaitAny(ctx, call, handles)
	}
	items := make([]taskBatchControlItem, 0, len(handles))
	started := time.Now()
	for _, handle := range handles {
		yield := time.Duration(0)
		if strings.EqualFold(action, "wait") {
			yield = taskWaitMaxYield
		}
		if strings.EqualFold(action, "wait") && yield > 0 {
			elapsed := time.Since(started)
			if elapsed >= yield {
				yield = 0
			} else {
				yield -= elapsed
			}
		}
		identity, resolveErr := t.tasks.resolveTaskHandle(ctx, t.sessionRef, handle)
		if resolveErr != nil {
			items = append(items, taskBatchControlItem{Handle: handle, Err: resolveErr})
			continue
		}
		req := taskapi.ControlRequest{
			TaskID:    identity.taskID,
			Yield:     yield,
			Input:     input,
			Principal: session.ActorKindTool,
			Source:    "agent_tool",
		}
		var snapshot taskapi.Snapshot
		var err error
		itemStarted := time.Now()
		actualWaitMS := 0
		snapshot, err = t.callTaskControl(ctx, action, req)
		if strings.EqualFold(action, "wait") {
			actualWaitMS = durationMillis(time.Since(itemStarted))
		}
		if err != nil {
			items = append(items, taskBatchControlItem{Handle: handle, Err: err, ActualWaitMS: actualWaitMS})
			continue
		}
		if identity.kind == taskapi.KindSubagent && strings.EqualFold(action, "wait") {
			snapshot = t.tasks.consumeSubagentFinalResponses(snapshot)
		}
		items = append(items, taskBatchControlItem{Handle: handle, Snapshot: snapshot, OK: true, ActualWaitMS: actualWaitMS})
	}
	actualWaitMS := 0
	if strings.EqualFold(action, "wait") {
		actualWaitMS = durationMillis(time.Since(started))
	}
	result := taskBatchControlToolResult(call, t.base.Definition(), items, action, actualWaitMS)
	result.Metadata = taskBatchToolResultEventMeta(result.Metadata, action, input, actualWaitMS, items)
	return result
}

func (t runtimeTaskTool) callBatchTaskWaitAny(ctx context.Context, call tool.Call, handles []string) tool.Result {
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
		identity, err := t.tasks.resolveTaskHandle(ctx, t.sessionRef, handle)
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
				snapshot, err := t.tasks.Wait(waitCtx, t.sessionRef, taskapi.ControlRequest{
					TaskID: item.identity.taskID, Yield: taskWaitMaxYield,
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
			if result.err == nil && (!result.snapshot.Running || snapshotHasUnreadSubagentFinalResponses(result.snapshot)) {
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
				if item.identity.kind == taskapi.KindSubagent {
					waited.snapshot = t.tasks.consumeSubagentFinalResponses(waited.snapshot)
				}
				itemsByHandle[item.handle] = taskBatchControlItem{Handle: item.handle, Snapshot: waited.snapshot, OK: true, ActualWaitMS: waited.waitMS}
				continue
			}
			if winner.handle == "" || !errors.Is(waited.err, context.Canceled) {
				itemsByHandle[item.handle] = taskBatchControlItem{Handle: item.handle, Err: waited.err, ActualWaitMS: waited.waitMS}
				continue
			}
			snapshot, err := t.tasks.Read(ctx, t.sessionRef, taskapi.ControlRequest{
				TaskID: item.identity.taskID, Principal: session.ActorKindTool, Source: "agent_tool",
			})
			if err == nil && item.identity.kind == taskapi.KindSubagent {
				snapshot = t.tasks.consumeSubagentFinalResponses(snapshot)
			}
			itemsByHandle[item.handle] = taskBatchControlItem{Handle: item.handle, Snapshot: snapshot, OK: err == nil, Err: err}
		}
	}
	items := make([]taskBatchControlItem, 0, len(handles))
	for _, handle := range handles {
		items = append(items, itemsByHandle[handle])
	}
	actualWaitMS := durationMillis(time.Since(started))
	result := taskBatchControlToolResult(call, t.base.Definition(), items, "wait", actualWaitMS)
	result.Metadata = taskBatchToolResultEventMeta(result.Metadata, "wait", "", actualWaitMS, items)
	return result
}

func (t runtimeTaskTool) callTaskControl(ctx context.Context, action string, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	normalizedReq := normalizeTaskControlRequest(req)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait":
		return t.tasks.Wait(ctx, t.sessionRef, normalizedReq)
	case "read":
		return t.tasks.Read(ctx, t.sessionRef, normalizedReq)
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

func taskToolResultEventMeta(existing map[string]any, action string, input string, actualWaitMS int, snapshot taskapi.Snapshot) map[string]any {
	out := session.CloneState(existing)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := taskRuntimeMetaSection(out, "tool")
	toolMeta["name"] = tasktool.ToolName
	toolMeta["action"] = strings.ToLower(strings.TrimSpace(action))
	toolMeta["target_kind"] = strings.TrimSpace(string(snapshot.Kind))
	toolMeta["target_handle"] = taskPublicHandle(snapshot)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		toolMeta["effective_yield_time_ms"] = int(taskWaitMaxYield / time.Millisecond)
		toolMeta["actual_wait_time_ms"] = actualWaitMS
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		toolMeta["input"] = strings.TrimSpace(input)
	}
	return out
}

func taskBatchToolResultEventMeta(existing map[string]any, action string, input string, actualWaitMS int, items []taskBatchControlItem) map[string]any {
	out := session.CloneState(existing)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := taskRuntimeMetaSection(out, "tool")
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
		toolMeta["effective_yield_time_ms"] = int(taskWaitMaxYield / time.Millisecond)
		toolMeta["actual_wait_time_ms"] = actualWaitMS
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		toolMeta["input"] = strings.TrimSpace(input)
	}
	taskMeta := taskRuntimeMetaSection(out, "task")
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
		if handle == "" && item.OK {
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
		if !item.OK {
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

func taskRuntimeMetaSection(meta map[string]any, section string) map[string]any {
	if meta == nil {
		return nil
	}
	caelis, _ := meta["caelis"].(map[string]any)
	if caelis == nil {
		caelis = map[string]any{}
		meta["caelis"] = caelis
	}
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = 1
	}
	runtime, _ := caelis["runtime"].(map[string]any)
	if runtime == nil {
		runtime = map[string]any{}
		caelis["runtime"] = runtime
	}
	values, _ := runtime[section].(map[string]any)
	if values == nil {
		values = map[string]any{}
		runtime[section] = values
	}
	return values
}
