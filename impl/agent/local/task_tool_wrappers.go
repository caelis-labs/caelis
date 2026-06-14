package local

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func (r *Runtime) wrapToolsForRuntime(activeSession session.Session, ref session.SessionRef, spec agent.AgentSpec, toolCtx runtimeToolContext) []tool.Tool {
	if len(spec.Tools) == 0 {
		return spec.Tools
	}
	out := make([]tool.Tool, 0, len(spec.Tools)+1)
	hasCommand := false
	hasSpawn := false
	hasTask := false
	for _, one := range spec.Tools {
		if one == nil {
			continue
		}
		name := strings.ToUpper(strings.TrimSpace(one.Definition().Name))
		switch name {
		case shell.RunCommandToolName:
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
		case spawn.ToolName:
			hasSpawn = true
			out = append(out, runtimeSpawnTool{
				base:         one,
				session:      session.CloneSession(activeSession),
				sessionRef:   session.NormalizeSessionRef(ref),
				tasks:        r.tasks,
				runner:       r.subagents,
				mode:         strings.TrimSpace(toolCtx.mode),
				approvalMode: strings.TrimSpace(toolCtx.approvalMode),
				approval:     toolCtx.approvalRequester,
			})
		case tasktool.ToolName:
			hasTask = true
			out = append(out, runtimeTaskTool{
				base:       one,
				sessionRef: session.NormalizeSessionRef(ref),
				tasks:      r.tasks,
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
	return out
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
	command, ok := stringArg(args, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return tool.Result{}, fmt.Errorf("tool: arg %q is required", "command")
	}
	workdir, _ := stringArg(args, "workdir")
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
		return tool.Result{}, err
	}
	return taskSnapshotToolResult(call, t.base.Definition(), snapshot), nil
}

type runtimeSpawnTool struct {
	base         tool.Tool
	session      session.Session
	sessionRef   session.SessionRef
	tasks        *taskRuntime
	runner       subagent.Runner
	mode         string
	approvalMode string
	approval     agent.ApprovalRequester
}

func (t runtimeSpawnTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}

func (t runtimeSpawnTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.runner == nil {
		return tool.Result{}, fmt.Errorf("impl/agent/local: subagent runner is unavailable")
	}
	args, err := decodeJSONMap(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	prompt, ok := stringArg(args, "prompt")
	if !ok || strings.TrimSpace(prompt) == "" {
		return tool.Result{}, fmt.Errorf("tool: arg %q is required", "prompt")
	}
	if err := rejectUnknownArgs(args, "agent", "prompt"); err != nil {
		return tool.Result{}, err
	}
	agent, _ := stringArg(args, "agent")
	agent, err = resolveRuntimeSpawnToolAgent(t.base.Definition(), t.session, agent)
	if err != nil {
		return tool.Result{}, err
	}
	snapshot, err := t.tasks.StartSubagent(ctx, t.session, t.sessionRef, t.runner, taskapi.SubagentStartRequest{
		Agent:        strings.TrimSpace(agent),
		Prompt:       strings.TrimSpace(prompt),
		ParentCall:   strings.TrimSpace(call.ID),
		ParentTool:   strings.TrimSpace(call.Name),
		Mode:         strings.TrimSpace(t.mode),
		ApprovalMode: strings.TrimSpace(t.approvalMode),
		Approval:     newSubagentApprovalRequester(t.approval, t.session, t.sessionRef),
	})
	if err != nil {
		return tool.Result{}, err
	}
	result := taskSnapshotToolResult(call, t.base.Definition(), snapshot)
	return result, nil
}

func resolveRuntimeSpawnToolAgent(def tool.Definition, activeSession session.Session, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	enum := spawnAgentEnum(def)
	if len(enum) == 0 {
		if requested != "" && !strings.EqualFold(requested, "self") {
			return "", fmt.Errorf("tool: SPAWN agent %q is not available", requested)
		}
		return resolveSpawnAgent(activeSession, requested)
	}
	if requested == "" {
		for _, allowed := range enum {
			if strings.EqualFold(allowed, "self") {
				return strings.TrimSpace(allowed), nil
			}
		}
		return "", fmt.Errorf("tool: SPAWN agent default is not available")
	}
	for _, allowed := range enum {
		if strings.EqualFold(requested, allowed) {
			return strings.TrimSpace(allowed), nil
		}
	}
	return "", fmt.Errorf("tool: SPAWN agent %q is not available", requested)
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

type subagentApprovalRequester struct {
	requester  agent.ApprovalRequester
	session    session.Session
	sessionRef session.SessionRef
}

func newSubagentApprovalRequester(
	requester agent.ApprovalRequester,
	activeSession session.Session,
	sessionRef session.SessionRef,
) subagent.ApprovalRequester {
	if requester == nil {
		return nil
	}
	return subagentApprovalRequester{
		requester:  requester,
		session:    session.CloneSession(activeSession),
		sessionRef: session.NormalizeSessionRef(sessionRef),
	}
}

func (r subagentApprovalRequester) RequestSubagentApproval(
	ctx context.Context,
	req subagent.ApprovalRequest,
) (subagent.ApprovalResponse, error) {
	if r.requester == nil {
		return subagent.ApprovalResponse{}, nil
	}
	options := make([]session.ProtocolApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, session.ProtocolApprovalOption{
			ID:   strings.TrimSpace(item.ID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	toolName := strings.TrimSpace(req.ToolCall.Name)
	if toolName == "" || strings.EqualFold(toolName, "UNKNOWN") {
		toolName = firstNonEmpty(req.ToolCall.Title, req.ToolCall.Kind, "UNKNOWN")
	}
	rawInput := maps.Clone(req.ToolCall.RawInput)
	var callInput json.RawMessage
	if len(rawInput) > 0 {
		if data, err := json.Marshal(rawInput); err == nil {
			callInput = data
		}
	}
	resp, err := r.requester.RequestApproval(ctx, agent.ApprovalRequest{
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
				ID:       strings.TrimSpace(req.ToolCall.ID),
				Name:     toolName,
				Kind:     strings.TrimSpace(req.ToolCall.Kind),
				Title:    strings.TrimSpace(req.ToolCall.Title),
				Status:   strings.TrimSpace(req.ToolCall.Status),
				RawInput: rawInput,
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
			"parent_tool":    "SPAWN",
		},
	})
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
	action, ok := stringArg(args, "action")
	if !ok || strings.TrimSpace(action) == "" {
		return tool.Result{}, fmt.Errorf("tool: arg %q is required", "action")
	}
	taskID, ok := stringArg(args, "task_id")
	if !ok || strings.TrimSpace(taskID) == "" {
		return tool.Result{}, fmt.Errorf("tool: arg %q is required", "task_id")
	}
	taskIDs := splitTaskControlIDs(taskID)
	if len(taskIDs) == 0 {
		return tool.Result{}, fmt.Errorf("tool: arg %q is required", "task_id")
	}
	yieldMS := 0
	parsedYield := optionalIntArg(args, "yield_time_ms")
	waitUntilDone := false
	if parsed := optionalBoolArg(args, "wait_until_done"); parsed != nil {
		waitUntilDone = *parsed
	}
	yieldDefaulted := false
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		yieldMS = int(defaultCommandYield / time.Millisecond)
		if waitUntilDone {
			yieldMS = int(defaultTaskWaitUntilDoneYield / time.Millisecond)
		}
		yieldDefaulted = parsedYield == nil
	}
	if parsedYield != nil {
		yieldMS = *parsedYield
	}
	if yieldMS < 0 {
		yieldMS = 0
	}
	input, _ := stringArg(args, "input")
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	switch normalizedAction {
	case "wait", "write", "cancel":
	default:
		return tool.Result{}, fmt.Errorf("tool: invalid action %q", action)
	}
	if normalizedAction == "write" && len(taskIDs) > 1 {
		return tool.Result{}, fmt.Errorf("tool: TASK write supports one task_id; got %d", len(taskIDs))
	}

	if len(taskIDs) > 1 {
		result := t.callBatchTaskControl(ctx, call, normalizedAction, taskIDs, input, yieldMS, yieldDefaulted, waitUntilDone)
		return result, nil
	}
	budget := time.Duration(yieldMS) * time.Millisecond
	req := taskapi.ControlRequest{
		TaskID: taskIDs[0],
		Yield:  budget,
		Input:  input,
		Source: "agent_tool",
	}
	var snapshot taskapi.Snapshot
	var timedOut bool
	if strings.EqualFold(normalizedAction, "wait") && waitUntilDone && budget > 0 {
		var waitErr error
		snapshot, timedOut, waitErr = t.tasks.WaitUntilDone(ctx, t.sessionRef, req, budget)
		if waitErr != nil {
			return tool.Result{}, waitErr
		}
	} else {
		var controlErr error
		snapshot, controlErr = t.callTaskControl(ctx, normalizedAction, req)
		if controlErr != nil {
			return tool.Result{}, controlErr
		}
	}
	result := taskControlSnapshotToolResult(call, t.base.Definition(), snapshot, normalizedAction, waitUntilDone, timedOut)
	result.Metadata = taskToolResultEventMeta(result.Metadata, normalizedAction, input, yieldMS, yieldDefaulted, waitUntilDone, timedOut, snapshot)
	return result, nil
}

func (t runtimeTaskTool) callBatchTaskControl(ctx context.Context, call tool.Call, action string, taskIDs []string, input string, yieldMS int, yieldDefaulted bool, waitUntilDone bool) tool.Result {
	items := make([]taskBatchControlItem, 0, len(taskIDs))
	started := time.Now()
	for _, id := range taskIDs {
		yield := time.Duration(yieldMS) * time.Millisecond
		if strings.EqualFold(action, "wait") && yield > 0 {
			elapsed := time.Since(started)
			if elapsed >= yield {
				yield = 0
			} else {
				yield -= elapsed
			}
		}
		req := taskapi.ControlRequest{
			TaskID: id,
			Yield:  yield,
			Input:  input,
			Source: "agent_tool",
		}
		var snapshot taskapi.Snapshot
		var timedOut bool
		var err error
		if strings.EqualFold(action, "wait") && waitUntilDone && yield > 0 {
			snapshot, timedOut, err = t.tasks.WaitUntilDone(ctx, t.sessionRef, req, yield)
		} else {
			snapshot, err = t.callTaskControl(ctx, action, req)
		}
		if err != nil {
			items = append(items, taskBatchControlItem{TaskID: id, Err: err})
			continue
		}
		items = append(items, taskBatchControlItem{TaskID: id, Snapshot: snapshot, OK: true, TimedOut: timedOut})
	}
	result := taskBatchControlToolResult(call, t.base.Definition(), items, action, waitUntilDone)
	result.Metadata = taskBatchToolResultEventMeta(result.Metadata, action, input, yieldMS, yieldDefaulted, waitUntilDone, items)
	return result
}

func (t runtimeTaskTool) callTaskControl(ctx context.Context, action string, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	normalizedReq := normalizeTaskControlRequest(req)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait":
		return t.tasks.Wait(ctx, t.sessionRef, normalizedReq)
	case "write":
		return t.tasks.Write(ctx, t.sessionRef, normalizedReq)
	case "cancel":
		return t.tasks.Cancel(ctx, t.sessionRef, normalizedReq)
	default:
		return taskapi.Snapshot{}, fmt.Errorf("tool: invalid action %q", action)
	}
}

func splitTaskControlIDs(taskID string) []string {
	parts := strings.Split(taskID, ",")
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

func taskToolResultEventMeta(existing map[string]any, action string, input string, yieldMS int, yieldDefaulted bool, waitUntilDone bool, timedOut bool, snapshot taskapi.Snapshot) map[string]any {
	out := maps.Clone(existing)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := taskRuntimeMetaSection(out, "tool")
	toolMeta["name"] = "TASK"
	toolMeta["action"] = strings.ToLower(strings.TrimSpace(action))
	toolMeta["target_kind"] = strings.TrimSpace(string(snapshot.Kind))
	toolMeta["target_id"] = taskVisibleID(snapshot)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		toolMeta["effective_yield_time_ms"] = yieldMS
		if waitUntilDone {
			toolMeta["wait_until_done"] = true
		}
		if yieldDefaulted {
			toolMeta["yield_time_ms_defaulted"] = true
		}
		if timedOut && snapshot.Running {
			toolMeta["wait_timed_out"] = true
			toolMeta["still_running"] = true
		}
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		toolMeta["input"] = strings.TrimSpace(input)
	}
	return out
}

func taskBatchToolResultEventMeta(existing map[string]any, action string, input string, yieldMS int, yieldDefaulted bool, waitUntilDone bool, items []taskBatchControlItem) map[string]any {
	out := maps.Clone(existing)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := taskRuntimeMetaSection(out, "tool")
	toolMeta["name"] = "TASK"
	toolMeta["action"] = strings.ToLower(strings.TrimSpace(action))
	toolMeta["target_ids"] = taskBatchVisibleIDs(items)
	toolMeta["target_count"] = len(items)
	if failed := taskBatchErrorCount(items); failed > 0 {
		toolMeta["failed_count"] = failed
	}
	if kind, ok := commonTaskBatchKind(items); ok {
		toolMeta["target_kind"] = strings.TrimSpace(string(kind))
	}
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		toolMeta["effective_yield_time_ms"] = yieldMS
		if waitUntilDone {
			toolMeta["wait_until_done"] = true
		}
		if yieldDefaulted {
			toolMeta["yield_time_ms_defaulted"] = true
		}
		if timedOut := taskBatchTimedOutCount(items); timedOut > 0 {
			toolMeta["wait_timed_out"] = true
			toolMeta["still_running"] = true
			toolMeta["timed_out_count"] = timedOut
		}
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		toolMeta["input"] = strings.TrimSpace(input)
	}
	taskMeta := taskRuntimeMetaSection(out, "task")
	taskMeta["task_ids"] = taskBatchVisibleIDs(items)
	taskMeta["count"] = len(items)
	if failed := taskBatchErrorCount(items); failed > 0 {
		taskMeta["failed_count"] = failed
	}
	if timedOut := taskBatchTimedOutCount(items); timedOut > 0 {
		taskMeta["timed_out_count"] = timedOut
	}
	if kind, ok := commonTaskBatchKind(items); ok {
		taskMeta["kind"] = strings.TrimSpace(string(kind))
	}
	return out
}

func taskBatchTimedOutCount(items []taskBatchControlItem) int {
	count := 0
	for _, item := range items {
		if item.TimedOut && item.Snapshot.Running {
			count++
		}
	}
	return count
}

func taskBatchVisibleIDs(items []taskBatchControlItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.TaskID)
		if id == "" && item.OK {
			id = taskVisibleID(item.Snapshot)
		}
		if id != "" {
			out = append(out, id)
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
