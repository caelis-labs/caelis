package acp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/internal/acpingress"
	"github.com/OnslaughtSnail/caelis/impl/agent/acp/internal/acputil"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	acpschema "github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type PermissionHandler func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error)

type PermissionBridge interface {
	RequestPermission(context.Context, PermissionRequest) (client.RequestPermissionResponse, error)
}

type PermissionRequest struct {
	Spawn   subagent.SpawnContext
	Agent   delegation.Agent
	AgentID string
	Request client.RequestPermissionRequest
}

type RunnerConfig struct {
	Registry          *Registry
	ClientInfo        *client.Implementation
	Clock             func() time.Time
	PermissionHandler PermissionHandler
	PermissionBridge  PermissionBridge
}

type Runner struct {
	registry          *Registry
	clientInfo        *client.Implementation
	clock             func() time.Time
	permissionHandler PermissionHandler
	permissionBridge  PermissionBridge

	counter atomic.Uint64
	mu      sync.RWMutex
	runs    map[string]*childRun
}

type childRun struct {
	anchor delegation.Anchor
	client *client.Client
	taskID string
	sink   stream.Sink
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.RWMutex
	state          delegation.State
	outputPreview  string
	result         string
	agentText      string
	finalAssistant acpschema.FinalAssistantAccumulator
	lastTraceText  string
	updatedAt      time.Time
	running        bool
	done           chan struct{}
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("impl/agent/acp/subagent: registry is required")
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

func (r *Runner) Spawn(ctx context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	cfg, err := r.registry.Resolve(req.Agent)
	if err != nil {
		return delegation.Anchor{}, delegation.Result{}, err
	}
	run := &childRun{
		state:     delegation.StateRunning,
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
	client, err := client.Start(childCtx, client.Config{
		Command:    cfg.Command,
		Args:       append([]string(nil), cfg.Args...),
		Env:        launchEnv,
		WorkDir:    pickWorkDir(cfg.WorkDir, spawn.CWD),
		ClientInfo: r.clientInfo,
		OnUpdate:   func(env client.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, agentID)(ctx, req)
		},
	})
	if err != nil {
		childCancel()
		return delegation.Anchor{}, delegation.Result{}, err
	}
	if _, err := client.Initialize(ctx); err != nil {
		childCancel()
		_ = client.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	sessionResp, err := client.NewSession(ctx, strings.TrimSpace(spawn.CWD), nil)
	if err != nil {
		childCancel()
		_ = client.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	// Do not call session/set_mode for spawned ACP children here. External ACP
	// agents own their session-mode vocabulary, while Caelis approval modes
	// (manual/auto-review) are parent-routing policy. Permission requests from
	// the child still bridge through OnPermissionRequest above; Caelis self ACP
	// children that need launch-time approval policy get it from assembly args.
	anchor := delegation.Anchor{
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

func (r *Runner) Wait(ctx context.Context, anchor delegation.Anchor, yieldTimeMS int) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	return r.waitRun(ctx, run, yieldTimeMS), nil
}

func (r *Runner) Continue(ctx context.Context, anchor delegation.Anchor, req delegation.ContinueRequest) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return delegation.Result{}, fmt.Errorf("impl/agent/acp/subagent: continuation prompt is required")
	}
	run.mu.Lock()
	if run.running {
		run.mu.Unlock()
		return delegation.Result{}, fmt.Errorf("impl/agent/acp/subagent: child session %q is still running; use TASK wait before write", run.anchor.SessionID)
	}
	run.state = delegation.StateRunning
	run.running = true
	run.outputPreview = ""
	run.result = ""
	run.agentText = ""
	run.finalAssistant.Reset()
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

func (r *Runner) Cancel(ctx context.Context, anchor delegation.Anchor) error {
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
	run.state = delegation.StateCancelled
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
			if run.state != delegation.StateCancelled {
				run.state = delegation.StateInterrupted
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
		run.state = delegation.StateFailed
		run.outputPreview = compactPreview(errText)
		run.result = ""
		_ = run.client.Close(context.WithoutCancel(ctx))
		return
	}
	if strings.EqualFold(strings.TrimSpace(resp.StopReason), "cancelled") {
		run.state = delegation.StateCancelled
		run.outputPreview = "cancelled"
		run.result = ""
		_ = run.client.Close(context.WithoutCancel(ctx))
		return
	}
	run.state = delegation.StateCompleted
	run.outputPreview = compactPreview(run.outputPreview)
}

func (r *Runner) waitRun(ctx context.Context, run *childRun, yieldTimeMS int) delegation.Result {
	if run == nil {
		return delegation.Result{}
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
	out := delegation.Result{
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
	return delegation.CloneResult(out)
}

func (r *Runner) lookup(anchor delegation.Anchor) (*childRun, error) {
	sessionID := strings.TrimSpace(anchor.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("impl/agent/acp/subagent: session_id is required")
	}
	r.mu.RLock()
	run := r.runs[sessionID]
	r.mu.RUnlock()
	if run == nil {
		return nil, fmt.Errorf("impl/agent/acp/subagent: child session %q not found", sessionID)
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

func (r *Runner) permissionCallback(spawn subagent.SpawnContext, cfg AgentConfig, agentID string) PermissionHandler {
	if r.permissionBridge != nil {
		return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			resp, err := r.permissionBridge.RequestPermission(ctx, PermissionRequest{
				Spawn: spawn,
				Agent: delegation.Agent{
					Name:        strings.TrimSpace(cfg.Name),
					Description: strings.TrimSpace(cfg.Description),
				},
				AgentID: strings.TrimSpace(agentID),
				Request: req,
			})
			if err != nil {
				return client.RequestPermissionResponse{}, err
			}
			return resp, nil
		}
	}
	if r.permissionHandler != nil {
		return r.permissionHandler
	}
	return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		if spawn.ApprovalRequester != nil {
			resp, err := spawn.ApprovalRequester.RequestSubagentApproval(ctx, translateApprovalRequest(spawn, cfg, agentID, req))
			if err != nil {
				return client.RequestPermissionResponse{}, err
			}
			if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
				return selected, nil
			}
		}
		return acputil.RejectOnce(), nil
	}
}

func translateApprovalRequest(
	spawn subagent.SpawnContext,
	cfg AgentConfig,
	agentID string,
	req client.RequestPermissionRequest,
) subagent.ApprovalRequest {
	options := make([]subagent.ApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, subagent.ApprovalOption{
			ID:   strings.TrimSpace(item.OptionID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	return subagent.ApprovalRequest{
		SessionRef:   session.NormalizeSessionRef(spawn.SessionRef),
		Session:      session.CloneSession(spawn.Session),
		TaskID:       strings.TrimSpace(spawn.TaskID),
		ParentCallID: strings.TrimSpace(spawn.ParentCallID),
		Agent:        firstNonEmpty(strings.TrimSpace(cfg.Name), strings.TrimSpace(agentID)),
		Mode:         strings.TrimSpace(spawn.Mode),
		ToolCall: subagent.ApprovalToolCall{
			ID:       strings.TrimSpace(req.ToolCall.ToolCallID),
			Name:     acputil.ToolCallName(req.ToolCall),
			Kind:     trimStringPtr(req.ToolCall.Kind),
			Title:    trimStringPtr(req.ToolCall.Title),
			Status:   trimStringPtr(req.ToolCall.Status),
			RawInput: acpschema.NormalizeRawMap(req.ToolCall.RawInput),
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

func (r *Runner) handleUpdate(run *childRun, env client.UpdateEnvelope) {
	if run == nil {
		return
	}
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
	var streamText string
	var event *session.Event
	var frame *stream.Frame
	run.mu.Lock()
	run.updatedAt = r.clock()
	switch update := env.Update.(type) {
	case client.ContentChunk:
		if text := chunkText(update); text != "" {
			switch strings.TrimSpace(update.SessionUpdate) {
			case client.UpdateAgentMessage:
				streamText = run.appendAgentMessageChunkLocked(update.MessageID, text)
				run.outputPreview = compactPreview(run.agentText)
				if streamText != "" {
					event = run.acpUpdateEvent(env, run.updatedAt, streamText)
				}
			case client.UpdateAgentThought:
				run.clearFinalAssistantLocked()
				event = run.acpUpdateEvent(env, run.updatedAt)
			default:
				break
			}
		}
	case client.ToolCall:
		run.clearFinalAssistantLocked()
		run.outputPreview = compactPreview(toolActivity(update.Title, update.Kind, update.Status))
		streamText = run.appendTraceTextLocked(childToolCallTraceText(update))
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.ToolCallUpdate:
		run.clearFinalAssistantLocked()
		run.outputPreview = compactPreview(toolActivity(derefString(update.Title), derefString(update.Kind), derefString(update.Status)))
		if text := childToolCallUpdateTerminalText(update); text != "" {
			streamText = text
		} else {
			streamText = run.appendTraceTextLocked(childToolCallUpdateTraceText(update))
		}
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.PlanUpdate:
		run.clearFinalAssistantLocked()
		run.outputPreview = "updating plan"
		streamText = run.appendTraceTextLocked(childPlanTraceText(update))
		event = run.acpUpdateEvent(env, run.updatedAt)
	}
	if streamText != "" || event != nil {
		next := stream.Frame{
			Ref: stream.Ref{
				TaskID:    firstNonEmpty(run.taskID, run.anchor.TaskID),
				SessionID: firstNonEmpty(strings.TrimSpace(env.SessionID), run.anchor.SessionID),
			},
			Text:      streamText,
			State:     string(run.state),
			Running:   run.running,
			Event:     event,
			UpdatedAt: run.updatedAt,
		}
		frame = &next
	}
	run.mu.Unlock()
	if frame != nil {
		run.emit(*frame)
	}
}

func (run *childRun) acpUpdateEvent(env client.UpdateEnvelope, at time.Time, textOverride ...string) *session.Event {
	if run == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	scope := &session.EventScope{
		Source: "acp_subagent",
		Controller: session.ControllerRef{
			Kind: session.ControllerKindACP,
			ID:   strings.TrimSpace(firstNonEmpty(run.anchor.Agent, run.anchor.AgentID)),
		},
		Participant: session.ParticipantRef{
			ID:           strings.TrimSpace(firstNonEmpty(run.anchor.AgentID, run.taskID, run.anchor.TaskID)),
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			DelegationID: strings.TrimSpace(firstNonEmpty(run.taskID, run.anchor.TaskID)),
		},
		ACP: session.ACPRef{
			SessionID: strings.TrimSpace(firstNonEmpty(env.SessionID, run.anchor.SessionID)),
		},
	}
	actor := session.ActorRef{
		Kind: session.ActorKindParticipant,
		ID:   strings.TrimSpace(firstNonEmpty(run.anchor.AgentID, run.taskID, run.anchor.TaskID)),
		Name: strings.TrimSpace(firstNonEmpty(run.anchor.Agent, run.anchor.AgentID)),
	}
	opts := acpingress.Options{
		At:         at,
		Scope:      *scope,
		Actor:      actor,
		Visibility: acpingress.UIOnlyVisibility,
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"subagent": map[string]any{
						"task_id":    strings.TrimSpace(firstNonEmpty(run.taskID, run.anchor.TaskID)),
						"agent":      strings.TrimSpace(run.anchor.Agent),
						"agent_id":   strings.TrimSpace(run.anchor.AgentID),
						"session_id": strings.TrimSpace(firstNonEmpty(env.SessionID, run.anchor.SessionID)),
					},
				},
			},
		},
	}
	if len(textOverride) > 0 {
		opts.TextOverride = textOverride[0]
	}
	return acpingress.NormalizeUpdate(env.Update, opts)
}

func (run *childRun) emit(frame stream.Frame) {
	if run == nil || run.sink == nil {
		return
	}
	run.sink.PublishStream(frame)
}

func (run *childRun) appendAgentMessageLocked(text string) string {
	return run.appendAgentMessageChunkLocked("", text)
}

func (run *childRun) appendAgentMessageChunkLocked(messageID string, text string) string {
	if run == nil {
		return ""
	}
	update := run.finalAssistant.ObserveUpdate(acpschema.ContentChunk{
		SessionUpdate: acpschema.UpdateAgentMessage,
		MessageID:     strings.TrimSpace(messageID),
		Content:       text,
	})
	run.agentText = update.Text
	run.result = update.Text
	return update.Delta
}

func (run *childRun) clearFinalAssistantLocked() {
	if run == nil {
		return
	}
	run.agentText = ""
	run.result = ""
	run.finalAssistant.Reset()
}

func (run *childRun) appendTraceTextLocked(text string) string {
	if run == nil {
		return ""
	}
	text = normalizeTraceLine(text)
	if text == "" {
		return ""
	}
	if text == run.lastTraceText {
		return ""
	}
	run.lastTraceText = text
	return text
}

func childToolCallTraceText(update client.ToolCall) string {
	line := childToolTraceLine(update.Title, update.Kind, update.RawInput, update.Status)
	return normalizeTraceLine(line)
}

func childToolCallUpdateTraceText(update client.ToolCallUpdate) string {
	line := childToolTraceLine(derefString(update.Title), derefString(update.Kind), update.RawInput, derefString(update.Status))
	return normalizeTraceLine(line)
}

func childToolTraceLine(title string, kind string, rawInput any, status string) string {
	label := childToolLabel(title, kind)
	if label == "" {
		label = "Tool"
	}
	if summary := childToolInputSummary(rawInput); shouldAppendChildToolSummary(label, summary, title, kind) {
		label += " " + summary
	}
	if suffix := childToolStatusSuffix(status); suffix != "" && !containsFold(label, suffix) {
		label += " " + suffix
	}
	return label
}

func childToolCallUpdateTerminalText(update client.ToolCallUpdate) string {
	if text := childTerminalContentText(update.Content); text != "" {
		return text
	}
	return childTerminalOutputMetaText(update.Meta)
}

func childTerminalOutputMetaText(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	output := metautil.RuntimeSection(meta, metautil.Terminal)
	if len(output) == 0 {
		return ""
	}
	return acpschema.ExtractTextValue(output["data"])
}

func childTerminalContentText(content []client.ToolCallContent) string {
	var out strings.Builder
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		out.WriteString(acpschema.ExtractTextValue(item.Content))
	}
	return out.String()
}

func childPlanTraceText(update client.PlanUpdate) string {
	if len(update.Entries) == 0 {
		return "Plan updated"
	}
	latest := update.Entries[len(update.Entries)-1]
	content := strings.TrimSpace(latest.Content)
	status := childToolStatusSuffix(latest.Status)
	switch {
	case content != "" && status != "":
		return "Plan " + content + " " + status
	case content != "":
		return "Plan " + content
	case status != "":
		return "Plan " + status
	default:
		return "Plan updated"
	}
}

func childToolLabel(title string, kind string) string {
	if text := strings.TrimSpace(title); text != "" {
		return text
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	switch strings.ToLower(kind) {
	case "think":
		return "Think"
	case "execute":
		return "Run"
	}
	parts := strings.FieldsFunc(kind, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
	if len(parts) == 0 {
		return kind
	}
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func childToolStatusSuffix(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "interrupted":
		return "interrupted"
	default:
		return ""
	}
}

func childToolInputSummary(raw any) string {
	values := acpschema.NormalizeRawMap(raw)
	if len(values) == 0 {
		return ""
	}
	if query := rawValueString(values, "query"); query != "" {
		if scope := firstNonEmpty(rawValueString(values, "path"), rawValueString(values, "cwd"), rawValueString(values, "directory")); scope != "" {
			return truncateTraceText(quoteTraceValue(query)+" in "+scope, 140)
		}
		return truncateTraceText(quoteTraceValue(query), 140)
	}
	if pattern := rawValueString(values, "pattern"); pattern != "" {
		if scope := firstNonEmpty(rawValueString(values, "path"), rawValueString(values, "cwd"), rawValueString(values, "directory")); scope != "" {
			return truncateTraceText(quoteTraceValue(pattern)+" in "+scope, 140)
		}
		return truncateTraceText(quoteTraceValue(pattern), 140)
	}
	for _, key := range []string{"path", "file_path", "filePath", "uri", "url", "command", "cmd"} {
		if value := rawValueString(values, key); value != "" {
			return truncateTraceText(value, 140)
		}
	}
	if prompt := rawValueString(values, "prompt"); prompt != "" {
		if agent := rawValueString(values, "agent"); agent != "" {
			return truncateTraceText(agent+": "+prompt, 140)
		}
		return truncateTraceText(prompt, 140)
	}
	if text := rawValueString(values, "text"); text != "" {
		return truncateTraceText(text, 140)
	}
	return ""
}

func shouldAppendChildToolSummary(label string, summary string, title string, kind string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	if containsFold(label, summary) || containsFold(label, strings.Trim(summary, `"`)) {
		return false
	}
	if strings.TrimSpace(title) != "" && strings.EqualFold(strings.TrimSpace(kind), "execute") {
		return false
	}
	return true
}

func rawValueString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	text := acpschema.ExtractTextValue(value)
	if text == "" {
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "<nil>" {
		return ""
	}
	return strings.TrimSpace(text)
}

func quoteTraceValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t") && !strings.HasPrefix(value, "\"") {
		return `"` + value + `"`
	}
	return value
}

func truncateTraceText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "..."
}

func containsFold(text string, part string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	part = strings.ToLower(strings.TrimSpace(part))
	return text != "" && part != "" && strings.Contains(text, part)
}

func normalizeTraceLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return text + "\n"
}

func chunkText(chunk client.ContentChunk) string {
	return acpingress.ContentChunkText(chunk)
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
