package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/internal/agenthandle"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const (
	defaultBashYield             = 7 * time.Second
	bashLiveOutputBufferCapBytes = 64 * 1024
)

type taskRuntime struct {
	runtime *Runtime
	store   taskapi.Store

	mu        sync.RWMutex
	tasks     map[string]*bashTask
	subagents map[string]*subagentTask
	pending   map[string][]stream.Frame
	order     map[string][]string
	backends  map[sandbox.Backend]sandbox.Runtime
	handles   map[string]map[string]struct{}
}

type sandboxRuntimeBackends interface {
	SupportedBackends() []sandbox.Backend
}

type sandboxSessionRefOpener interface {
	OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error)
}

type bashTask struct {
	ref        taskapi.Ref
	sessionRef session.SessionRef
	session    sandbox.Session
	command    string
	workdir    string
	title      string
	createdAt  time.Time

	mu             sync.Mutex
	state          taskapi.State
	running        bool
	stdoutCursor   int64
	stderrCursor   int64
	modelCursor    int64
	output         string
	outputBase     int64
	outputLive     bool
	outputCallback bool
	result         map[string]any
	metadata       map[string]any
}

type subagentTask struct {
	ref        taskapi.Ref
	sessionRef session.SessionRef
	anchor     delegation.Anchor
	runner     subagent.Runner
	agent      string
	handle     string
	title      string
	prompt     string
	createdAt  time.Time

	mu       sync.Mutex
	state    taskapi.State
	running  bool
	result   map[string]any
	metadata map[string]any

	stdout       string
	stderr       string
	stdoutCursor int64
	stderrCursor int64
	turnSeq      int64
	streamFrames []stream.Frame
}

func newTaskRuntime(runtime *Runtime, store taskapi.Store) *taskRuntime {
	return &taskRuntime{
		runtime:   runtime,
		store:     store,
		tasks:     map[string]*bashTask{},
		subagents: map[string]*subagentTask{},
		pending:   map[string][]stream.Frame{},
		order:     map[string][]string{},
		backends:  map[sandbox.Backend]sandbox.Runtime{},
		handles:   map[string]map[string]struct{}{},
	}
}

type runtimeToolContext struct {
	mode              string
	approvalRequester agent.ApprovalRequester
	runID             string
	turnID            string
	now               func() time.Time
	grants            *permissionGrantStore
}

func (r *Runtime) wrapToolsForRuntime(activeSession session.Session, ref session.SessionRef, spec agent.AgentSpec, toolCtx runtimeToolContext) []tool.Tool {
	if len(spec.Tools) == 0 {
		return spec.Tools
	}
	out := make([]tool.Tool, 0, len(spec.Tools)+1)
	hasBash := false
	hasSpawn := false
	hasTask := false
	hasRequestPermissions := false
	for _, one := range spec.Tools {
		if one == nil {
			continue
		}
		name := strings.ToUpper(strings.TrimSpace(one.Definition().Name))
		switch name {
		case strings.ToUpper(requestPermissionsToolName):
			if !hasRequestPermissions {
				hasRequestPermissions = true
				out = append(out, runtimeRequestPermissionsTool(r.sessions, activeSession, ref, toolCtx))
			}
		case shell.BashToolName:
			hasBash = true
			if runtime, ok := sandboxRuntimeFromTool(one); ok {
				r.tasks.registerSandboxRuntime(runtime)
			}
			out = append(out, runtimeBashTool{
				base:       one,
				session:    session.CloneSession(activeSession),
				sessionRef: session.NormalizeSessionRef(ref),
				tasks:      r.tasks,
			})
		case spawn.ToolName:
			hasSpawn = true
			out = append(out, runtimeSpawnTool{
				base:       one,
				session:    session.CloneSession(activeSession),
				sessionRef: session.NormalizeSessionRef(ref),
				tasks:      r.tasks,
				runner:     r.subagents,
				mode:       strings.TrimSpace(toolCtx.mode),
				approval:   toolCtx.approvalRequester,
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
	if (hasBash || hasSpawn) && !hasTask {
		out = append(out, runtimeTaskTool{
			base:       tasktool.New(),
			sessionRef: session.NormalizeSessionRef(ref),
			tasks:      r.tasks,
		})
	}
	if !hasRequestPermissions {
		out = append(out, runtimeRequestPermissionsTool(r.sessions, activeSession, ref, toolCtx))
	}
	return out
}

func runtimeRequestPermissionsTool(sessions session.Service, activeSession session.Session, ref session.SessionRef, toolCtx runtimeToolContext) requestPermissionsTool {
	return requestPermissionsTool{
		session:    session.CloneSession(activeSession),
		sessionRef: session.NormalizeSessionRef(ref),
		sessions:   sessions,
		mode:       strings.TrimSpace(toolCtx.mode),
		runID:      strings.TrimSpace(toolCtx.runID),
		turnID:     strings.TrimSpace(toolCtx.turnID),
		now:        toolCtx.now,
		approval:   toolCtx.approvalRequester,
		grants:     toolCtx.grants,
	}
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

type runtimeBashTool struct {
	base       tool.Tool
	session    session.Session
	sessionRef session.SessionRef
	tasks      *taskRuntime
}

func (t runtimeBashTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}

func (t runtimeBashTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
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
	yieldMS := int(defaultBashYield / time.Millisecond)
	if parsed := optionalIntArg(args, "yield_time_ms"); parsed != nil {
		yieldMS = *parsed
	}
	if yieldMS < 0 {
		yieldMS = 0
	}
	req := taskapi.BashStartRequest{
		Command:     strings.TrimSpace(command),
		Workdir:     strings.TrimSpace(workdir),
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
	snapshot, err := t.tasks.StartBash(ctx, t.session, t.sessionRef, runtime, req)
	if err != nil {
		return tool.Result{}, err
	}
	return taskSnapshotToolResult(call, t.base.Definition(), snapshot), nil
}

type runtimeSpawnTool struct {
	base       tool.Tool
	session    session.Session
	sessionRef session.SessionRef
	tasks      *taskRuntime
	runner     subagent.Runner
	mode       string
	approval   agent.ApprovalRequester
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
	agent, err = resolveSpawnAgent(t.session, agent)
	if err != nil {
		return tool.Result{}, err
	}
	snapshot, err := t.tasks.StartSubagent(ctx, t.session, t.sessionRef, t.runner, taskapi.SubagentStartRequest{
		Agent:      strings.TrimSpace(agent),
		Prompt:     strings.TrimSpace(prompt),
		ParentCall: strings.TrimSpace(call.ID),
		ParentTool: strings.TrimSpace(call.Name),
		Mode:       strings.TrimSpace(t.mode),
		Approval:   newSubagentApprovalRequester(t.approval, t.session, t.sessionRef),
	})
	if err != nil {
		return tool.Result{}, err
	}
	result := taskSnapshotToolResult(call, t.base.Definition(), snapshot)
	return result, nil
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
		Mode:       strings.TrimSpace(req.Mode),
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
	yieldMS := 0
	parsedYield := optionalIntArg(args, "yield_time_ms")
	yieldDefaulted := false
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		yieldMS = int(defaultBashYield / time.Millisecond)
		yieldDefaulted = parsedYield == nil
	}
	if parsedYield != nil {
		yieldMS = *parsedYield
	}
	if yieldMS < 0 {
		yieldMS = 0
	}
	input, _ := stringArg(args, "input")
	req := taskapi.ControlRequest{
		TaskID: strings.TrimSpace(taskID),
		Yield:  time.Duration(yieldMS) * time.Millisecond,
		Input:  input,
		Source: "agent_tool",
	}
	var snapshot taskapi.Snapshot
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait":
		snapshot, err = t.tasks.Wait(ctx, t.sessionRef, req)
	case "write":
		snapshot, err = t.tasks.Write(ctx, t.sessionRef, req)
	case "cancel":
		snapshot, err = t.tasks.Cancel(ctx, t.sessionRef, req)
	default:
		return tool.Result{}, fmt.Errorf("tool: invalid action %q", action)
	}
	if err != nil {
		return tool.Result{}, err
	}
	result := taskSnapshotToolResult(call, t.base.Definition(), snapshot)
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	result.Metadata = taskToolResultEventMeta(result.Metadata, normalizedAction, input, yieldMS, yieldDefaulted, snapshot)
	return result, nil
}

func taskToolResultEventMeta(existing map[string]any, action string, input string, yieldMS int, yieldDefaulted bool, snapshot taskapi.Snapshot) map[string]any {
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
		if yieldDefaulted {
			toolMeta["yield_time_ms_defaulted"] = true
		}
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		toolMeta["input"] = strings.TrimSpace(input)
	}
	return out
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

func (tm *taskRuntime) StartBash(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runtime sandbox.Runtime,
	req taskapi.BashStartRequest,
) (taskapi.Snapshot, error) {
	var (
		task          *bashTask
		pendingOutput strings.Builder
		pendingMu     sync.Mutex
	)
	sandboxReq := sandbox.CommandRequest{
		Command: req.Command,
		Dir:     req.Workdir,
		OnOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Text == "" {
				return
			}
			pendingMu.Lock()
			current := task
			if current == nil {
				pendingOutput.WriteString(chunk.Text)
				pendingMu.Unlock()
				return
			}
			pendingMu.Unlock()
			current.appendOutput(chunk.Text)
		},
	}
	if constraints, ok := req.Constraints.(sandbox.Constraints); ok {
		sandboxReq.Constraints = constraints
		sandboxReq.RouteHint = constraints.Route
		sandboxReq.Backend = constraints.Backend
		sandboxReq.Permission = constraints.Permission
	}
	sessionHandle, err := runtime.Start(ctx, sandboxReq)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	now := tm.runtime.now()
	taskID := tm.runtime.nextID("task", nil)
	createdTask := &bashTask{
		ref: taskapi.Ref{
			TaskID:     taskID,
			SessionID:  strings.TrimSpace(sessionHandle.Ref().SessionID),
			TerminalID: strings.TrimSpace(sessionHandle.Terminal().TerminalID),
		},
		sessionRef:     session.NormalizeSessionRef(ref),
		session:        sessionHandle,
		command:        strings.TrimSpace(req.Command),
		workdir:        strings.TrimSpace(req.Workdir),
		title:          shell.BashToolName + " " + strings.TrimSpace(req.Command),
		createdAt:      now,
		state:          taskapi.StateRunning,
		running:        true,
		outputCallback: true,
	}
	pendingMu.Lock()
	task = createdTask
	if pending := pendingOutput.String(); pending != "" {
		task.appendOutputLocked(pending)
	}
	pendingMu.Unlock()
	tm.mu.Lock()
	tm.tasks[taskID] = task
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.order[sessionID] = append(tm.order[sessionID], taskID)
	tm.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, task.entrySnapshot(tm.runtime.now())); err != nil {
		return taskapi.Snapshot{}, err
	}
	if req.Observer != nil {
		status, statusErr := sessionHandle.Status(ctx)
		if statusErr != nil {
			status = sandbox.SessionStatus{
				SessionRef:    sessionHandle.Ref(),
				Terminal:      sessionHandle.Terminal(),
				Running:       true,
				SupportsInput: true,
				UpdatedAt:     now,
			}
		}
		task.mu.Lock()
		snapshot := task.snapshotLocked(status)
		task.mu.Unlock()
		req.Observer.ObserveTaskSnapshot(snapshot)
	}
	snapshot, err := tm.waitBash(ctx, task, req.Yield)
	if err != nil {
		return tm.failBashTaskIfStopped(ctx, task, err)
	}
	return snapshot, nil
}

type taskToolObserver struct {
	call     tool.Call
	def      tool.Definition
	observer tool.Observer
}

func (o taskToolObserver) ObserveTaskSnapshot(snapshot taskapi.Snapshot) {
	if o.observer == nil {
		return
	}
	o.observer.ObserveToolResult(taskSnapshotToolResult(o.call, o.def, snapshot))
}

func (tm *taskRuntime) StartSubagent(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runner subagent.Runner,
	req taskapi.SubagentStartRequest,
) (taskapi.Snapshot, error) {
	if runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: subagent runner is required")
	}
	taskID := tm.runtime.nextID("task", nil)
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = strings.TrimSpace(tm.runtime.defaultPolicyMode)
	}
	childPrompt := subagentPromptWithContext(req.ContextPrelude, req.Prompt)
	anchor, result, err := runner.Spawn(ctx, subagent.SpawnContext{
		SessionRef:        session.NormalizeSessionRef(ref),
		Session:           session.CloneSession(activeSession),
		CWD:               strings.TrimSpace(activeSession.CWD),
		TaskID:            taskID,
		ParentCallID:      strings.TrimSpace(req.ParentCall),
		Mode:              mode,
		ApprovalRequester: req.Approval,
		Streams:           tm,
	}, delegation.Request{
		Agent:  strings.TrimSpace(req.Agent),
		Prompt: childPrompt,
	})
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	anchor.TaskID = taskID
	now := tm.runtime.now()
	task := &subagentTask{
		ref: taskapi.Ref{
			TaskID:     taskID,
			SessionID:  strings.TrimSpace(anchor.SessionID),
			TerminalID: subagentTerminalID(taskID),
		},
		sessionRef: session.NormalizeSessionRef(ref),
		anchor:     delegation.CloneAnchor(anchor),
		runner:     runner,
		agent:      strings.TrimSpace(anchor.Agent),
		handle:     tm.reserveSubagentHandle(activeSession, ref, anchor.Agent),
		title:      spawn.ToolName + " " + strings.TrimSpace(anchor.Agent),
		prompt:     strings.TrimSpace(req.Prompt),
		createdAt:  now,
		state:      taskStateFromDelegation(result.State),
		running:    result.State == delegation.StateRunning,
		turnSeq:    1,
		metadata: map[string]any{
			"source":      firstNonEmpty(strings.TrimSpace(req.Source), "agent_spawn"),
			"interaction": subagentInteraction(req.ParentTool, req.Source),
		},
	}
	task.applyResult(result)
	task.seedStreamFromResult(result)
	tm.mu.Lock()
	tm.subagents[taskID] = task
	pending := append([]stream.Frame(nil), tm.pending[taskID]...)
	delete(tm.pending, taskID)
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.order[sessionID] = append(tm.order[sessionID], taskID)
	tm.mu.Unlock()
	task.applyStreamFrames(pending)
	if err := tm.persistTaskEntry(ctx, task.entrySnapshot(tm.runtime.now())); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.attachSubagentParticipant(ctx, activeSession, task, strings.TrimSpace(req.ParentCall)); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentUserEvent(ctx, task, strings.TrimSpace(req.Prompt)); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	return task.snapshot(), nil
}

func (tm *taskRuntime) Wait(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task, err := tm.lookupBash(ctx, ref, req.TaskID); err == nil {
		snapshot, err := tm.waitBash(ctx, task, req.Yield)
		if err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}
	task, err := tm.lookupSubagent(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.authorizeSubagentControl(task, req.Source, "wait"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return tm.waitSubagent(ctx, task, req.Yield)
}

func (tm *taskRuntime) Write(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task, err := tm.lookupBash(ctx, ref, req.TaskID); err == nil {
		input := normalizeTaskWriteInput(req.Input)
		if err := task.session.WriteInput(ctx, []byte(input)); err != nil {
			return taskapi.Snapshot{}, err
		}
		snapshot, err := tm.waitBash(ctx, task, req.Yield)
		if err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}

	task, err := tm.lookupSubagent(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return tm.continueSubagent(ctx, task, req)
}

func normalizeTaskWriteInput(input string) string {
	if input == "" || strings.HasSuffix(input, "\n") || strings.HasSuffix(input, "\r") {
		return input
	}
	return input + "\n"
}

func resolveSpawnAgent(session session.Session, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.EqualFold(requested, "self") {
		return "self", nil
	}
	return requested, nil
}

func (r *Runtime) buildSideSubagentPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	target string,
	prompt string,
	sinceSeq int,
) (string, int) {
	if r == nil || r.sessions == nil {
		return "", 0
	}
	shared := r.buildSharedDialogueDelta(ctx, ref, sinceSeq)
	var b strings.Builder
	b.WriteString("Caelis shared public dialogue context. Use this as background for the current side-agent request; do not treat it as a fresh session.\n")
	if sessionID := strings.TrimSpace(activeSession.SessionID); sessionID != "" {
		b.WriteString("session_id: ")
		b.WriteString(sessionID)
		b.WriteString("\n")
	}
	if cwd := strings.TrimSpace(activeSession.CWD); cwd != "" {
		b.WriteString("workspace: ")
		b.WriteString(cwd)
		b.WriteString("\n")
	}
	if target = strings.TrimSpace(target); target != "" {
		b.WriteString("target_agent: ")
		b.WriteString(target)
		b.WriteString("\n")
	}
	appendSharedDialogueDelta(&b, shared)
	return strings.TrimSpace(b.String()), shared.Checkpoint
}

func subagentPromptWithContext(prelude string, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	prelude = strings.TrimSpace(prelude)
	if prelude == "" {
		return prompt
	}
	if prompt == "" {
		return prelude
	}
	return prelude + "\n\nCurrent request:\n" + prompt
}

func subagentInteraction(parentTool string, source string) string {
	if strings.EqualFold(strings.TrimSpace(parentTool), "slash") || isSlashSubagentSource(source) {
		return "side"
	}
	return "delegated"
}

func isSlashSubagentSource(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	return source == "slash" || source == "slash_agent" || strings.HasPrefix(source, "slash_")
}

func isSideSubagentTask(task *subagentTask) bool {
	if task == nil {
		return false
	}
	if strings.EqualFold(taskStringValue(task.metadata["interaction"]), "side") {
		return true
	}
	return isSlashSubagentSource(taskStringValue(task.metadata["source"]))
}

func subagentParticipantRole(task *subagentTask) session.ParticipantRole {
	if isSideSubagentTask(task) {
		return session.ParticipantRoleSidecar
	}
	return session.ParticipantRoleDelegated
}

func (tm *taskRuntime) authorizeSubagentControl(task *subagentTask, source string, action string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "agent_tool":
		if isSideSubagentTask(task) {
			return fmt.Errorf("impl/agent/local: TASK %s cannot control user-created side subagent %q", strings.TrimSpace(action), task.handle)
		}
	case "user_side_agent":
		if !isSideSubagentTask(task) {
			return fmt.Errorf("impl/agent/local: @handle can only target side subagents created with /<agent>")
		}
	}
	return nil
}

type StartSubagentOptions struct {
	ApprovalRequester agent.ApprovalRequester
}

func (r *Runtime) StartSubagent(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
) (taskapi.Snapshot, error) {
	return r.StartSubagentWithOptions(ctx, ref, agent, prompt, source, StartSubagentOptions{})
}

func (r *Runtime) StartSubagentWithOptions(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (taskapi.Snapshot, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: runtime is unavailable")
	}
	if r.subagents == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: subagent runner is unavailable")
	}
	ref = session.NormalizeSessionRef(ref)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	agent, err = resolveSpawnAgent(activeSession, agent)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: subagent prompt is required")
	}
	contextPrelude, _ := r.buildSideSubagentPromptContext(ctx, activeSession, ref, strings.TrimSpace(agent), strings.TrimSpace(prompt), 0)
	snapshot, err := r.tasks.StartSubagent(ctx, activeSession, ref, r.subagents, taskapi.SubagentStartRequest{
		Agent:          strings.TrimSpace(agent),
		Prompt:         strings.TrimSpace(prompt),
		ContextPrelude: contextPrelude,
		ParentTool:     "slash",
		Source:         firstNonEmpty(strings.TrimSpace(source), "slash_agent"),
		Mode:           strings.TrimSpace(r.defaultPolicyMode),
		Approval:       newSubagentApprovalRequester(opts.ApprovalRequester, activeSession, ref),
	})
	if err != nil || !snapshot.Running {
		return snapshot, err
	}
	return r.tasks.Wait(ctx, ref, taskapi.ControlRequest{
		TaskID: snapshot.Ref.TaskID,
		Yield:  2 * time.Second,
		Source: "ui_side_agent",
	})
}

func (r *Runtime) ContinueSubagentByHandle(
	ctx context.Context,
	ref session.SessionRef,
	handle string,
	prompt string,
	yield time.Duration,
) (taskapi.Snapshot, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: runtime is unavailable")
	}
	ref = session.NormalizeSessionRef(ref)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	taskID, binding, ok := subagentTaskIDForHandle(activeSession, handle)
	if !ok {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: subagent handle %q not found", strings.TrimSpace(handle))
	}
	contextPrelude, _ := r.buildSideSubagentPromptContext(ctx, activeSession, ref, strings.TrimSpace(handle), strings.TrimSpace(prompt), binding.ContextSyncSeq)
	return r.tasks.Write(ctx, ref, taskapi.ControlRequest{
		TaskID:         taskID,
		Input:          strings.TrimSpace(prompt),
		Yield:          yield,
		Source:         "user_side_agent",
		ContextPrelude: contextPrelude,
	})
}

func (r *Runtime) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yield time.Duration,
) (taskapi.Snapshot, error) {
	if r == nil || r.tasks == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: runtime is unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: subagent task id is required")
	}
	return r.tasks.Wait(ctx, session.NormalizeSessionRef(ref), taskapi.ControlRequest{
		TaskID: taskID,
		Yield:  yield,
		Source: "ui_side_agent",
	})
}

func subagentTaskIDForHandle(activeSession session.Session, handle string) (string, session.ParticipantBinding, bool) {
	handle = normalizeSubagentHandle(handle)
	if handle == "" {
		return "", session.ParticipantBinding{}, false
	}
	for _, participant := range activeSession.Participants {
		if participant.Kind != session.ParticipantKindSubagent || participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		if normalizeSubagentHandle(participant.Label) != handle {
			continue
		}
		taskID := strings.TrimSpace(participant.DelegationID)
		return taskID, session.CloneParticipantBinding(participant), taskID != ""
	}
	return "", session.ParticipantBinding{}, false
}

func (tm *taskRuntime) Cancel(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task, err := tm.lookupBash(ctx, ref, req.TaskID); err == nil {
		if err := task.session.Terminate(ctx); err != nil {
			return taskapi.Snapshot{}, err
		}
		snapshot, err := tm.waitBash(ctx, task, 10*time.Millisecond)
		if err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}
	task, err := tm.lookupSubagent(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.authorizeSubagentControl(task, req.Source, "cancel"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return tm.cancelSubagent(ctx, task)
}

func (tm *taskRuntime) waitBash(ctx context.Context, task *bashTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	status, err := task.session.Wait(ctx, yield)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	stdout, stderr, nextStdout, nextStderr, err := task.session.ReadOutput(ctx, task.stdoutCursor, task.stderrCursor)
	if err != nil {
		return taskapi.Snapshot{}, err
	}

	task.mu.Lock()
	task.stdoutCursor = nextStdout
	task.stderrCursor = nextStderr
	if !task.outputCallback {
		task.appendOutputLocked(terminalDeltaText(string(stdout), string(stderr)))
	}
	outputText := task.output
	outputCursor := task.outputCursorLocked()
	state := stateFromStatus(status)
	task.state = state
	task.running = status.Running
	task.metadata = map[string]any{
		"task_id":     task.ref.TaskID,
		"task_kind":   string(taskapi.KindBash),
		"state":       string(state),
		"running":     status.Running,
		"session_id":  task.ref.SessionID,
		"terminal_id": task.ref.TerminalID,
	}
	if status.Terminal.TerminalID != "" {
		task.metadata["terminal_id"] = status.Terminal.TerminalID
	}
	if status.Running {
		latestOutput := compactLatestOutput(task.outputFromCursorLocked(task.modelCursor))
		task.modelCursor = outputCursor
		task.metadata["output_cursor"] = outputCursor
		task.metadata["model_output_cursor"] = task.modelCursor
		task.result = map[string]any{
			"task_id": task.ref.TaskID,
			"state":   string(state),
		}
		if latestOutput != "" {
			task.result["latest_output"] = latestOutput
		}
		snapshot := task.snapshotLocked(status)
		entry := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		if err := tm.persistTaskEntry(ctx, entry); err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}

	result, resultErr := task.session.Result(ctx)
	stdoutText := result.Stdout
	stderrText := result.Stderr
	finalText := terminalFinalText(outputText, stdoutText, stderrText, resultErr)
	task.metadata["output_cursor"] = int64(len([]byte(finalText)))
	task.metadata["model_output_cursor"] = int64(len([]byte(finalText)))
	task.result = map[string]any{
		"result": finalText,
		"state":  string(state),
	}
	if commandExitCodeAvailable(state, result.ExitCode, resultErr) {
		task.result["exit_code"] = result.ExitCode
	}
	if detail, ok := sandbox.SandboxPermissionDetail(result, resultErr); ok {
		task.result["error"] = detail
		task.result["error_code"] = string(tool.ErrorCodeSandboxDenied)
	} else if resultErr != nil && strings.TrimSpace(finalText) == "(no output)" {
		task.result["error"] = strings.TrimSpace(resultErr.Error())
		if code, _ := tool.ErrorPayload(resultErr)["error_code"].(string); code != "" {
			task.result["error_code"] = code
		}
	}
	snapshot := task.snapshotLocked(status)
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	tm.mu.Lock()
	delete(tm.tasks, task.ref.TaskID)
	tm.mu.Unlock()
	return snapshot, nil
}

func (tm *taskRuntime) failBashTaskIfStopped(ctx context.Context, task *bashTask, cause error) (taskapi.Snapshot, error) {
	if task == nil || task.session == nil {
		return tm.failBashTask(ctx, task, cause)
	}
	if err := ctx.Err(); err != nil {
		return taskapi.Snapshot{}, cause
	}
	status, statusErr := task.session.Status(context.WithoutCancel(ctx))
	if statusErr == nil && status.Running {
		return taskapi.Snapshot{}, cause
	}
	return tm.failBashTask(ctx, task, cause)
}

func (tm *taskRuntime) failBashTask(ctx context.Context, task *bashTask, cause error) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	reason := strings.TrimSpace(fmt.Sprint(cause))
	if reason == "" {
		reason = "bash task failed"
	}
	state := taskapi.StateFailed
	if errors.Is(cause, context.Canceled) {
		state = taskapi.StateInterrupted
	}
	persistCtx := context.WithoutCancel(ctx)
	if task.session != nil {
		_ = task.session.Terminate(persistCtx)
	}
	now := tm.runtime.now()
	status := sandbox.SessionStatus{
		Running:   false,
		ExitCode:  -1,
		UpdatedAt: now,
	}
	if task.session != nil {
		status.SessionRef = task.session.Ref()
		status.Terminal = task.session.Terminal()
	} else {
		status.SessionRef = sandbox.SessionRef{SessionID: task.ref.SessionID}
		status.Terminal = sandbox.TerminalRef{
			SessionID:  task.ref.SessionID,
			TerminalID: task.ref.TerminalID,
		}
	}

	task.mu.Lock()
	task.state = state
	task.running = false
	task.metadata = map[string]any{
		"task_id":     task.ref.TaskID,
		"task_kind":   string(taskapi.KindBash),
		"state":       string(state),
		"running":     false,
		"session_id":  task.ref.SessionID,
		"terminal_id": task.ref.TerminalID,
	}
	if status.Terminal.TerminalID != "" {
		task.metadata["terminal_id"] = status.Terminal.TerminalID
	}
	task.result = map[string]any{
		"state":      string(state),
		"error":      reason,
		"error_code": string(tool.ErrorCodeInvalidInput),
		"result":     reason,
	}
	snapshot := task.snapshotLocked(status)
	entry := task.entrySnapshot(now)
	task.mu.Unlock()
	persistErr := tm.persistTaskEntry(persistCtx, entry)
	tm.mu.Lock()
	delete(tm.tasks, task.ref.TaskID)
	tm.mu.Unlock()
	if persistErr != nil {
		return snapshot, persistErr
	}
	return snapshot, nil
}

func (tm *taskRuntime) waitSubagent(ctx context.Context, task *subagentTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	if task.runner == nil {
		task.mu.Lock()
		snapshot := task.snapshot()
		task.mu.Unlock()
		return snapshot, nil
	}
	if !task.isRunning() {
		task.mu.Lock()
		snapshot := task.snapshot()
		task.mu.Unlock()
		return snapshot, nil
	}
	result, err := task.runner.Wait(ctx, delegation.CloneAnchor(task.anchor), int(yield/time.Millisecond))
	if err != nil {
		if task.isRunning() {
			return tm.interruptSubagentTask(ctx, task, "subagent session interrupted during recovery: "+strings.TrimSpace(err.Error()))
		}
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	task.applyResult(result)
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		delete(tm.subagents, task.ref.TaskID)
		tm.mu.Unlock()
		_ = tm.updateSubagentParticipant(ctx, task, "updated")
	}
	return snapshot, nil
}

func (tm *taskRuntime) continueSubagent(ctx context.Context, task *subagentTask, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	prompt := strings.TrimSpace(req.Input)
	if prompt == "" {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: TASK write for SPAWN task %q requires a follow-up prompt", task.ref.TaskID)
	}
	task.mu.Lock()
	state := task.state
	running := task.running
	task.mu.Unlock()
	if running || state != taskapi.StateCompleted {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: SPAWN task %q is %s; use TASK wait until completed before TASK write", task.ref.TaskID, state)
	}
	if task.runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: SPAWN task %q cannot continue because its child session runner is unavailable", task.ref.TaskID)
	}
	if err := tm.authorizeSubagentControl(task, req.Source, "write"); err != nil {
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	previousStdout := task.stdout
	previousStderr := task.stderr
	previousStdoutCursor := task.stdoutCursor
	previousStderrCursor := task.stderrCursor
	previousStreamFrames := append([]stream.Frame(nil), task.streamFrames...)
	previousTurnSeq := task.turnSeq
	task.turnSeq++
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	task.stdout = ""
	task.stderr = ""
	task.stdoutCursor = 0
	task.stderrCursor = 0
	task.streamFrames = nil
	if task.metadata != nil {
		delete(task.metadata, "final_event_persisted")
	}
	task.mu.Unlock()
	childPrompt := subagentPromptWithContext(req.ContextPrelude, prompt)
	result, err := task.runner.Continue(ctx, delegation.CloneAnchor(task.anchor), delegation.ContinueRequest{
		Agent:       task.agent,
		Prompt:      childPrompt,
		YieldTimeMS: int(req.Yield / time.Millisecond),
	})
	if err != nil {
		task.mu.Lock()
		if task.stdout == "" && task.stderr == "" {
			task.stdout = previousStdout
			task.stderr = previousStderr
			task.stdoutCursor = previousStdoutCursor
			task.stderrCursor = previousStderrCursor
			task.streamFrames = previousStreamFrames
			task.turnSeq = previousTurnSeq
		}
		task.mu.Unlock()
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentUserEvent(ctx, task, prompt); err != nil {
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	task.prompt = prompt
	task.applyResult(result)
	task.seedStreamFromResult(result)
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		delete(tm.subagents, task.ref.TaskID)
		tm.mu.Unlock()
	}
	_ = tm.updateSubagentParticipant(ctx, task, "updated")
	return snapshot, nil
}

func (tm *taskRuntime) cancelSubagent(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	if task.runner == nil {
		task.mu.Lock()
		task.state = taskapi.StateCancelled
		task.running = false
		snapshot := task.snapshot()
		entry := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		if err := tm.persistTaskEntry(ctx, entry); err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}
	if err := task.runner.Cancel(ctx, delegation.CloneAnchor(task.anchor)); err != nil {
		return taskapi.Snapshot{}, err
	}
	result, err := task.runner.Wait(ctx, delegation.CloneAnchor(task.anchor), 10)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	task.applyResult(result)
	task.state = taskapi.StateCancelled
	task.running = false
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	tm.mu.Lock()
	delete(tm.subagents, task.ref.TaskID)
	tm.mu.Unlock()
	_ = tm.updateSubagentParticipant(ctx, task, "detached")
	return snapshot, nil
}

func shouldDropInactiveSubagentTask(snapshot taskapi.Snapshot) bool {
	return !snapshot.Running && snapshot.State != taskapi.StateCompleted
}

func (tm *taskRuntime) lookupBash(ctx context.Context, ref session.SessionRef, taskID string) (*bashTask, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[strings.TrimSpace(taskID)]
	tm.mu.RUnlock()
	if ok && task != nil {
		if strings.TrimSpace(task.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
		}
		return task, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, strings.TrimSpace(taskID))
	if err != nil || entry == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	if entry.Kind != taskapi.KindBash {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	rehydrated, err := tm.rehydrateBashTask(entry)
	if err != nil {
		return nil, err
	}
	tm.mu.Lock()
	tm.tasks[rehydrated.ref.TaskID] = rehydrated
	tm.mu.Unlock()
	return rehydrated, nil
}

func (tm *taskRuntime) lookupSubagent(ctx context.Context, ref session.SessionRef, taskID string) (*subagentTask, error) {
	lookupID := strings.TrimSpace(taskID)
	tm.mu.RLock()
	task, ok := tm.subagents[lookupID]
	if !ok {
		handle := normalizeSubagentHandle(lookupID)
		var matches []*subagentTask
		for _, candidate := range tm.subagents {
			if candidate == nil {
				continue
			}
			if strings.TrimSpace(candidate.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
				continue
			}
			if normalizeSubagentHandle(candidate.handle) == handle || normalizeSubagentHandle(taskStringValue(candidate.metadata["handle"])) == handle {
				matches = append(matches, candidate)
			}
		}
		if len(matches) == 1 {
			task = matches[0]
			ok = true
		} else if len(matches) > 1 {
			tm.mu.RUnlock()
			return nil, fmt.Errorf("impl/agent/local: subagent handle %q is ambiguous; use the task id", lookupID)
		}
	}
	tm.mu.RUnlock()
	if ok && task != nil {
		if strings.TrimSpace(task.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
		}
		return task, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, lookupID)
	if err != nil || entry == nil {
		entry, err = tm.lookupStoredSubagentByHandle(ctx, ref, lookupID)
	}
	if err != nil || entry == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) || entry.Kind != taskapi.KindSubagent {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	rehydrated := tm.rehydrateSubagentTask(entry)
	tm.mu.Lock()
	tm.subagents[rehydrated.ref.TaskID] = rehydrated
	tm.rememberSubagentHandleLocked(rehydrated.sessionRef.SessionID, rehydrated.handle)
	tm.mu.Unlock()
	return rehydrated, nil
}

func (tm *taskRuntime) lookupStoredSubagentByHandle(ctx context.Context, ref session.SessionRef, handle string) (*taskapi.Entry, error) {
	if tm == nil || tm.store == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", handle)
	}
	handle = normalizeSubagentHandle(handle)
	if handle == "" {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", handle)
	}
	entries, err := tm.store.ListSession(ctx, ref)
	if err != nil {
		return nil, err
	}
	var matches []*taskapi.Entry
	for _, entry := range entries {
		if entry == nil || entry.Kind != taskapi.KindSubagent {
			continue
		}
		if normalizeSubagentHandle(taskSpecString(entry.Spec, "handle")) == handle ||
			normalizeSubagentHandle(taskStringValue(entry.Metadata["handle"])) == handle ||
			normalizeSubagentHandle(taskStringValue(entry.Result["handle"])) == handle {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 1 {
		return taskapi.CloneEntry(matches[0]), nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("impl/agent/local: subagent handle %q is ambiguous; use the task id", handle)
	}
	return nil, fmt.Errorf("impl/agent/local: task %q not found", handle)
}

func (t *bashTask) snapshotLocked(status sandbox.SessionStatus) taskapi.Snapshot {
	return taskapi.CloneSnapshot(taskapi.Snapshot{
		Ref:            t.ref,
		Kind:           taskapi.KindBash,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  status.SupportsInput,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      status.UpdatedAt,
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		Result:         canonicalTaskResult(t.result),
		Metadata:       maps.Clone(t.metadata),
		Terminal:       status.Terminal,
	})
}

func canonicalTaskResult(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	out, _ := tool.TruncateMap(result, tool.DefaultTruncationPolicy())
	return out
}

func taskSnapshotToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot) tool.Result {
	payload := taskToolPayload(snapshot)
	if payload == nil {
		payload = map[string]any{}
	}
	payload, _ = tool.TruncateMap(payload, tool.DefaultTruncationPolicy())
	meta := taskToolMeta(snapshot)
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:       strings.TrimSpace(call.ID),
		Name:     strings.TrimSpace(def.Name),
		Content:  []model.Part{model.NewJSONPart(raw)},
		Metadata: meta,
	}
}

func taskToolMeta(snapshot taskapi.Snapshot) map[string]any {
	meta := map[string]any{}
	taskMeta := taskRuntimeMetaSection(meta, "task")
	visibleTaskID := taskVisibleID(snapshot)
	taskMeta["kind"] = strings.TrimSpace(string(snapshot.Kind))
	taskMeta["state"] = strings.TrimSpace(string(snapshot.State))
	taskMeta["running"] = snapshot.Running
	taskMeta["task_id"] = visibleTaskID
	if sessionID := strings.TrimSpace(snapshot.Ref.SessionID); sessionID != "" {
		taskMeta["session_id"] = sessionID
	}
	if internalTaskID := strings.TrimSpace(snapshot.Ref.TaskID); snapshot.Kind != taskapi.KindSubagent && internalTaskID != "" && internalTaskID != visibleTaskID {
		taskMeta["internal_task_id"] = internalTaskID
	}
	if cursor, ok := taskInt64Value(snapshot.Metadata["output_cursor"]); ok && cursor >= 0 {
		taskMeta["output_cursor"] = cursor
	} else if snapshot.Kind == taskapi.KindSubagent && snapshot.StdoutCursor >= 0 {
		taskMeta["output_cursor"] = snapshot.StdoutCursor
	} else if snapshot.Kind != taskapi.KindSubagent {
		if text, _ := snapshot.Result["result"].(string); text != "" {
			taskMeta["output_cursor"] = int64(len([]byte(text)))
		}
	}
	if terminalID := firstNonEmpty(strings.TrimSpace(snapshot.Terminal.TerminalID), strings.TrimSpace(snapshot.Ref.TerminalID), taskStringValue(snapshot.Metadata["terminal_id"])); terminalID != "" {
		taskMeta["terminal_id"] = terminalID
	}
	for _, key := range []string{"source", "interaction", "agent", "agent_id", "handle", "mention", "prompt", "turn_id", "turn_seq"} {
		if value, ok := snapshot.Metadata[key]; ok {
			taskMeta[key] = value
		}
	}
	return meta
}

func taskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	if snapshot.Kind == taskapi.KindSubagent {
		return subagentTaskToolPayload(snapshot)
	}
	return bashTaskToolPayload(snapshot)
}

func bashTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	visibleTaskID := taskVisibleID(snapshot)
	payload := map[string]any{}
	if snapshot.Running {
		payload["task_id"] = visibleTaskID
		payload["state"] = string(snapshot.State)
		if latestOutput, _ := snapshot.Result["latest_output"].(string); strings.TrimSpace(latestOutput) != "" {
			payload["latest_output"] = latestOutput
		}
		return payload
	}
	payload["state"] = string(snapshot.State)
	if text, _ := snapshot.Result["result"].(string); text != "" {
		payload["result"] = text
	}
	if errText, _ := snapshot.Result["error"].(string); strings.TrimSpace(errText) != "" {
		payload["error"] = strings.TrimSpace(errText)
	}
	if exitCode, ok := snapshot.Result["exit_code"]; ok {
		payload["exit_code"] = exitCode
	}
	return payload
}

func subagentTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	payload := map[string]any{
		"task_id": taskVisibleID(snapshot),
		"state":   string(snapshot.State),
	}
	if snapshot.Running {
		if preview := strings.TrimSpace(taskStringValue(snapshot.Result["output_preview"])); preview != "" {
			payload["text"] = preview
		}
		return payload
	}
	finalMessage := firstNonEmpty(taskStringValue(snapshot.Result["final_message"]), taskStringValue(snapshot.Result["result"]))
	if strings.TrimSpace(finalMessage) != "" {
		payload["final_message"] = strings.TrimSpace(finalMessage)
	}
	if errText := strings.TrimSpace(taskStringValue(snapshot.Result["error"])); errText != "" {
		payload["error"] = errText
	}
	return payload
}

func taskVisibleID(snapshot taskapi.Snapshot) string {
	if snapshot.Kind == taskapi.KindSubagent {
		if handle := firstNonEmpty(taskStringValue(snapshot.Result["handle"]), taskStringValue(snapshot.Metadata["handle"])); handle != "" {
			return normalizeSubagentHandle(handle)
		}
	}
	return strings.TrimSpace(snapshot.Ref.TaskID)
}

func stateFromStatus(status sandbox.SessionStatus) taskapi.State {
	if status.Running {
		return taskapi.StateRunning
	}
	if status.ExitCode == 0 {
		return taskapi.StateCompleted
	}
	if status.ExitCode == -1 {
		return taskapi.StateCancelled
	}
	return taskapi.StateFailed
}

func (tm *taskRuntime) persistTaskEntry(ctx context.Context, entry *taskapi.Entry) error {
	if tm == nil || tm.store == nil || entry == nil {
		return nil
	}
	return tm.store.Upsert(ctx, entry)
}

func (tm *taskRuntime) hasActiveSubagentTask(entry *taskapi.Entry) bool {
	if tm == nil || entry == nil {
		return false
	}
	taskID := strings.TrimSpace(entry.TaskID)
	sessionID := strings.TrimSpace(entry.Session.SessionID)
	if taskID == "" || sessionID == "" {
		return false
	}
	tm.mu.RLock()
	task := tm.subagents[taskID]
	tm.mu.RUnlock()
	if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.running
}

func interruptedSubagentEntry(entry *taskapi.Entry, reason string) *taskapi.Entry {
	next := taskapi.CloneEntry(entry)
	if next == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "subagent interrupted during resume"
	}
	next.Running = false
	next.State = taskapi.StateInterrupted
	if next.Result == nil {
		next.Result = map[string]any{}
	}
	next.Result["state"] = string(taskapi.StateInterrupted)
	next.Result["error"] = reason
	next.Result["result"] = reason
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["state"] = string(taskapi.StateInterrupted)
	next.Metadata["interrupted_reason"] = reason
	return next
}

func (tm *taskRuntime) interruptSubagentTask(ctx context.Context, task *subagentTask, reason string) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	task.mu.Lock()
	task.applyInterruptedLocked(reason)
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	_ = tm.updateSubagentParticipant(ctx, task, "updated")
	return snapshot, nil
}

func (tm *taskRuntime) PublishStream(frame stream.Frame) {
	if tm == nil {
		return
	}
	taskID := strings.TrimSpace(frame.Ref.TaskID)
	sessionID := strings.TrimSpace(frame.Ref.SessionID)
	tm.mu.RLock()
	task := tm.subagents[taskID]
	if task == nil && sessionID != "" {
		for _, candidate := range tm.subagents {
			if candidate == nil {
				continue
			}
			if strings.TrimSpace(candidate.anchor.SessionID) == sessionID {
				task = candidate
				break
			}
		}
	}
	tm.mu.RUnlock()
	if task == nil {
		if taskID != "" {
			tm.mu.Lock()
			tm.pending[taskID] = append(tm.pending[taskID], stream.CloneFrame(frame))
			tm.mu.Unlock()
		}
		return
	}
	task.applyStreamFrames([]stream.Frame{frame})
}

func (tm *taskRuntime) listSessionEntries(ctx context.Context, ref session.SessionRef) []*taskapi.Entry {
	if tm == nil {
		return nil
	}
	if tm.store != nil {
		listed, err := tm.store.ListSession(ctx, ref)
		if err == nil && len(listed) > 0 {
			out := make([]*taskapi.Entry, 0, len(listed))
			for _, entry := range listed {
				out = append(out, taskapi.CloneEntry(entry))
			}
			return out
		}
	}
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.mu.RLock()
	ids := append([]string(nil), tm.order[sessionID]...)
	tm.mu.RUnlock()
	out := make([]*taskapi.Entry, 0, len(ids))
	for _, taskID := range ids {
		tm.mu.RLock()
		if task, ok := tm.tasks[taskID]; ok && task != nil {
			task.mu.Lock()
			out = append(out, task.entrySnapshot(tm.runtime.now()))
			task.mu.Unlock()
			tm.mu.RUnlock()
			continue
		}
		if task, ok := tm.subagents[taskID]; ok && task != nil {
			task.mu.Lock()
			out = append(out, task.entrySnapshot(tm.runtime.now()))
			task.mu.Unlock()
		}
		tm.mu.RUnlock()
	}
	return out
}

func (tm *taskRuntime) rehydrateBashTask(entry *taskapi.Entry) (*bashTask, error) {
	if entry == nil {
		return nil, fmt.Errorf("impl/agent/local: task entry is required")
	}
	task := &bashTask{
		ref: taskapi.Ref{
			TaskID:     strings.TrimSpace(entry.TaskID),
			SessionID:  strings.TrimSpace(entry.Terminal.SessionID),
			TerminalID: strings.TrimSpace(entry.Terminal.TerminalID),
		},
		sessionRef:   session.NormalizeSessionRef(entry.Session),
		command:      taskSpecString(entry.Spec, "command"),
		workdir:      taskSpecString(entry.Spec, "workdir"),
		title:        strings.TrimSpace(entry.Title),
		createdAt:    entry.CreatedAt,
		state:        entry.State,
		running:      entry.Running,
		stdoutCursor: entry.StdoutCursor,
		stderrCursor: entry.StderrCursor,
		output:       taskStringValue(entry.Result["result"]),
		result:       maps.Clone(entry.Result),
		metadata:     maps.Clone(entry.Metadata),
	}
	if cursor, ok := taskInt64Value(entry.Metadata["model_output_cursor"]); ok && cursor >= 0 {
		task.modelCursor = cursor
	}
	if !entry.Running {
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		return task, nil
	}
	backend := entry.Terminal.Backend
	if backend == "" {
		backend = sandbox.BackendHost
	}
	tm.mu.RLock()
	runtime := tm.backends[backend]
	tm.mu.RUnlock()
	if runtime == nil {
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		task.running = false
		task.state = taskapi.StateInterrupted
		task.result = maps.Clone(entry.Result)
		if task.result == nil {
			task.result = map[string]any{}
		}
		task.result["state"] = string(taskapi.StateInterrupted)
		task.result["error"] = "task interrupted during resume"
		task.result["result"] = "task interrupted during resume"
		return task, nil
	}
	var (
		session sandbox.Session
		err     error
	)
	if opener, ok := runtime.(sandboxSessionRefOpener); ok && opener != nil {
		session, err = opener.OpenSessionRef(sandbox.SessionRef{
			Backend:   backend,
			SessionID: strings.TrimSpace(entry.Terminal.SessionID),
		})
	} else {
		session, err = runtime.OpenSession(strings.TrimSpace(entry.Terminal.SessionID))
	}
	if err != nil {
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		task.running = false
		task.state = taskapi.StateInterrupted
		if task.result == nil {
			task.result = map[string]any{}
		}
		task.result["state"] = string(taskapi.StateInterrupted)
		task.result["error"] = "task interrupted during resume"
		task.result["result"] = "task interrupted during resume"
		return task, nil
	}
	task.session = session
	return task, nil
}

func (tm *taskRuntime) rehydrateSubagentTask(entry *taskapi.Entry) *subagentTask {
	if entry == nil {
		return nil
	}
	agent := taskSpecString(entry.Spec, "agent")
	task := &subagentTask{
		ref: taskapi.Ref{
			TaskID:     strings.TrimSpace(entry.TaskID),
			SessionID:  taskSpecString(entry.Spec, "session_id"),
			TerminalID: firstNonEmpty(taskSpecString(entry.Spec, "terminal_id"), subagentTerminalID(entry.TaskID)),
		},
		sessionRef: session.NormalizeSessionRef(entry.Session),
		anchor: delegation.Anchor{
			TaskID:    strings.TrimSpace(entry.TaskID),
			SessionID: taskSpecString(entry.Spec, "session_id"),
			Agent:     agent,
			AgentID:   taskSpecString(entry.Spec, "agent_id"),
		},
		runner:    tm.runtime.subagents,
		agent:     agent,
		handle:    firstNonEmpty(taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"])),
		title:     strings.TrimSpace(entry.Title),
		prompt:    taskSpecString(entry.Spec, "prompt"),
		createdAt: entry.CreatedAt,
		state:     entry.State,
		running:   entry.Running,
		turnSeq:   taskTurnSeqFromSpec(entry.Spec),
		result:    maps.Clone(entry.Result),
		metadata:  maps.Clone(entry.Metadata),
	}
	if task.turnSeq <= 0 {
		task.turnSeq = taskTurnSeqFromSpec(entry.Metadata)
	}
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	if task.runner == nil && task.running {
		task.applyInterruptedLocked("subagent session requires reconnect")
	}
	return task
}

func (tm *taskRuntime) attachSubagentParticipant(ctx context.Context, activeSession session.Session, task *subagentTask, parentCall string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil {
		return nil
	}
	handle := strings.TrimSpace(task.handle)
	if handle == "" {
		handle = tm.reserveSubagentHandle(activeSession, task.sessionRef, task.agent)
		task.handle = handle
	}
	mention := "@" + strings.TrimPrefix(handle, "@")
	role := subagentParticipantRole(task)
	_, err := tm.runtime.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: task.sessionRef,
		Binding: session.ParticipantBinding{
			ID:            strings.TrimSpace(task.anchor.AgentID),
			Kind:          session.ParticipantKindSubagent,
			Role:          role,
			AgentName:     strings.TrimSpace(task.agent),
			Label:         mention,
			SessionID:     strings.TrimSpace(task.anchor.SessionID),
			Source:        firstNonEmpty(strings.TrimSpace(taskStringValue(task.metadata["source"])), "agent_spawn"),
			ParentTurnID:  strings.TrimSpace(parentCall),
			DelegationID:  strings.TrimSpace(task.ref.TaskID),
			AttachedAt:    tm.runtime.now(),
			ControllerRef: strings.TrimSpace(activeSession.Controller.EpochID),
		},
	})
	if err != nil {
		return err
	}
	_, err = tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: task.sessionRef,
		Event: &session.Event{
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityUIOnly,
			Time:       tm.runtime.now(),
			Actor: session.ActorRef{
				Kind: session.ActorKindSystem,
				ID:   "spawn",
				Name: "spawn",
			},
			Protocol: &session.EventProtocol{
				Participant: &session.ProtocolParticipant{Action: "attached"},
			},
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           strings.TrimSpace(task.anchor.AgentID),
					Kind:         session.ParticipantKindSubagent,
					Role:         role,
					DelegationID: strings.TrimSpace(task.ref.TaskID),
				},
			},
			Meta: map[string]any{
				"task_id":    task.ref.TaskID,
				"agent":      task.agent,
				"agent_id":   task.anchor.AgentID,
				"handle":     handle,
				"mention":    mention,
				"session_id": task.anchor.SessionID,
				"state":      string(task.state),
			},
		},
	})
	return err
}

func (tm *taskRuntime) updateSubagentParticipant(ctx context.Context, task *subagentTask, action string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil {
		return nil
	}
	role := subagentParticipantRole(task)
	_, err := tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: task.sessionRef,
		Event: &session.Event{
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityUIOnly,
			Time:       tm.runtime.now(),
			Actor: session.ActorRef{
				Kind: session.ActorKindSystem,
				ID:   "spawn",
				Name: "spawn",
			},
			Protocol: &session.EventProtocol{
				Participant: &session.ProtocolParticipant{Action: strings.TrimSpace(action)},
			},
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           strings.TrimSpace(task.anchor.AgentID),
					Kind:         session.ParticipantKindSubagent,
					Role:         role,
					DelegationID: strings.TrimSpace(task.ref.TaskID),
				},
			},
			Meta: map[string]any{
				"task_id":        task.ref.TaskID,
				"agent":          task.agent,
				"agent_id":       task.anchor.AgentID,
				"handle":         task.handle,
				"mention":        "@" + strings.TrimPrefix(task.handle, "@"),
				"session_id":     task.anchor.SessionID,
				"state":          string(task.state),
				"output_preview": strings.TrimSpace(taskStringValue(task.result["output_preview"])),
			},
		},
	})
	return err
}

func (tm *taskRuntime) appendSideSubagentUserEvent(ctx context.Context, task *subagentTask, prompt string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil || !isSideSubagentTask(task) {
		return nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	role := subagentParticipantRole(task)
	message := model.NewTextMessage(model.RoleUser, prompt)
	_, err := tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: task.sessionRef,
		Event: &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Time:       tm.runtime.now(),
			Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
			Scope: &session.EventScope{
				TurnID: subagentTurnID(task.ref.TaskID, task.turnSeq),
				Source: firstNonEmpty(taskStringValue(task.metadata["source"]), "slash_agent"),
				Participant: session.ParticipantRef{
					ID:           strings.TrimSpace(task.anchor.AgentID),
					Kind:         session.ParticipantKindSubagent,
					Role:         role,
					DelegationID: strings.TrimSpace(task.ref.TaskID),
				},
			},
			Message: &message,
			Text:    prompt,
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeUserMessage),
			},
			Meta: map[string]any{
				"handle":  strings.TrimSpace(task.handle),
				"mention": "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
				"agent":   strings.TrimSpace(task.agent),
			},
		},
	})
	return err
}

func (tm *taskRuntime) appendSideSubagentFinalEvent(ctx context.Context, task *subagentTask) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil || !isSideSubagentTask(task) {
		return nil
	}
	task.mu.Lock()
	if task.running || task.state != taskapi.StateCompleted || strings.EqualFold(taskStringValue(task.metadata["final_event_persisted"]), "true") {
		task.mu.Unlock()
		return nil
	}
	text := strings.TrimSpace(taskStringValue(task.result["result"]))
	if text == "" {
		text = compactFinalOutput(task.stdout, task.stderr)
	}
	if text == "" {
		text = strings.TrimSpace(taskStringValue(task.result["output_preview"]))
	}
	if text == "" {
		task.mu.Unlock()
		return nil
	}
	role := subagentParticipantRole(task)
	message := model.NewTextMessage(model.RoleAssistant, text)
	event := &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Time:       tm.runtime.now(),
		Actor: session.ActorRef{
			Kind: session.ActorKindParticipant,
			ID:   strings.TrimSpace(task.anchor.AgentID),
			Role: string(role),
			Name: "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
		},
		Scope: &session.EventScope{
			TurnID: subagentTurnID(task.ref.TaskID, task.turnSeq),
			Source: firstNonEmpty(taskStringValue(task.metadata["source"]), "slash_agent"),
			Participant: session.ParticipantRef{
				ID:           strings.TrimSpace(task.anchor.AgentID),
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(task.ref.TaskID),
			},
		},
		Message: &message,
		Text:    text,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
		},
		Meta: map[string]any{
			"handle":  strings.TrimSpace(task.handle),
			"mention": "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
			"agent":   strings.TrimSpace(task.agent),
		},
	}
	task.mu.Unlock()

	if _, err := tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: task.sessionRef, Event: event}); err != nil {
		return err
	}
	if err := tm.runtime.updateParticipantContextCheckpoint(ctx, task.sessionRef, strings.TrimSpace(task.anchor.AgentID)); err != nil {
		return err
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["final_event_persisted"] = "true"
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	return tm.persistTaskEntry(ctx, entry)
}

func taskSpecString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw := values[key]
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}

func taskStringValue(raw any) string {
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}

func subagentTerminalID(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ""
	}
	return "subagent-" + taskID
}

func allocateSubagentHandle(activeSession session.Session, agent string) string {
	return agenthandle.Allocate(subagentHandlesFromSession(activeSession), agent)
}

func (tm *taskRuntime) reserveSubagentHandle(activeSession session.Session, ref session.SessionRef, agent string) string {
	used := subagentHandlesFromSession(activeSession)
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, task := range tm.subagents {
		if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
			continue
		}
		for _, handle := range []string{task.handle, taskStringValue(task.metadata["handle"]), taskStringValue(task.result["handle"])} {
			if normalized := normalizeSubagentHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	if sessionID != "" {
		for handle := range tm.handles[sessionID] {
			if normalized := normalizeSubagentHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	handle := agenthandle.Allocate(used, agent)
	tm.rememberSubagentHandleLocked(sessionID, handle)
	return handle
}

func (tm *taskRuntime) rememberSubagentHandleLocked(sessionID string, handle string) {
	sessionID = strings.TrimSpace(sessionID)
	handle = normalizeSubagentHandle(handle)
	if sessionID == "" || handle == "" {
		return
	}
	if tm.handles == nil {
		tm.handles = map[string]map[string]struct{}{}
	}
	if tm.handles[sessionID] == nil {
		tm.handles[sessionID] = map[string]struct{}{}
	}
	tm.handles[sessionID][handle] = struct{}{}
}

func subagentHandlesFromSession(activeSession session.Session) map[string]struct{} {
	used := map[string]struct{}{}
	for _, participant := range activeSession.Participants {
		handle := normalizeSubagentHandle(participant.Label)
		if handle != "" {
			used[handle] = struct{}{}
		}
	}
	return used
}

func normalizeSubagentHandle(value string) string {
	return agenthandle.Normalize(value)
}

func (t *bashTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	return &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Kind:           taskapi.KindBash,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  true,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		HeartbeatAt:    now,
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		Spec: map[string]any{
			"command":    t.command,
			"workdir":    t.workdir,
			"session_id": t.ref.SessionID,
		},
		Result:   canonicalTaskResult(t.result),
		Metadata: maps.Clone(t.metadata),
		Terminal: t.session.Terminal(),
	}
}

func (t *subagentTask) applyResult(result delegation.Result) {
	if t == nil {
		return
	}
	t.state = taskStateFromDelegation(result.State)
	t.running = result.State == delegation.StateRunning
	if t.result == nil {
		t.result = map[string]any{}
	}
	if t.metadata == nil {
		t.metadata = map[string]any{}
	}
	t.metadata["task_id"] = t.handle
	t.metadata["internal_task_id"] = t.ref.TaskID
	t.metadata["task_kind"] = string(taskapi.KindSubagent)
	t.metadata["agent"] = t.agent
	t.metadata["agent_id"] = t.anchor.AgentID
	t.metadata["handle"] = t.handle
	t.metadata["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.metadata["prompt"] = t.prompt
	t.metadata["session_id"] = t.anchor.SessionID
	t.metadata["terminal_id"] = t.ref.TerminalID
	t.metadata["state"] = string(t.state)
	if preview := strings.TrimSpace(result.OutputPreview); preview != "" {
		t.result["output_preview"] = preview
	} else if t.result != nil {
		delete(t.result, "output_preview")
	}
	if text := strings.TrimSpace(result.Result); text != "" {
		t.result["result"] = text
		if !t.running {
			t.result["final_message"] = text
		}
	} else if !t.running {
		if preview := strings.TrimSpace(result.OutputPreview); preview != "" {
			t.result["result"] = preview
			t.result["final_message"] = preview
		} else {
			delete(t.result, "result")
			delete(t.result, "final_message")
		}
	} else if t.result != nil {
		delete(t.result, "result")
		delete(t.result, "final_message")
	}
	t.result["task_id"] = t.handle
	t.result["handle"] = t.handle
	t.result["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.result["agent"] = t.agent
	t.result["state"] = string(t.state)
}

func (t *subagentTask) isRunning() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

func (t *subagentTask) applyInterruptedLocked(reason string) {
	if t == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "subagent interrupted during resume"
	}
	t.running = false
	t.state = taskapi.StateInterrupted
	if t.result == nil {
		t.result = map[string]any{}
	}
	if t.metadata == nil {
		t.metadata = map[string]any{}
	}
	t.result["state"] = string(taskapi.StateInterrupted)
	t.result["error"] = reason
	t.result["result"] = reason
	t.result["output_preview"] = reason
	t.result["task_id"] = t.handle
	t.result["handle"] = t.handle
	t.result["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.result["agent"] = t.agent
	t.metadata["state"] = string(taskapi.StateInterrupted)
	t.metadata["interrupted_reason"] = reason
	t.metadata["task_id"] = t.handle
	t.metadata["internal_task_id"] = t.ref.TaskID
	t.metadata["task_kind"] = string(taskapi.KindSubagent)
	t.metadata["agent"] = t.agent
	t.metadata["agent_id"] = t.anchor.AgentID
	t.metadata["handle"] = t.handle
	t.metadata["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.metadata["prompt"] = t.prompt
	t.metadata["session_id"] = t.anchor.SessionID
	t.metadata["terminal_id"] = t.ref.TerminalID
}

func (t *subagentTask) seedStreamFromResult(result delegation.Result) {
	if t == nil {
		return
	}
	if strings.TrimSpace(t.stdout) != "" || strings.TrimSpace(t.stderr) != "" {
		return
	}
	text := strings.TrimSpace(result.Result)
	if text != "" && subagentFramesContainAssistantText(t.streamFrames) {
		return
	}
	if text == "" {
		if len(t.streamFrames) > 0 {
			return
		}
		text = strings.TrimSpace(result.OutputPreview)
	}
	if text == "" {
		return
	}
	t.appendStreamLocked(text)
}

func subagentFramesContainAssistantText(frames []stream.Frame) bool {
	for _, frame := range frames {
		if strings.TrimSpace(frame.Text) != "" {
			return true
		}
		event := frame.Event
		if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant {
			continue
		}
		if event.Message != nil && strings.TrimSpace(event.Message.TextContent()) != "" {
			return true
		}
		updateType := ""
		if event.Protocol != nil {
			updateType = strings.TrimSpace(event.Protocol.UpdateType)
		}
		if updateType == string(session.ProtocolUpdateTypeAgentThought) {
			continue
		}
		if strings.TrimSpace(event.Text) != "" {
			return true
		}
	}
	return false
}

func (t *subagentTask) applyStreamFrames(frames []stream.Frame) {
	if t == nil || len(frames) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, frame := range frames {
		if frame.Event != nil || frame.Text != "" {
			cloned := stream.CloneFrame(frame)
			cloned.Ref.TaskID = firstNonEmpty(strings.TrimSpace(cloned.Ref.TaskID), strings.TrimSpace(t.ref.TaskID))
			cloned.Ref.SessionID = firstNonEmpty(strings.TrimSpace(cloned.Ref.SessionID), strings.TrimSpace(t.sessionRef.SessionID))
			cloned.Ref.TerminalID = firstNonEmpty(strings.TrimSpace(cloned.Ref.TerminalID), subagentTurnID(t.ref.TaskID, t.turnSeq))
			if cloned.Event != nil {
				if cloned.Event.Scope == nil {
					cloned.Event.Scope = &session.EventScope{}
				}
				cloned.Event.Scope.TurnID = firstNonEmpty(strings.TrimSpace(cloned.Event.Scope.TurnID), subagentTurnID(t.ref.TaskID, t.turnSeq))
			}
			t.streamFrames = append(t.streamFrames, cloned)
		}
		text := frame.Text
		if text == "" {
			if frame.State != "" {
				t.state = taskStateFromDelegation(delegation.State(frame.State))
				t.running = frame.Running
			} else if frame.Running {
				t.running = true
			}
			continue
		}
		t.appendStreamLocked(text)
		if t.result == nil {
			t.result = map[string]any{}
		}
		t.result["output_preview"] = compactFinalOutput(t.stdout, t.stderr)
		if frame.State != "" {
			t.state = taskStateFromDelegation(delegation.State(frame.State))
		}
		t.running = frame.Running
	}
}

func (t *subagentTask) appendStreamLocked(text string) {
	if t == nil || text == "" {
		return
	}
	t.stdout += text
	t.stdoutCursor = int64(len([]byte(t.stdout)))
}

func (t *bashTask) appendOutput(text string) {
	if t == nil || text == "" {
		return
	}
	t.mu.Lock()
	t.appendOutputLocked(text)
	t.mu.Unlock()
}

func (t *bashTask) appendOutputLocked(text string) {
	if t == nil || text == "" {
		return
	}
	raw := []byte(t.output)
	raw = append(raw, text...)
	if bashLiveOutputBufferCapBytes > 0 && len(raw) > bashLiveOutputBufferCapBytes {
		dropped := len(raw) - bashLiveOutputBufferCapBytes
		raw = raw[dropped:]
		t.outputBase += int64(dropped)
		if t.modelCursor < t.outputBase {
			t.modelCursor = t.outputBase
		}
	}
	t.output = string(raw)
	t.outputLive = true
}

func (t *bashTask) outputCursorLocked() int64 {
	if t == nil {
		return 0
	}
	return t.outputBase + int64(len([]byte(t.output)))
}

func (t *bashTask) outputFromCursorLocked(cursor int64) string {
	if t == nil || t.output == "" {
		return ""
	}
	if cursor < t.outputBase {
		cursor = t.outputBase
	}
	return sliceStringFromByteCursor(t.output, cursor-t.outputBase)
}

func (t *subagentTask) snapshot() taskapi.Snapshot {
	if t == nil {
		return taskapi.Snapshot{}
	}
	result := maps.Clone(t.result)
	metadata := maps.Clone(t.metadata)
	if result == nil {
		result = map[string]any{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	turnID := subagentTurnID(t.ref.TaskID, t.turnSeq)
	result["turn_id"] = turnID
	result["turn_seq"] = t.turnSeq
	metadata["turn_id"] = turnID
	metadata["turn_seq"] = t.turnSeq
	return taskapi.CloneSnapshot(taskapi.Snapshot{
		Ref:            t.ref,
		Kind:           taskapi.KindSubagent,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  !t.running && t.state == taskapi.StateCompleted,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      time.Now(),
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		EventCursor:    int64(len(t.streamFrames)),
		Result:         canonicalTaskResult(result),
		Metadata:       metadata,
	})
}

func (t *subagentTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	return &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Kind:           taskapi.KindSubagent,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  !t.running && t.state == taskapi.StateCompleted,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		HeartbeatAt:    now,
		Spec: map[string]any{
			"agent":       t.agent,
			"prompt":      t.prompt,
			"session_id":  t.anchor.SessionID,
			"agent_id":    t.anchor.AgentID,
			"handle":      t.handle,
			"terminal_id": t.ref.TerminalID,
			"turn_seq":    t.turnSeq,
			"turn_id":     subagentTurnID(t.ref.TaskID, t.turnSeq),
		},
		Result:   canonicalTaskResult(t.result),
		Metadata: maps.Clone(t.metadata),
	}
}

func subagentTurnID(taskID string, seq int64) string {
	taskID = strings.TrimSpace(taskID)
	if seq <= 0 {
		seq = 1
	}
	if taskID == "" {
		return fmt.Sprintf("turn-%d", seq)
	}
	return fmt.Sprintf("%s:%d", taskID, seq)
}

func taskTurnSeqFromSpec(values map[string]any) int64 {
	if len(values) == 0 {
		return 0
	}
	value, ok := intArg(values, "turn_seq")
	if !ok {
		return 0
	}
	return int64(value)
}

func taskStateFromDelegation(state delegation.State) taskapi.State {
	switch state {
	case delegation.StateCompleted:
		return taskapi.StateCompleted
	case delegation.StateCancelled:
		return taskapi.StateCancelled
	case delegation.StateInterrupted:
		return taskapi.StateInterrupted
	case delegation.StateWaitingApproval:
		return taskapi.StateWaitingApproval
	case delegation.StateFailed:
		return taskapi.StateFailed
	default:
		return taskapi.StateRunning
	}
}

type completedTaskSession struct {
	entry *taskapi.Entry
}

func (s completedTaskSession) Ref() sandbox.SessionRef {
	if s.entry == nil {
		return sandbox.SessionRef{}
	}
	return sandbox.SessionRef{
		Backend:   s.entry.Terminal.Backend,
		SessionID: s.entry.Terminal.SessionID,
	}
}

func (s completedTaskSession) Terminal() sandbox.TerminalRef {
	if s.entry == nil {
		return sandbox.TerminalRef{}
	}
	return sandbox.CloneTerminalRef(s.entry.Terminal)
}

func (completedTaskSession) WriteInput(_ context.Context, _ []byte) error {
	return fmt.Errorf("impl/agent/local: task is not running")
}

func (s completedTaskSession) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	if s.entry == nil || s.entry.Result == nil {
		return nil, nil, 0, 0, nil
	}
	stdout, stderr := completedTaskOutput(s.entry.Result)
	if stdoutMarker < 0 {
		stdoutMarker = 0
	}
	if stderrMarker < 0 {
		stderrMarker = 0
	}
	if stdoutMarker > int64(len(stdout)) {
		stdoutMarker = int64(len(stdout))
	}
	if stderrMarker > int64(len(stderr)) {
		stderrMarker = int64(len(stderr))
	}
	return []byte(stdout[stdoutMarker:]), []byte(stderr[stderrMarker:]), int64(len(stdout)), int64(len(stderr)), nil
}

func (s completedTaskSession) Status(context.Context) (sandbox.SessionStatus, error) {
	if s.entry == nil {
		return sandbox.SessionStatus{}, nil
	}
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       false,
		SupportsInput: false,
		ExitCode:      completedTaskExitCode(s.entry),
		StartedAt:     s.entry.CreatedAt,
		UpdatedAt:     s.entry.UpdatedAt,
	}, nil
}

func (s completedTaskSession) Wait(ctx context.Context, _ time.Duration) (sandbox.SessionStatus, error) {
	return s.Status(ctx)
}

func (s completedTaskSession) Result(context.Context) (sandbox.CommandResult, error) {
	if s.entry == nil || s.entry.Result == nil {
		return sandbox.CommandResult{}, nil
	}
	stdout, stderr := completedTaskOutput(s.entry.Result)
	return sandbox.CommandResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: completedTaskExitCode(s.entry),
		Route:    sandbox.RouteHost,
		Backend:  s.entry.Terminal.Backend,
	}, nil
}

func (completedTaskSession) Terminate(context.Context) error { return nil }

func completedTaskOutput(result map[string]any) (string, string) {
	if result == nil {
		return "", ""
	}
	if text, _ := result["result"].(string); text != "" {
		return text, ""
	}
	stdout, _ := result["stdout"].(string)
	stderr, _ := result["stderr"].(string)
	return stdout, stderr
}

func completedTaskExitCode(entry *taskapi.Entry) int {
	if entry == nil {
		return 0
	}
	if code, ok := parseIntArgValue(entry.Result["exit_code"]); ok {
		return code
	}
	state := entry.State
	if state == "" {
		state = taskapi.State(strings.TrimSpace(taskStringValue(entry.Result["state"])))
	}
	switch state {
	case taskapi.StateCompleted:
		return 0
	case taskapi.StateCancelled, taskapi.StateInterrupted:
		return -1
	case taskapi.StateFailed:
		return 1
	default:
		if entry.Running {
			return 0
		}
		return 1
	}
}

func sandboxRuntimeFromTool(tool tool.Tool) (sandbox.Runtime, bool) {
	provider, ok := tool.(sandboxRuntimeProvider)
	if !ok || provider == nil {
		return nil, false
	}
	runtime := provider.SandboxRuntime()
	if runtime == nil {
		return nil, false
	}
	return runtime, true
}

func constraintsFromMetadata(meta map[string]any) sandbox.Constraints {
	if meta == nil {
		return sandbox.Constraints{}
	}
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sandbox.Constraints{}
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed)
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}
	}
	var out sandbox.Constraints
	if err := json.Unmarshal(bytes, &out); err != nil {
		return sandbox.Constraints{}
	}
	return sandbox.NormalizeConstraints(out)
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	var out map[string]any
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func rejectUnknownArgs(values map[string]any, allowed ...string) error {
	allowedSet := map[string]struct{}{}
	for _, key := range allowed {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		allowedSet[key] = struct{}{}
	}
	for key := range values {
		if _, ok := allowedSet[key]; !ok {
			return fmt.Errorf("tool: arg %q is not supported", key)
		}
	}
	return nil
}

func stringArg(values map[string]any, key string) (string, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func intArg(values map[string]any, key string) (int, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return 0, false
	}
	return parseIntArgValue(raw)
}

func optionalIntArg(values map[string]any, key string) *int {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	value, ok := parseIntArgValue(raw)
	if !ok {
		return nil
	}
	return &value
}

func parseIntArgValue(raw any) (int, bool) {
	switch typed := raw.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}

func taskInt64Value(raw any) (int64, bool) {
	switch typed := raw.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		value, err := typed.Int64()
		return value, err == nil
	default:
		return 0, false
	}
}

func compactLatestOutput(delta string) string {
	delta = strings.TrimRight(strings.ReplaceAll(delta, "\r\n", "\n"), "\r\n")
	if strings.TrimSpace(delta) == "" {
		return ""
	}
	lines := strings.Split(delta, "\n")
	for i := range lines {
		lines[i] = compactLine(lines[i])
	}
	const keepLines = 5
	if len(lines) > keepLines {
		hidden := len(lines) - keepLines
		lines = append([]string{fmt.Sprintf("...%d lines hidden...", hidden)}, lines[len(lines)-keepLines:]...)
	}
	return strings.Join(lines, "\n")
}

func commandExitCodeAvailable(state taskapi.State, exitCode int, resultErr error) bool {
	if exitCode < 0 {
		return false
	}
	switch state {
	case taskapi.StateCompleted, taskapi.StateFailed:
	default:
		return false
	}
	if resultErr != nil && exitCode == 0 && !plainTerminalExitError(resultErr) {
		return false
	}
	return true
}

func plainTerminalExitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.TrimSpace(err.Error())
	return strings.HasPrefix(text, "exit status ") || strings.HasPrefix(text, "signal: ")
}

func terminalDeltaText(stdout string, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return stdout + stderr
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
}

func terminalFinalText(output string, stdout string, stderr string, resultErr error) string {
	if text := terminalDeltaText(stdout, stderr); text != "" {
		return text
	}
	if output != "" {
		return output
	}
	if resultErr != nil {
		if text := strings.TrimSpace(resultErr.Error()); text != "" {
			if !strings.HasPrefix(text, "exit status ") && !strings.HasPrefix(text, "signal: ") {
				return text
			}
		}
	}
	return "(no output)"
}

func compactFinalOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout != "" && stderr != "":
		return compactBlock(stdout+"\nstderr:\n"+stderr, 1600)
	case stdout != "":
		return compactBlock(stdout, 1600)
	case stderr != "":
		return compactBlock("stderr:\n"+stderr, 1600)
	default:
		return ""
	}
}

func compactBlock(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 || len(text) <= limit {
		return text
	}
	const marker = "\n...[truncated]...\n"
	head := limit / 2
	tail := limit - head - len(marker)
	if tail < 0 {
		tail = 0
	}
	return text[:head] + marker + text[len(text)-tail:]
}

func compactLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const limit = 160
	if len(text) <= limit {
		return text
	}
	const marker = " ...[truncated]... "
	head := 70
	tail := limit - head - len(marker)
	if tail < 0 {
		tail = 0
	}
	return text[:head] + marker + text[len(text)-tail:]
}
