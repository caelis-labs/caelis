// Package loop implements the model/tool turn loop for the new engine.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
	enginecontext "github.com/OnslaughtSnail/caelis/internal/engine/context"
)

const defaultMaxToolSteps = 8

type Config struct {
	Provider     model.Provider
	Tools        tool.Registry
	ModelTools   []model.ToolSpec
	Approval     approval.Policy
	Spawner      Spawner
	Instructions []string
	Clock        func() time.Time
	MaxToolSteps int
}

type Loop struct {
	provider     model.Provider
	tools        tool.Registry
	modelTools   []model.ToolSpec
	approval     approval.Policy
	spawner      Spawner
	instructions []string
	clock        func() time.Time
	maxToolSteps int
}

type Spawner interface {
	Spawn(context.Context, SpawnRequest) (SpawnResult, error)
}

type SpawnRequest struct {
	Session    session.Session
	TurnID     string
	Surface    string
	Call       tool.Call
	Definition tool.Definition
}

type SpawnResult struct {
	Result tool.Result
	Events []session.Event
}

type Request struct {
	Session       session.Session
	Events        []session.Event
	State         session.State
	Input         string
	ContentParts  []model.ContentPart
	Instructions  []string
	Model         string
	Reasoning     model.ReasoningConfig
	ModelTools    []model.ToolSpec
	Mode          string
	TurnID        string
	Surface       string
	StartedAt     time.Time
	Emit          func(context.Context, []session.Event) error
	AwaitApproval func(context.Context, session.Event) (session.ApprovalEvent, error)
}

func New(cfg Config) (*Loop, error) {
	if cfg.Provider == nil {
		return nil, errors.New("engine/loop: model provider is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.MaxToolSteps <= 0 {
		cfg.MaxToolSteps = defaultMaxToolSteps
	}
	return &Loop{
		provider:     cfg.Provider,
		tools:        cfg.Tools,
		modelTools:   model.CloneToolSpecs(cfg.ModelTools),
		approval:     cfg.Approval,
		spawner:      cfg.Spawner,
		instructions: cloneStrings(cfg.Instructions),
		clock:        cfg.Clock,
		maxToolSteps: cfg.MaxToolSteps,
	}, nil
}

func (l *Loop) Run(ctx context.Context, req Request) ([]session.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req.Session = session.CloneSession(req.Session)
	req.TurnID = strings.TrimSpace(req.TurnID)
	if req.TurnID == "" {
		return nil, errors.New("engine/loop: turn id is required")
	}
	messages := enginecontext.Messages(req.Events)
	reviewEvents := cloneEvents(req.Events)
	userMessage := userMessage(req.Input, req.ContentParts)
	now := l.now()
	out := make([]session.Event, 0)
	record := func(event session.Event) error {
		if err := l.record(ctx, req, &out, event); err != nil {
			return err
		}
		reviewEvents = append(reviewEvents, session.CloneEvent(event))
		return nil
	}
	if err := record(session.Event{
		Type:       session.EventUser,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorUser, ID: "user", Name: "user"},
		Scope:      eventScope(req),
		Message:    &userMessage,
	}); err != nil {
		return nil, err
	}
	messages = append(messages, userMessage)

	for step := 0; step < l.maxToolSteps; step++ {
		tools, err := l.listTools(ctx)
		if err != nil {
			return nil, err
		}
		response, err := l.complete(ctx, model.Request{
			Model:        strings.TrimSpace(req.Model),
			Messages:     cloneMessages(messages),
			Tools:        modelToolSpecs(tool.ModelSpecs(tools), l.modelTools, req.ModelTools),
			Instructions: requestInstructions(l.instructions, req.Instructions),
			Reasoning:    req.Reasoning,
			Stream:       true,
			Meta:         requestMeta(req),
		})
		if err != nil {
			return nil, err
		}
		assistant := model.CloneMessage(response.Message)
		assistant.Role = model.RoleAssistant
		if assistant.Usage == nil && response.Usage != nil {
			usage := *response.Usage
			assistant.Usage = &usage
		}
		if assistant.Origin == nil && response.Origin != nil {
			origin := *response.Origin
			assistant.Origin = &origin
		}
		if err := record(session.Event{
			Type:       session.EventAssistant,
			Visibility: session.VisibilityCanonical,
			Time:       l.now(),
			Actor:      session.ActorRef{Kind: session.ActorController, ID: "builtin", Name: "assistant"},
			Scope:      eventScope(req),
			Message:    &assistant,
		}); err != nil {
			return nil, err
		}
		messages = append(messages, assistant)

		calls := assistant.ToolCalls()
		if len(calls) == 0 {
			return out, nil
		}
		for _, call := range calls {
			callEvent := session.Event{
				Type:       session.EventToolCall,
				Visibility: session.VisibilityCanonical,
				Time:       l.now(),
				Actor:      session.ActorRef{Kind: session.ActorTool, ID: strings.TrimSpace(call.ID), Name: strings.TrimSpace(call.Name)},
				Scope:      eventScope(req),
				Tool:       toolCallEvent(call),
			}
			if err := record(callEvent); err != nil {
				return nil, err
			}
			selected := l.lookupTool(call, tools)
			if selected != nil {
				decisionEvent, ok, err := l.reviewToolCall(ctx, req, reviewEvents, call, selected.Definition())
				if err != nil {
					return nil, err
				}
				if ok {
					if err := record(decisionEvent); err != nil {
						return nil, err
					}
					if decisionEvent.Approval == nil || decisionEvent.Approval.Status != session.ApprovalApproved {
						reason := "tool execution rejected"
						if decisionEvent.Approval != nil && strings.TrimSpace(decisionEvent.Approval.Reason) != "" {
							reason = strings.TrimSpace(decisionEvent.Approval.Reason)
						}
						result := tool.Result{
							ID:      strings.TrimSpace(call.ID),
							Name:    strings.TrimSpace(call.Name),
							IsError: true,
							Content: []model.Part{model.NewTextPart(reason)},
						}
						resultMessage := toolResultMessage(call, result)
						resultEvent := l.toolResultEvent(req, call, result)
						messages = append(messages, resultMessage)
						if err := record(resultEvent); err != nil {
							return nil, err
						}
						continue
					}
				}
			}
			resultMessage, childEvents, resultEvent, result, err := l.executeTool(ctx, req, call, tools)
			if err != nil {
				return nil, err
			}
			for _, event := range childEvents {
				if err := record(event); err != nil {
					return nil, err
				}
			}
			messages = append(messages, resultMessage)
			if err := record(resultEvent); err != nil {
				return nil, err
			}
			if planEvent, ok := l.planEvent(req, result); ok {
				if err := record(planEvent); err != nil {
					return nil, err
				}
			}
		}
	}
	return nil, fmt.Errorf("engine/loop: exceeded max tool steps %d", l.maxToolSteps)
}

func (l *Loop) record(ctx context.Context, req Request, out *[]session.Event, event session.Event) error {
	next := session.CloneEvent(event)
	if next.Visibility == "" {
		next.Visibility = session.VisibilityCanonical
	}
	if req.Emit != nil {
		return req.Emit(ctx, []session.Event{next})
	}
	*out = append(*out, next)
	return nil
}

func (l *Loop) complete(ctx context.Context, req model.Request) (model.Response, error) {
	stream, err := l.provider.Stream(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	defer stream.Close()
	var final *model.Response
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return model.Response{}, err
		}
		if event.Response != nil {
			response := *event.Response
			final = &response
		}
		if event.Message != nil {
			message := model.CloneMessage(*event.Message)
			final = &model.Response{Message: message, Status: model.ResponseCompleted}
		}
	}
	if final == nil {
		return model.Response{}, errors.New("engine/loop: model stream ended without final response")
	}
	return *final, nil
}

func (l *Loop) executeTool(ctx context.Context, req Request, call model.ToolCall, tools []tool.Tool) (model.Message, []session.Event, session.Event, tool.Result, error) {
	name := strings.TrimSpace(call.Name)
	selected := l.lookupTool(call, tools)
	if selected == nil {
		result := tool.Result{
			ID:      strings.TrimSpace(call.ID),
			Name:    name,
			IsError: true,
			Content: []model.Part{model.NewTextPart("tool not found: " + name)},
		}
		return toolResultMessage(call, result), nil, l.toolResultEvent(req, call, result), result, nil
	}
	if l.spawner != nil && isSpawnTool(selected.Definition(), name) {
		spawned, err := l.spawner.Spawn(ctx, SpawnRequest{
			Session:    session.CloneSession(req.Session),
			TurnID:     req.TurnID,
			Surface:    req.Surface,
			Definition: selected.Definition(),
			Call: tool.Call{
				ID:    strings.TrimSpace(call.ID),
				Name:  name,
				Input: call.Input,
			},
		})
		result, _ := tool.CloneResult(spawned.Result, nil)
		if err != nil {
			result = tool.Result{
				ID:      strings.TrimSpace(call.ID),
				Name:    name,
				IsError: true,
				Content: []model.Part{model.NewTextPart(err.Error())},
			}
		}
		if strings.TrimSpace(result.ID) == "" {
			result.ID = strings.TrimSpace(call.ID)
		}
		if strings.TrimSpace(result.Name) == "" {
			result.Name = name
		}
		return toolResultMessage(call, result), normalizeSpawnEvents(req, spawned.Events), l.toolResultEvent(req, call, result), result, nil
	}
	result, err := selected.Call(ctx, tool.Call{
		ID:    strings.TrimSpace(call.ID),
		Name:  name,
		Input: call.Input,
	})
	if err != nil {
		result = tool.Result{
			ID:      strings.TrimSpace(call.ID),
			Name:    name,
			IsError: true,
			Content: []model.Part{model.NewTextPart(err.Error())},
		}
	}
	return toolResultMessage(call, result), nil, l.toolResultEvent(req, call, result), result, nil
}

func (l *Loop) lookupTool(call model.ToolCall, tools []tool.Tool) tool.Tool {
	name := strings.TrimSpace(call.Name)
	for _, candidate := range tools {
		if candidate == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Definition().Name), name) {
			return candidate
		}
	}
	return nil
}

func isSpawnTool(def tool.Definition, name string) bool {
	if kind, _ := def.Meta["caelis.kind"].(string); strings.EqualFold(strings.TrimSpace(kind), "spawn") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(def.Name), "SPAWN") || strings.EqualFold(strings.TrimSpace(name), "SPAWN")
}

func normalizeSpawnEvents(req Request, events []session.Event) []session.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		next := session.CloneEvent(event)
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		if next.Scope == nil {
			next.Scope = eventScope(req)
		}
		if next.Scope.TurnID == "" {
			next.Scope.TurnID = strings.TrimSpace(req.TurnID)
		}
		if next.Scope.Source == "" {
			next.Scope.Source = "spawn"
		}
		out = append(out, next)
	}
	return out
}

func (l *Loop) reviewToolCall(ctx context.Context, req Request, reviewEvents []session.Event, call model.ToolCall, def tool.Definition) (session.Event, bool, error) {
	if l.approval == nil {
		return session.Event{}, false, nil
	}
	decision, err := l.approval.ReviewToolCall(ctx, approval.Request{
		Session:    req.Session,
		TurnID:     req.TurnID,
		Surface:    req.Surface,
		Mode:       req.Mode,
		State:      req.State,
		Events:     cloneEvents(reviewEvents),
		Model:      strings.TrimSpace(req.Model),
		Call:       call,
		Definition: def,
	})
	if err != nil {
		return session.Event{}, false, err
	}
	toolEvent := toolCallEvent(call)
	toolEvent.Status = session.ToolWaitingApproval
	meta := approvalMeta(decision.Meta)
	switch decision.Verdict {
	case approval.VerdictAsk:
		pending := session.Event{
			Type:       session.EventApproval,
			Visibility: session.VisibilityCanonical,
			Time:       l.now(),
			Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "approval", Name: "approval"},
			Scope:      eventScope(req),
			Tool:       toolEvent,
			Meta:       meta,
			Approval: &session.ApprovalEvent{
				ID:      approvalID(call),
				Status:  session.ApprovalPending,
				Tool:    toolEvent,
				Options: approvalOptions(decision.Options),
				Reason:  strings.TrimSpace(decision.Reason),
			},
		}
		if req.AwaitApproval == nil {
			rejected := pending
			rejected.Time = l.now()
			rejected.Approval.Status = session.ApprovalRejected
			rejected.Approval.Reason = "approval required but no resolver is attached"
			return rejected, true, nil
		}
		result, err := req.AwaitApproval(ctx, pending)
		if err != nil {
			return session.Event{}, false, err
		}
		if result.ID == "" {
			result.ID = approvalID(call)
		}
		if result.Tool == nil {
			result.Tool = toolEvent
		}
		return session.Event{
			Type:       session.EventApproval,
			Visibility: session.VisibilityCanonical,
			Time:       l.now(),
			Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "approval", Name: "approval"},
			Scope:      eventScope(req),
			Tool:       result.Tool,
			Approval:   &result,
			Meta:       meta,
		}, true, nil
	case approval.VerdictDeny:
		return session.Event{
			Type:       session.EventApproval,
			Visibility: session.VisibilityCanonical,
			Time:       l.now(),
			Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "approval", Name: "approval"},
			Scope:      eventScope(req),
			Tool:       toolEvent,
			Meta:       meta,
			Approval: &session.ApprovalEvent{
				ID:     approvalID(call),
				Status: session.ApprovalRejected,
				Tool:   toolEvent,
				Reason: strings.TrimSpace(decision.Reason),
			},
		}, true, nil
	case approval.VerdictAllow:
		return session.Event{
			Type:       session.EventApproval,
			Visibility: session.VisibilityCanonical,
			Time:       l.now(),
			Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "approval", Name: "approval"},
			Scope:      eventScope(req),
			Tool:       toolEvent,
			Meta:       meta,
			Approval: &session.ApprovalEvent{
				ID:     approvalID(call),
				Status: session.ApprovalApproved,
				Tool:   toolEvent,
				Reason: strings.TrimSpace(decision.Reason),
			},
		}, true, nil
	default:
		return session.Event{}, false, nil
	}
}

func (l *Loop) listTools(ctx context.Context) ([]tool.Tool, error) {
	if l.tools == nil {
		return nil, nil
	}
	return l.tools.List(ctx)
}

func (l *Loop) now() time.Time {
	return l.clock()
}

func cloneMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}

func modelToolSpecs(groups ...[]model.ToolSpec) []model.ToolSpec {
	var out []model.ToolSpec
	for _, group := range groups {
		for _, spec := range group {
			out = append(out, model.CloneToolSpec(spec))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, value := range in {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func requestInstructions(base []string, turn []string) []string {
	out := cloneStrings(base)
	out = append(out, cloneStrings(turn)...)
	if len(out) == 0 {
		return nil
	}
	return out
}

func requestMeta(req Request) map[string]any {
	meta := map[string]any{}
	if mode := strings.TrimSpace(req.Mode); mode != "" {
		meta["caelis.session_mode"] = mode
	}
	if surface := strings.TrimSpace(req.Surface); surface != "" {
		meta["caelis.surface"] = surface
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func userMessage(input string, parts []model.ContentPart) model.Message {
	message := model.Message{Role: model.RoleUser}
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartText:
			if text := strings.TrimSpace(part.Text); text != "" {
				message.Parts = append(message.Parts, model.NewTextPart(text))
			}
		case model.ContentPartImage:
			message.Parts = append(message.Parts, model.Part{
				Kind: model.PartMedia,
				Media: &model.MediaPart{
					Modality: model.MediaImage,
					MimeType: strings.TrimSpace(part.MimeType),
					Name:     strings.TrimSpace(part.FileName),
					Source: model.MediaSource{
						Kind: model.MediaLocalRef,
						Data: strings.TrimSpace(part.Data),
						URI:  strings.TrimSpace(part.URI),
					},
				},
			})
		case model.ContentPartFile:
			message.Parts = append(message.Parts, model.Part{
				Kind: model.PartFileRef,
				FileRef: &model.FileRefPart{
					Name:     strings.TrimSpace(part.FileName),
					MimeType: strings.TrimSpace(part.MimeType),
					URI:      strings.TrimSpace(part.URI),
					LocalRef: strings.TrimSpace(part.Data),
				},
			})
		}
	}
	if len(message.Parts) == 0 {
		message.Parts = append(message.Parts, model.NewTextPart(strings.TrimSpace(input)))
	}
	return message
}

func eventScope(req Request) *session.EventScope {
	return &session.EventScope{
		TurnID: strings.TrimSpace(req.TurnID),
		Source: strings.TrimSpace(req.Surface),
		Controller: session.ControllerBinding{
			Kind: session.ControllerBuiltin,
			ID:   "builtin",
		},
	}
}

func toolCallEvent(call model.ToolCall) *session.ToolEvent {
	input := map[string]any{}
	if len(call.Input) > 0 {
		_ = json.Unmarshal(call.Input, &input)
	}
	return &session.ToolEvent{
		ID:     strings.TrimSpace(call.ID),
		Name:   strings.TrimSpace(call.Name),
		Status: session.ToolStarted,
		Input:  input,
	}
}

func approvalID(call model.ToolCall) string {
	if id := strings.TrimSpace(call.ID); id != "" {
		return "approval-" + id
	}
	if name := strings.TrimSpace(call.Name); name != "" {
		return "approval-" + name
	}
	return "approval"
}

func approvalOptions(options []session.ApprovalOption) []session.ApprovalOption {
	if len(options) > 0 {
		return append([]session.ApprovalOption(nil), options...)
	}
	return []session.ApprovalOption{
		{ID: approval.OptionAllowOnce, Name: "Allow once", Kind: "allow"},
		{ID: approval.OptionRejectOnce, Name: "Reject", Kind: "reject"},
	}
}

func approvalMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	return maps.Clone(meta)
}

func toolResultMessage(call model.ToolCall, result tool.Result) model.Message {
	return model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartToolResult,
			ToolResult: &model.ToolResultPart{
				ToolCallID: strings.TrimSpace(call.ID),
				Name:       strings.TrimSpace(call.Name),
				Content:    model.CloneParts(result.Content),
				IsError:    result.IsError,
			},
		}},
	}
}

func (l *Loop) toolResultEvent(req Request, call model.ToolCall, result tool.Result) session.Event {
	message := toolResultMessage(call, result)
	status := session.ToolCompleted
	if result.IsError {
		status = session.ToolFailed
	}
	return session.Event{
		Type:       session.EventToolResult,
		Visibility: session.VisibilityCanonical,
		Time:       l.now(),
		Actor:      session.ActorRef{Kind: session.ActorTool, ID: strings.TrimSpace(call.ID), Name: strings.TrimSpace(call.Name)},
		Scope:      eventScope(req),
		Message:    &message,
		Tool: &session.ToolEvent{
			ID:      strings.TrimSpace(call.ID),
			Name:    strings.TrimSpace(call.Name),
			Status:  status,
			Output:  toolOutput(result.Content),
			Content: toolContent(result.Content),
			Meta:    result.Meta,
		},
	}
}

func (l *Loop) planEvent(req Request, result tool.Result) (session.Event, bool) {
	entries, ok := planEntriesFromMeta(result.Meta)
	if !ok {
		return session.Event{}, false
	}
	return session.Event{
		Type:       session.EventPlan,
		Visibility: session.VisibilityCanonical,
		Time:       l.now(),
		Actor:      session.ActorRef{Kind: session.ActorTool, ID: strings.TrimSpace(result.ID), Name: strings.TrimSpace(result.Name)},
		Scope:      eventScope(req),
		Plan:       entries,
		Meta: map[string]any{
			"tool_call_id": strings.TrimSpace(result.ID),
			"tool_name":    strings.TrimSpace(result.Name),
			"explanation":  strings.TrimSpace(metaString(result.Meta, "explanation")),
		},
	}, true
}

func planEntriesFromMeta(meta map[string]any) ([]session.PlanEntry, bool) {
	raw, ok := meta["plan_entries"]
	if !ok || raw == nil {
		return nil, false
	}
	switch typed := raw.(type) {
	case []session.PlanEntry:
		return normalizePlanEntries(typed), true
	case []map[string]any:
		return normalizePlanEntries(planEntriesFromMaps(typed)), true
	case []any:
		return normalizePlanEntries(planEntriesFromAny(typed)), true
	default:
		return nil, false
	}
}

func planEntriesFromAny(values []any) []session.PlanEntry {
	out := make([]session.PlanEntry, 0, len(values))
	for _, item := range values {
		switch typed := item.(type) {
		case session.PlanEntry:
			out = append(out, typed)
		case map[string]any:
			out = append(out, planEntryFromMap(typed))
		}
	}
	return out
}

func planEntriesFromMaps(values []map[string]any) []session.PlanEntry {
	out := make([]session.PlanEntry, 0, len(values))
	for _, item := range values {
		out = append(out, planEntryFromMap(item))
	}
	return out
}

func planEntryFromMap(value map[string]any) session.PlanEntry {
	content, _ := value["content"].(string)
	status, _ := value["status"].(string)
	return session.PlanEntry{
		Content: strings.TrimSpace(content),
		Status:  strings.TrimSpace(status),
	}
}

func normalizePlanEntries(entries []session.PlanEntry) []session.PlanEntry {
	out := make([]session.PlanEntry, 0, len(entries))
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		out = append(out, session.PlanEntry{
			Content: content,
			Status:  strings.TrimSpace(entry.Status),
		})
	}
	return out
}

func metaString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}

func toolContent(parts []model.Part) []session.ToolContent {
	if len(parts) == 0 {
		return nil
	}
	out := make([]session.ToolContent, 0, len(parts))
	for _, part := range parts {
		if part.Kind == model.PartText && part.Text != nil {
			out = append(out, session.ToolContent{Type: "text", Text: part.Text.Text})
		}
	}
	return out
}

func toolOutput(parts []model.Part) map[string]any {
	for _, part := range parts {
		if part.Kind != model.PartJSON || part.JSON == nil || len(part.JSON.Value) == 0 {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(part.JSON.Value, &out); err != nil {
			continue
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func cloneEvents(events []session.Event) []session.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		out = append(out, session.CloneEvent(event))
	}
	return out
}
