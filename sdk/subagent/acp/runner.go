package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	"github.com/OnslaughtSnail/caelis/sdk/internal/acputil"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
	sdksubagent "github.com/OnslaughtSnail/caelis/sdk/subagent"
)

type PermissionHandler func(context.Context, sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error)

type PermissionBridge interface {
	RequestPermission(context.Context, PermissionRequest) (sdkacpclient.RequestPermissionResponse, error)
}

type PermissionRequest struct {
	Spawn   sdksubagent.SpawnContext
	Agent   sdkdelegation.Agent
	AgentID string
	Request sdkacpclient.RequestPermissionRequest
}

type RunnerConfig struct {
	Registry          *Registry
	ClientInfo        *sdkacpclient.Implementation
	Clock             func() time.Time
	PermissionHandler PermissionHandler
	PermissionBridge  PermissionBridge
}

type Runner struct {
	registry          *Registry
	clientInfo        *sdkacpclient.Implementation
	clock             func() time.Time
	permissionHandler PermissionHandler
	permissionBridge  PermissionBridge

	counter atomic.Uint64
	mu      sync.RWMutex
	runs    map[string]*childRun
}

type childRun struct {
	anchor sdkdelegation.Anchor
	client *sdkacpclient.Client
	taskID string
	sink   sdkstream.Sink
	ctx    context.Context
	cancel context.CancelFunc

	mu            sync.RWMutex
	state         sdkdelegation.State
	outputPreview string
	result        string
	agentText     string
	updatedAt     time.Time
	running       bool
	done          chan struct{}
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("sdk/subagent/acp: registry is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Runner{
		registry:          cfg.Registry,
		clientInfo:        cfg.ClientInfo,
		clock:             clock,
		permissionHandler: cfg.PermissionHandler,
		permissionBridge:  cfg.PermissionBridge,
		runs:              map[string]*childRun{},
	}, nil
}

func (r *Runner) Spawn(ctx context.Context, spawn sdksubagent.SpawnContext, req sdkdelegation.Request) (sdkdelegation.Anchor, sdkdelegation.Result, error) {
	cfg, err := r.registry.Resolve(req.Agent)
	if err != nil {
		return sdkdelegation.Anchor{}, sdkdelegation.Result{}, err
	}
	run := &childRun{
		state:     sdkdelegation.StateRunning,
		running:   true,
		taskID:    strings.TrimSpace(spawn.TaskID),
		sink:      spawn.Streams,
		updatedAt: r.clock(),
		done:      make(chan struct{}),
	}
	childCtx, childCancel := context.WithCancel(context.WithoutCancel(ctx))
	run.ctx = childCtx
	run.cancel = childCancel
	agentID := r.nextAgentID(cfg.Name)
	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}
		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	client, err := sdkacpclient.Start(childCtx, sdkacpclient.Config{
		Command:    cfg.Command,
		Args:       append([]string(nil), cfg.Args...),
		Env:        launchEnv,
		WorkDir:    pickWorkDir(cfg.WorkDir, spawn.CWD),
		ClientInfo: r.clientInfo,
		OnUpdate:   func(env sdkacpclient.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, agentID)(ctx, req)
		},
	})
	if err != nil {
		childCancel()
		return sdkdelegation.Anchor{}, sdkdelegation.Result{}, err
	}
	if _, err := client.Initialize(ctx); err != nil {
		childCancel()
		_ = client.Close(ctx)
		return sdkdelegation.Anchor{}, sdkdelegation.Result{}, err
	}
	sessionResp, err := client.NewSession(ctx, strings.TrimSpace(spawn.CWD), nil)
	if err != nil {
		childCancel()
		_ = client.Close(ctx)
		return sdkdelegation.Anchor{}, sdkdelegation.Result{}, err
	}
	anchor := sdkdelegation.Anchor{
		TaskID:    strings.TrimSpace(spawn.TaskID),
		SessionID: strings.TrimSpace(sessionResp.SessionID),
		Agent:     cfg.Name,
		AgentID:   agentID,
	}
	run.anchor = anchor
	run.client = client
	r.mu.Lock()
	r.runs[anchor.SessionID] = run
	r.mu.Unlock()
	go r.drivePrompt(childCtx, run, strings.TrimSpace(req.Prompt))
	return anchor, r.waitRun(ctx, run, 0), nil
}

func (r *Runner) Wait(ctx context.Context, anchor sdkdelegation.Anchor, yieldTimeMS int) (sdkdelegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return sdkdelegation.Result{}, err
	}
	return r.waitRun(ctx, run, yieldTimeMS), nil
}

func (r *Runner) Continue(ctx context.Context, anchor sdkdelegation.Anchor, req sdkdelegation.ContinueRequest) (sdkdelegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return sdkdelegation.Result{}, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return sdkdelegation.Result{}, fmt.Errorf("sdk/subagent/acp: continuation prompt is required")
	}
	run.mu.Lock()
	if run.running {
		run.mu.Unlock()
		return sdkdelegation.Result{}, fmt.Errorf("sdk/subagent/acp: child session %q is still running; use TASK wait before write", run.anchor.SessionID)
	}
	run.state = sdkdelegation.StateRunning
	run.running = true
	run.outputPreview = ""
	run.result = ""
	run.updatedAt = r.clock()
	run.done = make(chan struct{})
	runCtx := run.ctx
	if runCtx == nil {
		runCtx = context.WithoutCancel(ctx)
	}
	run.mu.Unlock()
	go r.drivePrompt(runCtx, run, prompt)
	return r.waitRun(ctx, run, req.YieldTimeMS), nil
}

func (r *Runner) Cancel(ctx context.Context, anchor sdkdelegation.Anchor) error {
	run, err := r.lookup(anchor)
	if err != nil {
		return err
	}
	run.mu.RLock()
	client := run.client
	sessionID := run.anchor.SessionID
	run.mu.RUnlock()
	if client != nil {
		_ = client.Cancel(ctx, sessionID)
	}
	if run.cancel != nil {
		run.cancel()
	}
	run.mu.Lock()
	run.running = false
	run.state = sdkdelegation.StateCancelled
	run.outputPreview = "cancelled"
	run.updatedAt = r.clock()
	run.mu.Unlock()
	return nil
}

func (r *Runner) drivePrompt(ctx context.Context, run *childRun, prompt string) {
	resp, err := run.client.Prompt(ctx, run.anchor.SessionID, prompt, nil)
	run.mu.Lock()
	defer run.mu.Unlock()
	defer close(run.done)
	run.running = false
	run.updatedAt = r.clock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if run.state != sdkdelegation.StateCancelled {
				run.state = sdkdelegation.StateInterrupted
				run.outputPreview = "interrupted"
			}
			run.result = ""
			_ = run.client.Close(context.WithoutCancel(ctx))
			return
		}
		errText := err.Error()
		if stderr := run.client.StderrTail(4096); strings.TrimSpace(stderr) != "" {
			errText += "\nstderr:\n" + stderr
		}
		run.state = sdkdelegation.StateFailed
		run.outputPreview = compactPreview(errText)
		run.result = ""
		_ = run.client.Close(context.WithoutCancel(ctx))
		return
	}
	if strings.EqualFold(strings.TrimSpace(resp.StopReason), "cancelled") {
		run.state = sdkdelegation.StateCancelled
		run.outputPreview = "cancelled"
		_ = run.client.Close(context.WithoutCancel(ctx))
		return
	}
	if strings.TrimSpace(run.result) == "" {
		run.result = strings.TrimSpace(run.outputPreview)
	}
	run.state = sdkdelegation.StateCompleted
	run.outputPreview = compactPreview(run.outputPreview)
}

func (r *Runner) waitRun(ctx context.Context, run *childRun, yieldTimeMS int) sdkdelegation.Result {
	if run == nil {
		return sdkdelegation.Result{}
	}
	wait := time.Duration(yieldTimeMS) * time.Millisecond
	if wait < 0 {
		wait = 0
	}
	if wait > 0 {
		select {
		case <-ctx.Done():
		case <-run.done:
		case <-time.After(wait):
		}
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	out := sdkdelegation.Result{
		State:         run.state,
		Running:       run.running,
		Yielded:       run.running,
		OutputPreview: strings.TrimSpace(run.outputPreview),
		Result:        "",
		UpdatedAt:     run.updatedAt,
	}
	if !run.running {
		out.Result = strings.TrimSpace(run.result)
	}
	return sdkdelegation.CloneResult(out)
}

func (r *Runner) lookup(anchor sdkdelegation.Anchor) (*childRun, error) {
	sessionID := strings.TrimSpace(anchor.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("sdk/subagent/acp: session_id is required")
	}
	r.mu.RLock()
	run := r.runs[sessionID]
	r.mu.RUnlock()
	if run == nil {
		return nil, fmt.Errorf("sdk/subagent/acp: child session %q not found", sessionID)
	}
	return run, nil
}

func (r *Runner) nextAgentID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		name = "agent"
	}
	return fmt.Sprintf("%s-%03d", name, r.counter.Add(1))
}

func (r *Runner) permissionCallback(spawn sdksubagent.SpawnContext, cfg AgentConfig, agentID string) PermissionHandler {
	if r.permissionBridge != nil {
		return func(ctx context.Context, req sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
			resp, err := r.permissionBridge.RequestPermission(ctx, PermissionRequest{
				Spawn: spawn,
				Agent: sdkdelegation.Agent{
					Name:        strings.TrimSpace(cfg.Name),
					Description: strings.TrimSpace(cfg.Description),
				},
				AgentID: strings.TrimSpace(agentID),
				Request: req,
			})
			if err != nil {
				return sdkacpclient.RequestPermissionResponse{}, err
			}
			return resp, nil
		}
	}
	if r.permissionHandler != nil {
		return r.permissionHandler
	}
	return func(ctx context.Context, req sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
		if auto, ok := acputil.AutoApproveAllOnce(spawn.Mode, cfg.Name, req); ok {
			return auto, nil
		}
		if spawn.ApprovalRequester != nil {
			resp, err := spawn.ApprovalRequester.RequestSubagentApproval(ctx, translateApprovalRequest(spawn, cfg, agentID, req))
			if err != nil {
				return sdkacpclient.RequestPermissionResponse{}, err
			}
			if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
				return selected, nil
			}
		}
		return acputil.RejectOnce(), nil
	}
}

func translateApprovalRequest(
	spawn sdksubagent.SpawnContext,
	cfg AgentConfig,
	agentID string,
	req sdkacpclient.RequestPermissionRequest,
) sdksubagent.ApprovalRequest {
	options := make([]sdksubagent.ApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, sdksubagent.ApprovalOption{
			ID:   strings.TrimSpace(item.OptionID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	return sdksubagent.ApprovalRequest{
		SessionRef: sdksession.NormalizeSessionRef(spawn.SessionRef),
		Session:    sdksession.CloneSession(spawn.Session),
		TaskID:     strings.TrimSpace(spawn.TaskID),
		Agent:      firstNonEmpty(strings.TrimSpace(cfg.Name), strings.TrimSpace(agentID)),
		Mode:       strings.TrimSpace(spawn.Mode),
		ToolCall: sdksubagent.ApprovalToolCall{
			ID:     strings.TrimSpace(req.ToolCall.ToolCallID),
			Name:   acputil.ToolCallName(req.ToolCall),
			Kind:   trimStringPtr(req.ToolCall.Kind),
			Title:  trimStringPtr(req.ToolCall.Title),
			Status: trimStringPtr(req.ToolCall.Status),
		},
		Options: options,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func trimStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func compactPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		last = strings.TrimSpace(text)
	}
	if len(last) <= 160 {
		return last
	}
	return strings.TrimSpace(last[:80]) + " ...[truncated]... " + strings.TrimSpace(last[len(last)-48:])
}

func pickWorkDir(preferred string, fallback string) string {
	if text := strings.TrimSpace(preferred); text != "" {
		return text
	}
	return strings.TrimSpace(fallback)
}

func (r *Runner) handleUpdate(run *childRun, env sdkacpclient.UpdateEnvelope) {
	if run == nil {
		return
	}
	var streamText string
	streamName := "stdout"
	var event *sdksession.Event
	run.mu.Lock()
	defer run.mu.Unlock()
	run.updatedAt = r.clock()
	switch update := env.Update.(type) {
	case sdkacpclient.ContentChunk:
		if text := chunkText(update); text != "" {
			switch strings.TrimSpace(update.SessionUpdate) {
			case sdkacpclient.UpdateAgentMessage:
				streamText = run.appendAgentMessageLocked(text)
				run.outputPreview = compactPreview(run.agentText)
			case sdkacpclient.UpdateAgentThought:
				streamName = "reasoning"
				streamText = text
			default:
				break
			}
		}
	case sdkacpclient.ToolCall:
		run.outputPreview = compactPreview(toolActivity(update.Title, update.Kind, update.Status))
	case sdkacpclient.ToolCallUpdate:
		run.outputPreview = compactPreview(toolActivity(derefString(update.Title), derefString(update.Kind), derefString(update.Status)))
	case sdkacpclient.PlanUpdate:
		run.outputPreview = "updating plan"
	}
	event = run.acpUpdateEvent(env, run.updatedAt)
	if streamText != "" || event != nil {
		run.emitLocked(sdkstream.Frame{
			Ref: sdkstream.Ref{
				TaskID:    firstNonEmpty(run.taskID, run.anchor.TaskID),
				SessionID: firstNonEmpty(strings.TrimSpace(env.SessionID), run.anchor.SessionID),
			},
			Stream:    streamName,
			Text:      streamText,
			State:     string(run.state),
			Running:   run.running,
			Event:     event,
			UpdatedAt: run.updatedAt,
		})
	}
}

func (run *childRun) acpUpdateEvent(env sdkacpclient.UpdateEnvelope, at time.Time) *sdksession.Event {
	if run == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	scope := &sdksession.EventScope{
		Source: "acp_subagent",
		Controller: sdksession.ControllerRef{
			Kind: sdksession.ControllerKindACP,
			ID:   strings.TrimSpace(firstNonEmpty(run.anchor.Agent, run.anchor.AgentID)),
		},
		Participant: sdksession.ParticipantRef{
			ID:           strings.TrimSpace(firstNonEmpty(run.anchor.AgentID, run.taskID, run.anchor.TaskID)),
			Kind:         sdksession.ParticipantKindSubagent,
			Role:         sdksession.ParticipantRoleDelegated,
			DelegationID: strings.TrimSpace(firstNonEmpty(run.taskID, run.anchor.TaskID)),
		},
		ACP: sdksession.ACPRef{
			SessionID: strings.TrimSpace(firstNonEmpty(env.SessionID, run.anchor.SessionID)),
		},
	}
	actor := sdksession.ActorRef{
		Kind: sdksession.ActorKindParticipant,
		ID:   strings.TrimSpace(firstNonEmpty(run.anchor.AgentID, run.taskID, run.anchor.TaskID)),
		Name: strings.TrimSpace(firstNonEmpty(run.anchor.Agent, run.anchor.AgentID)),
	}
	base := func(updateType string, eventType sdksession.EventType, text string) *sdksession.Event {
		scopeCopy := sdksession.CloneEventScope(*scope)
		scopeCopy.ACP.EventType = strings.TrimSpace(updateType)
		return &sdksession.Event{
			Type:       eventType,
			Visibility: sdksession.VisibilityCanonical,
			Time:       at,
			Actor:      actor,
			Scope:      &scopeCopy,
			Text:       text,
			Protocol:   &sdksession.EventProtocol{UpdateType: strings.TrimSpace(updateType)},
			Meta: map[string]any{
				"task_id":    strings.TrimSpace(firstNonEmpty(run.taskID, run.anchor.TaskID)),
				"agent":      strings.TrimSpace(run.anchor.Agent),
				"agent_id":   strings.TrimSpace(run.anchor.AgentID),
				"session_id": strings.TrimSpace(firstNonEmpty(env.SessionID, run.anchor.SessionID)),
			},
		}
	}
	switch update := env.Update.(type) {
	case sdkacpclient.ContentChunk:
		text := chunkText(update)
		if text == "" {
			return nil
		}
		switch strings.TrimSpace(update.SessionUpdate) {
		case sdkacpclient.UpdateAgentMessage, sdkacpclient.UpdateAgentThought:
			return base(update.SessionUpdate, sdksession.EventTypeAssistant, text)
		default:
			return nil
		}
	case sdkacpclient.ToolCall:
		event := base(update.SessionUpdate, sdksession.EventTypeToolCall, firstNonEmpty(strings.TrimSpace(update.Title), strings.TrimSpace(update.Kind), "tool call"))
		event.Protocol.ToolCall = &sdksession.ProtocolToolCall{
			ID:       strings.TrimSpace(update.ToolCallID),
			Name:     acpToolDisplayName(update.Kind, update.Title),
			Kind:     strings.TrimSpace(update.Kind),
			Title:    strings.TrimSpace(update.Title),
			Status:   firstNonEmpty(strings.TrimSpace(update.Status), "pending"),
			RawInput: acpToolRawInput(update.Kind, update.Title, update.RawInput),
		}
		return event
	case sdkacpclient.ToolCallUpdate:
		status := derefString(update.Status)
		event := base(update.SessionUpdate, acpToolEventType(status), firstNonEmpty(derefString(update.Title), derefString(update.Kind), "tool update"))
		event.Protocol.ToolCall = &sdksession.ProtocolToolCall{
			ID:        strings.TrimSpace(update.ToolCallID),
			Name:      acpToolDisplayName(derefString(update.Kind), derefString(update.Title)),
			Kind:      derefString(update.Kind),
			Title:     derefString(update.Title),
			Status:    status,
			RawInput:  acpToolRawInput(derefString(update.Kind), derefString(update.Title), update.RawInput),
			RawOutput: acpToolRawOutput(update.RawOutput, update.Content),
		}
		return event
	case sdkacpclient.PlanUpdate:
		event := base(update.SessionUpdate, sdksession.EventTypePlan, "plan updated")
		event.Protocol.Plan = &sdksession.ProtocolPlan{Entries: acpPlanEntries(update.Entries)}
		return event
	default:
		return nil
	}
}

func acpToolEventType(status string) sdksession.EventType {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled":
		return sdksession.EventTypeToolResult
	default:
		return sdksession.EventTypeToolCall
	}
}

func acpToolDisplayName(kind string, title string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	return strings.TrimSpace(kind)
}

func acpToolRawInput(kind string, title string, raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolRawOutput(raw any, content []sdkacpclient.ToolCallContent) map[string]any {
	out := acpRawMap(raw)
	if out == nil {
		out = map[string]any{}
	}
	if text := strings.TrimSpace(acpToolContentText(content)); text != "" {
		if _, exists := out["text"]; !exists {
			out["text"] = text
		}
	}
	for _, item := range content {
		if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
			out["terminal_id"] = terminalID
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpRawMap(raw any) map[string]any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return maps.Clone(typed)
	default:
		if text := strings.TrimSpace(textFromContentValue(typed)); text != "" {
			return map[string]any{"text": text}
		}
		if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
			return map[string]any{"text": text}
		}
		return nil
	}
}

func acpToolContentText(content []sdkacpclient.ToolCallContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if text := strings.TrimSpace(textFromContentValue(item.Content)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func acpPlanEntries(in []sdkacpclient.PlanEntry) []sdksession.ProtocolPlanEntry {
	out := make([]sdksession.ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, sdksession.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: strings.TrimSpace(item.Priority),
		})
	}
	return out
}

func (run *childRun) emitLocked(frame sdkstream.Frame) {
	if run == nil || run.sink == nil {
		return
	}
	run.sink.PublishStream(frame)
}

func (run *childRun) appendAgentMessageLocked(text string) string {
	if run == nil {
		return ""
	}
	if text == "" {
		return ""
	}
	if run.agentText == "" {
		run.agentText = text
		run.result = run.agentText
		return text
	}
	if strings.HasPrefix(text, run.agentText) {
		delta := text[len(run.agentText):]
		run.agentText = text
		run.result = run.agentText
		return delta
	}
	if strings.HasPrefix(run.agentText, text) {
		return ""
	}
	overlap := longestSuffixPrefixOverlap(run.agentText, text)
	delta := text
	if overlap > 0 {
		delta = text[overlap:]
	}
	run.agentText += delta
	run.result = run.agentText
	return delta
}

func longestSuffixPrefixOverlap(left string, right string) int {
	if left == "" || right == "" {
		return 0
	}
	best := 0
	for idx := range right {
		if idx == 0 || idx > len(left) {
			continue
		}
		if strings.HasSuffix(left, right[:idx]) {
			best = idx
		}
	}
	if len(right) <= len(left) && strings.HasSuffix(left, right) {
		best = len(right)
	}
	return best
}

func chunkText(chunk sdkacpclient.ContentChunk) string {
	var text sdkacpclient.TextChunk
	if err := json.Unmarshal(chunk.Content, &text); err == nil {
		if text.Text != "" {
			return text.Text
		}
		return textFromRawContent(chunk.Content)
	}
	return textFromRawContent(chunk.Content)
}

func textFromRawContent(raw json.RawMessage) string {
	var content any
	if err := json.Unmarshal(raw, &content); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return textFromContentValue(content)
}

func textFromContentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(textFromContentValue(item))
		}
		return out.String()
	case map[string]any:
		for _, key := range []string{"text", "content", "detailedContent"} {
			if nested, ok := typed[key]; ok {
				if text := textFromContentValue(nested); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func toolActivity(title string, kind string, status string) string {
	title = strings.TrimSpace(title)
	kind = strings.TrimSpace(strings.ToLower(kind))
	status = strings.TrimSpace(strings.ToLower(status))
	switch {
	case title != "":
		return strings.ToLower(title)
	case kind != "" && status != "":
		return kind + " " + status
	case kind != "":
		return kind
	default:
		return "working"
	}
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}
