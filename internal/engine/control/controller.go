package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

type ControllerRunner struct {
	Store   session.Store
	Now     func() time.Time
	Tracker ControllerInvocationTracker
}

type ControllerRequest struct {
	SessionRef                session.Ref
	Workspace                 session.Workspace
	TurnID                    string
	Controller                session.ControllerBinding
	ControllerModel           string
	ControllerReasoningEffort string
	ControllerMode            string
	Input                     string
	ContentParts              []model.ContentPart
	Agent                     AgentSession
}

type ControllerResult struct {
	RemoteSessionID string
	Events          []session.Event
	Cursor          session.Cursor
	ConfigOptions   []ConfigOption
}

type ControllerInvocationPhase string

const (
	ControllerInvocationStarted       ControllerInvocationPhase = "started"
	ControllerInvocationRemoteSession ControllerInvocationPhase = "remote_session"
	ControllerInvocationCompleted     ControllerInvocationPhase = "completed"
	ControllerInvocationFailed        ControllerInvocationPhase = "failed"
)

type ControllerInvocationState struct {
	Phase                     ControllerInvocationPhase `json:"phase,omitempty"`
	SessionRef                session.Ref               `json:"session_ref,omitempty"`
	Workspace                 session.Workspace         `json:"workspace,omitempty"`
	TurnID                    string                    `json:"turn_id,omitempty"`
	Controller                session.ControllerBinding `json:"controller,omitempty"`
	RemoteSessionID           string                    `json:"remote_session_id,omitempty"`
	ControllerModel           string                    `json:"controller_model,omitempty"`
	ControllerReasoningEffort string                    `json:"controller_reasoning_effort,omitempty"`
	ControllerMode            string                    `json:"controller_mode,omitempty"`
	Input                     string                    `json:"input,omitempty"`
	ContentParts              []model.ContentPart       `json:"content_parts,omitempty"`
	ConfigOptions             []ConfigOption            `json:"config_options,omitempty"`
	Error                     string                    `json:"error,omitempty"`
	Time                      time.Time                 `json:"time,omitempty"`
}

type ControllerInvocationTracker interface {
	ControllerInvocationChanged(context.Context, ControllerInvocationState) error
}

type ConfigChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type ConfigOption struct {
	Type         string         `json:"type,omitempty"`
	ID           string         `json:"id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Description  string         `json:"description,omitempty"`
	Category     string         `json:"category,omitempty"`
	CurrentValue string         `json:"current_value,omitempty"`
	Options      []ConfigChoice `json:"options,omitempty"`
}

type AgentSessionState struct {
	RemoteSessionID string
	ConfigOptions   []ConfigOption
}

type ConfigurableAgentSession interface {
	NewSessionState(context.Context, session.Workspace) (AgentSessionState, error)
	ResumeSessionState(context.Context, string, session.Workspace) (AgentSessionState, error)
	SetConfigOption(context.Context, string, string, any) (AgentSessionState, error)
}

func (r ControllerRunner) Invoke(ctx context.Context, req ControllerRequest) (ControllerResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.Store == nil {
		return ControllerResult{}, errors.New("engine/control: session store is required")
	}
	if req.Agent == nil {
		return ControllerResult{}, errors.New("engine/control: agent session is required")
	}
	ref := session.NormalizeRef(req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		return ControllerResult{}, errors.New("engine/control: session id is required")
	}
	snapshot, err := r.Store.Load(ctx, ref)
	if err != nil {
		return ControllerResult{}, err
	}
	workspace := req.Workspace
	if strings.TrimSpace(workspace.Key) == "" && strings.TrimSpace(workspace.CWD) == "" {
		workspace = snapshot.Session.Workspace
	}
	state := r.invocationState(ref, workspace, req, ControllerInvocationStarted)
	if err := r.track(ctx, state); err != nil {
		return ControllerResult{}, err
	}
	if err := req.Agent.Initialize(ctx); err != nil {
		return r.fail(ctx, state, err)
	}
	remoteSessionID, configOptions, err := r.startControllerSession(ctx, req.Agent, req.Controller, workspace)
	if err != nil {
		return r.fail(ctx, state, err)
	}
	state.RemoteSessionID = remoteSessionID
	state.Controller.RemoteSessionID = remoteSessionID
	state.ConfigOptions = cloneConfigOptions(configOptions)
	if configurable, ok := req.Agent.(ConfigurableAgentSession); ok {
		configOptions, err = applyControllerConfigIntent(ctx, configurable, remoteSessionID, configOptions, req)
		if err != nil {
			return r.fail(ctx, state, err)
		}
	}
	state.ConfigOptions = cloneConfigOptions(configOptions)
	if err := r.track(ctx, withControllerPhase(state, ControllerInvocationRemoteSession, r.now())); err != nil {
		return ControllerResult{}, err
	}
	parts := model.CloneContentParts(req.ContentParts)
	if len(parts) == 0 && strings.TrimSpace(req.Input) != "" {
		parts = []model.ContentPart{{Type: model.ContentPartText, Text: req.Input}}
	}
	events, err := req.Agent.Prompt(ctx, remoteSessionID, parts)
	if err != nil {
		return r.fail(ctx, state, err)
	}
	events = normalizeControllerEvents(snapshot.Session.Ref.SessionID, remoteSessionID, req.Controller, events, configOptions, r.now())
	cursor, err := r.Store.Append(ctx, snapshot.Session.Ref, events)
	if err != nil {
		return r.fail(ctx, state, err)
	}
	if err := r.track(ctx, withControllerPhase(state, ControllerInvocationCompleted, r.now())); err != nil {
		return ControllerResult{}, err
	}
	return ControllerResult{
		RemoteSessionID: remoteSessionID,
		Events:          events,
		Cursor:          cursor,
		ConfigOptions:   cloneConfigOptions(configOptions),
	}, nil
}

func (r ControllerRunner) invocationState(ref session.Ref, workspace session.Workspace, req ControllerRequest, phase ControllerInvocationPhase) ControllerInvocationState {
	return ControllerInvocationState{
		Phase:                     phase,
		SessionRef:                ref,
		Workspace:                 workspace,
		TurnID:                    strings.TrimSpace(req.TurnID),
		Controller:                req.Controller,
		RemoteSessionID:           strings.TrimSpace(req.Controller.RemoteSessionID),
		ControllerModel:           strings.TrimSpace(req.ControllerModel),
		ControllerReasoningEffort: strings.TrimSpace(req.ControllerReasoningEffort),
		ControllerMode:            strings.TrimSpace(req.ControllerMode),
		Input:                     strings.TrimSpace(req.Input),
		ContentParts:              model.CloneContentParts(req.ContentParts),
		Time:                      r.now(),
	}
}

func (r ControllerRunner) fail(ctx context.Context, state ControllerInvocationState, cause error) (ControllerResult, error) {
	if cause == nil {
		return ControllerResult{}, nil
	}
	state.Error = strings.TrimSpace(cause.Error())
	if err := r.track(ctx, withControllerPhase(state, ControllerInvocationFailed, r.now())); err != nil {
		return ControllerResult{}, errors.Join(cause, err)
	}
	return ControllerResult{}, cause
}

func (r ControllerRunner) track(ctx context.Context, state ControllerInvocationState) error {
	if r.Tracker == nil {
		return nil
	}
	state.SessionRef = session.NormalizeRef(state.SessionRef)
	state.Workspace.Key = strings.TrimSpace(state.Workspace.Key)
	state.Workspace.CWD = strings.TrimSpace(state.Workspace.CWD)
	state.TurnID = strings.TrimSpace(state.TurnID)
	state.Controller = normalizeControllerBinding(state.Controller)
	state.RemoteSessionID = strings.TrimSpace(state.RemoteSessionID)
	state.ControllerModel = strings.TrimSpace(state.ControllerModel)
	state.ControllerReasoningEffort = strings.TrimSpace(state.ControllerReasoningEffort)
	state.ControllerMode = strings.TrimSpace(state.ControllerMode)
	state.Input = strings.TrimSpace(state.Input)
	state.ContentParts = model.CloneContentParts(state.ContentParts)
	state.ConfigOptions = cloneConfigOptions(state.ConfigOptions)
	state.Error = strings.TrimSpace(state.Error)
	if state.Time.IsZero() {
		state.Time = r.now()
	}
	return r.Tracker.ControllerInvocationChanged(ctx, state)
}

func withControllerPhase(state ControllerInvocationState, phase ControllerInvocationPhase, at time.Time) ControllerInvocationState {
	state.Phase = phase
	state.Time = at
	return state
}

func (r ControllerRunner) startControllerSession(ctx context.Context, agent AgentSession, controller session.ControllerBinding, workspace session.Workspace) (string, []ConfigOption, error) {
	remoteSessionID := strings.TrimSpace(controller.RemoteSessionID)
	configurable, ok := agent.(ConfigurableAgentSession)
	if remoteSessionID != "" {
		if !ok {
			return remoteSessionID, nil, nil
		}
		state, err := configurable.ResumeSessionState(ctx, remoteSessionID, workspace)
		if err != nil {
			return "", nil, err
		}
		return firstNonEmpty(state.RemoteSessionID, remoteSessionID), cloneConfigOptions(state.ConfigOptions), nil
	}
	if !ok {
		next, err := agent.NewSession(ctx, workspace)
		return strings.TrimSpace(next), nil, err
	}
	state, err := configurable.NewSessionState(ctx, workspace)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(state.RemoteSessionID), cloneConfigOptions(state.ConfigOptions), nil
}

func normalizeControllerBinding(in session.ControllerBinding) session.ControllerBinding {
	in.ID = strings.TrimSpace(in.ID)
	in.AgentName = strings.TrimSpace(in.AgentName)
	in.Label = strings.TrimSpace(in.Label)
	in.EpochID = strings.TrimSpace(in.EpochID)
	in.RemoteSessionID = strings.TrimSpace(in.RemoteSessionID)
	in.Source = strings.TrimSpace(in.Source)
	return in
}

func applyControllerConfigIntent(ctx context.Context, agent ConfigurableAgentSession, remoteSessionID string, options []ConfigOption, req ControllerRequest) ([]ConfigOption, error) {
	var err error
	options = cloneConfigOptions(options)
	if value := strings.TrimSpace(req.ControllerModel); value != "" {
		options, err = applyControllerConfigOption(ctx, agent, remoteSessionID, options, "model", value)
		if err != nil {
			return nil, err
		}
	}
	if value := strings.TrimSpace(req.ControllerReasoningEffort); value != "" {
		options, err = applyControllerConfigOption(ctx, agent, remoteSessionID, options, "reasoning", value)
		if err != nil {
			return nil, err
		}
	}
	if value := strings.TrimSpace(req.ControllerMode); value != "" {
		options, err = applyControllerConfigOption(ctx, agent, remoteSessionID, options, "mode", value)
		if err != nil {
			return nil, err
		}
	}
	return options, nil
}

func applyControllerConfigOption(ctx context.Context, agent ConfigurableAgentSession, remoteSessionID string, options []ConfigOption, kind string, requested string) ([]ConfigOption, error) {
	option, ok := findControllerConfigOption(options, kind)
	if !ok {
		return options, nil
	}
	remoteSessionID = strings.TrimSpace(remoteSessionID)
	if remoteSessionID == "" {
		return nil, errors.New("engine/control: remote controller session id is required to apply config")
	}
	value, ok := matchControllerConfigChoice(option, requested)
	if !ok {
		return nil, errors.New("engine/control: remote controller config option " + option.ID + " does not support value " + requested)
	}
	if strings.EqualFold(strings.TrimSpace(option.CurrentValue), value) {
		return options, nil
	}
	state, err := agent.SetConfigOption(ctx, strings.TrimSpace(remoteSessionID), option.ID, value)
	if err != nil {
		return nil, err
	}
	if len(state.ConfigOptions) == 0 {
		option.CurrentValue = value
		return mergeConfigOptions(options, []ConfigOption{option}), nil
	}
	return mergeConfigOptions(options, state.ConfigOptions), nil
}

func findControllerConfigOption(options []ConfigOption, kind string) (ConfigOption, bool) {
	for _, option := range options {
		if ConfigOptionKind(option) == kind {
			return cloneConfigOption(option), true
		}
	}
	return ConfigOption{}, false
}

func ConfigOptionKind(option ConfigOption) string {
	id := strings.ToLower(strings.TrimSpace(option.ID))
	category := strings.ToLower(strings.TrimSpace(option.Category))
	name := strings.ToLower(strings.TrimSpace(option.Name))
	switch {
	case id == "model" || category == "model":
		return "model"
	case id == "reasoning_effort" || id == "reasoning" || category == "thought_level" || category == "reasoning":
		return "reasoning"
	case id == "mode" || category == "mode":
		return "mode"
	case strings.Contains(name, "reasoning"):
		return "reasoning"
	case strings.Contains(name, "model"):
		return "model"
	case strings.Contains(name, "mode"):
		return "mode"
	default:
		return ""
	}
}

func matchControllerConfigChoice(option ConfigOption, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false
	}
	if len(option.Options) == 0 {
		return requested, true
	}
	for _, choice := range option.Options {
		for _, candidate := range []string{choice.Value, choice.Name} {
			if strings.EqualFold(strings.TrimSpace(candidate), requested) {
				return firstNonEmpty(choice.Value, choice.Name, requested), true
			}
		}
	}
	return "", false
}

func (r ControllerRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func normalizeControllerEvents(sessionID string, remoteSessionID string, controller session.ControllerBinding, events []session.Event, configOptions []ConfigOption, now time.Time) []session.Event {
	if len(events) == 0 {
		return nil
	}
	controller.RemoteSessionID = strings.TrimSpace(remoteSessionID)
	if strings.TrimSpace(controller.ID) == "" {
		controller.ID = firstNonEmpty(controller.AgentName, remoteSessionID, "external-acp")
	}
	if controller.Kind == "" {
		controller.Kind = session.ControllerACP
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		next := session.CloneEvent(event)
		next.SessionID = strings.TrimSpace(sessionID)
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		if next.Time.IsZero() {
			next.Time = now
		}
		if next.Actor.Kind == "" || next.Actor.Kind == session.ActorParticipant {
			next.Actor = session.ActorRef{
				Kind: session.ActorController,
				ID:   controller.ID,
				Name: firstNonEmpty(controller.Label, controller.AgentName, controller.ID),
			}
		}
		if next.Scope == nil {
			next.Scope = &session.EventScope{}
		}
		next.Scope.Source = firstNonEmpty(next.Scope.Source, "external_acp_controller")
		next.Scope.Controller = controller
		next.Scope.Participant = session.ParticipantBinding{}
		if next.Scope.ACP.SessionID == "" {
			next.Scope.ACP.SessionID = strings.TrimSpace(remoteSessionID)
		}
		if len(configOptions) > 0 {
			if next.Meta == nil {
				next.Meta = map[string]any{}
			}
			next.Meta["controller_config_options"] = cloneConfigOptions(configOptions)
		}
		out = append(out, next)
	}
	return out
}

func cloneConfigOptions(in []ConfigOption) []ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]ConfigOption, 0, len(in))
	for _, option := range in {
		out = append(out, cloneConfigOption(option))
	}
	return out
}

func cloneConfigOption(option ConfigOption) ConfigOption {
	option.ID = strings.TrimSpace(option.ID)
	option.Name = strings.TrimSpace(option.Name)
	option.Description = strings.TrimSpace(option.Description)
	option.Category = strings.TrimSpace(option.Category)
	option.CurrentValue = strings.TrimSpace(option.CurrentValue)
	option.Options = append([]ConfigChoice(nil), option.Options...)
	return option
}

func mergeConfigOptions(existing []ConfigOption, updates []ConfigOption) []ConfigOption {
	if len(existing) == 0 {
		return cloneConfigOptions(updates)
	}
	if len(updates) == 0 {
		return cloneConfigOptions(existing)
	}
	out := cloneConfigOptions(existing)
	for _, update := range updates {
		update = cloneConfigOption(update)
		if strings.TrimSpace(update.ID) == "" {
			continue
		}
		replaced := false
		for i, item := range out {
			if strings.EqualFold(strings.TrimSpace(item.ID), update.ID) {
				out[i] = update
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, update)
		}
	}
	return out
}
