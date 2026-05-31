package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

type ControllerConfigChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type ControllerMode struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type ControllerStatus struct {
	SessionRef      session.Ref              `json:"session_ref,omitempty"`
	Agent           string                   `json:"agent,omitempty"`
	RemoteSessionID string                   `json:"remote_session_id,omitempty"`
	Model           string                   `json:"model,omitempty"`
	ModelOptions    []ControllerConfigChoice `json:"model_options,omitempty"`
	ReasoningEffort string                   `json:"reasoning_effort,omitempty"`
	EffortOptions   []ControllerConfigChoice `json:"effort_options,omitempty"`
	Mode            string                   `json:"mode,omitempty"`
	ModeOptions     []ControllerMode         `json:"mode_options,omitempty"`
}

type ControllerHandoffRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
	Target     string      `json:"target,omitempty"`
	Source     string      `json:"source,omitempty"`
	Reason     string      `json:"reason,omitempty"`
}

type ControllerHandoffResult struct {
	Controller session.ControllerBinding `json:"controller,omitempty"`
	Status     ControllerStatus          `json:"status,omitempty"`
	ActiveACP  bool                      `json:"active_acp,omitempty"`
}

type ControllerService struct {
	services Services
}

func (s ControllerService) Status(ctx context.Context, ref session.Ref) (ControllerStatus, bool, error) {
	snapshot, controller, ok, err := s.activeControllerSnapshot(ctx, ref)
	if err != nil || !ok {
		return ControllerStatus{}, false, err
	}
	status, err := s.statusFromSnapshot(ctx, snapshot, controller)
	if err != nil {
		return ControllerStatus{}, false, err
	}
	return status, true, nil
}

func (s ControllerService) Handoff(ctx context.Context, req ControllerHandoffRequest) (ControllerHandoffResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.services.engine == nil {
		return ControllerHandoffResult{}, errors.New("app/services: runtime engine is required")
	}
	snapshot, err := s.services.Sessions().Load(ctx, req.SessionRef)
	if err != nil {
		return ControllerHandoffResult{}, err
	}
	controller, activeACP, err := s.controllerForHandoff(ctx, req.Target, req.Source)
	if err != nil {
		return ControllerHandoffResult{}, err
	}
	event := controllerHandoffEvent(controller, req.Source, req.Reason)
	if _, err := s.services.engine.RecordEvents(ctx, snapshot.Session.Ref, []session.Event{event}); err != nil {
		return ControllerHandoffResult{}, err
	}
	snapshot.Events = append(snapshot.Events, event)
	snapshot.Session.Controller = controllerFromSnapshot(snapshot)
	result := ControllerHandoffResult{
		Controller: controller,
		ActiveACP:  activeACP,
	}
	if activeACP {
		status, err := s.statusFromSnapshot(ctx, snapshot, controller)
		if err != nil {
			return ControllerHandoffResult{}, err
		}
		result.Status = status
	}
	return result, nil
}

func (s ControllerService) controllerForHandoff(ctx context.Context, target string, source string) (session.ControllerBinding, bool, error) {
	target = strings.TrimSpace(target)
	switch strings.ToLower(target) {
	case "", "main", "local", "kernel":
		return session.ControllerBinding{
			Kind:       session.ControllerBuiltin,
			ID:         "builtin",
			AgentName:  "local",
			Label:      "local kernel",
			EpochID:    controllerEpochID(),
			AttachedAt: time.Now().UTC(),
			Source:     firstNonEmpty(source, "app_command_handoff"),
		}, false, nil
	default:
		agent, ok, err := s.lookupControllerAgent(ctx, target)
		if err != nil {
			return session.ControllerBinding{}, false, err
		}
		if !ok {
			return session.ControllerBinding{}, false, fmt.Errorf("app/services: ACP agent %q is not configured", target)
		}
		id := firstNonEmpty(agent.ID, agent.Name, agent.Command)
		return session.ControllerBinding{
			Kind:       session.ControllerACP,
			ID:         id,
			AgentName:  id,
			Label:      firstNonEmpty(agent.Name, id),
			EpochID:    controllerEpochID(),
			AttachedAt: time.Now().UTC(),
			Source:     firstNonEmpty(source, "app_command_handoff"),
		}, true, nil
	}
}

func (s ControllerService) lookupControllerAgent(ctx context.Context, target string) (AgentDescriptor, bool, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return AgentDescriptor{}, false, nil
	}
	agents, err := s.services.Agents().List(ctx)
	if err != nil {
		return AgentDescriptor{}, false, err
	}
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.ID), target) ||
			strings.EqualFold(strings.TrimSpace(agent.Name), target) ||
			strings.EqualFold(strings.TrimSpace(agentLookupKey(agent)), target) {
			return normalizeAgentDescriptor(agent), true, nil
		}
	}
	return AgentDescriptor{}, false, nil
}

func (s ControllerService) SetModel(ctx context.Context, ref session.Ref, modelRef string, reasoningEffort string) (ControllerStatus, error) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return ControllerStatus{}, errors.New("app/services: controller model is required")
	}
	snapshot, controller, ok, err := s.activeControllerSnapshot(ctx, ref)
	if err != nil {
		return ControllerStatus{}, err
	}
	if !ok {
		return ControllerStatus{}, errors.New("app/services: no active ACP controller")
	}
	storedModel := modelRef
	storedReasoning := strings.TrimSpace(reasoningEffort)
	if cfg, err := s.services.Models().Resolve(ctx, modelRef); err == nil {
		storedModel = firstNonEmpty(strings.TrimSpace(cfg.Alias), strings.TrimSpace(cfg.ID), strings.TrimSpace(cfg.Model), modelRef)
		if storedReasoning != "" {
			matched, ok := matchControllerReasoningEffort(s.controllerEffortOptions(ctx, storedModel), storedReasoning)
			if !ok {
				return ControllerStatus{}, fmt.Errorf("app/services: controller model %q does not support reasoning effort %q", modelRef, storedReasoning)
			}
			storedReasoning = matched
		}
	}
	configRef := controllerConfigRef(controller)
	if err := s.services.engine.UpdateSessionState(ctx, snapshot.Session.Ref, func(state session.State) (session.State, error) {
		next := cloneState(state)
		if next == nil {
			next = session.State{}
		}
		next[StateControllerConfigRef] = configRef
		next[StateControllerModel] = storedModel
		if storedReasoning != "" {
			next[StateControllerReasoning] = storedReasoning
		} else {
			delete(next, StateControllerReasoning)
		}
		return next, nil
	}); err != nil {
		return ControllerStatus{}, err
	}
	snapshot.State = cloneState(snapshot.State)
	if snapshot.State == nil {
		snapshot.State = session.State{}
	}
	snapshot.State[StateControllerConfigRef] = configRef
	snapshot.State[StateControllerModel] = storedModel
	if storedReasoning != "" {
		snapshot.State[StateControllerReasoning] = storedReasoning
	} else {
		delete(snapshot.State, StateControllerReasoning)
	}
	return s.statusFromSnapshot(ctx, snapshot, controller)
}

func (s ControllerService) SetMode(ctx context.Context, ref session.Ref, mode string) (ControllerStatus, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return ControllerStatus{}, errors.New("app/services: controller mode is required")
	}
	snapshot, controller, ok, err := s.activeControllerSnapshot(ctx, ref)
	if err != nil {
		return ControllerStatus{}, err
	}
	if !ok {
		return ControllerStatus{}, errors.New("app/services: no active ACP controller")
	}
	configRef := controllerConfigRef(controller)
	if err := s.services.engine.UpdateSessionState(ctx, snapshot.Session.Ref, func(state session.State) (session.State, error) {
		next := cloneState(state)
		if next == nil {
			next = session.State{}
		}
		next[StateControllerConfigRef] = configRef
		next[StateControllerMode] = mode
		return next, nil
	}); err != nil {
		return ControllerStatus{}, err
	}
	snapshot.State = cloneState(snapshot.State)
	if snapshot.State == nil {
		snapshot.State = session.State{}
	}
	snapshot.State[StateControllerConfigRef] = configRef
	snapshot.State[StateControllerMode] = mode
	return s.statusFromSnapshot(ctx, snapshot, controller)
}

func (s ControllerService) CycleMode(ctx context.Context, ref session.Ref) (ControllerStatus, error) {
	status, ok, err := s.Status(ctx, ref)
	if err != nil {
		return ControllerStatus{}, err
	}
	if !ok {
		return ControllerStatus{}, errors.New("app/services: no active ACP controller")
	}
	next, err := nextControllerMode(status)
	if err != nil {
		return ControllerStatus{}, err
	}
	return s.SetMode(ctx, ref, next.ID)
}

func (s ControllerService) activeControllerSnapshot(ctx context.Context, ref session.Ref) (session.Snapshot, session.ControllerBinding, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.services.engine == nil {
		return session.Snapshot{}, session.ControllerBinding{}, false, errors.New("app/services: runtime engine is required")
	}
	ref = defaultSessionRef(s.services.Runtime(), ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return session.Snapshot{}, session.ControllerBinding{}, false, nil
	}
	snapshot, err := s.services.engine.LoadSession(ctx, ref)
	if err != nil {
		return session.Snapshot{}, session.ControllerBinding{}, false, err
	}
	controller := controllerFromSnapshot(snapshot)
	if controller.Kind != session.ControllerACP {
		return snapshot, session.ControllerBinding{}, false, nil
	}
	return snapshot, controller, true, nil
}

func (s ControllerService) statusFromSnapshot(ctx context.Context, snapshot session.Snapshot, controller session.ControllerBinding) (ControllerStatus, error) {
	remoteOptions := controllerConfigOptionsFromState(snapshot.State)
	status := ControllerStatus{
		SessionRef:      snapshot.Session.Ref,
		Agent:           firstNonEmpty(controller.AgentName, controller.Label, controller.ID),
		RemoteSessionID: strings.TrimSpace(controller.RemoteSessionID),
		ModelOptions:    s.controllerModelOptions(ctx),
		ModeOptions:     controllerModeOptions(),
	}
	if controllerConfigRefMatches(snapshot.State, controller) {
		status.Model = strings.TrimSpace(stateString(snapshot.State, StateControllerModel))
		status.ReasoningEffort = strings.TrimSpace(stateString(snapshot.State, StateControllerReasoning))
		status.Mode = strings.TrimSpace(stateString(snapshot.State, StateControllerMode))
	}
	if len(remoteOptions) > 0 {
		status = applyRemoteControllerConfigOptions(status, remoteOptions)
	}
	status.EffortOptions = s.controllerEffortOptions(ctx, status.Model)
	if len(remoteOptions) > 0 {
		if efforts := controllerConfigChoices(remoteOptions, "reasoning"); len(efforts) > 0 {
			status.EffortOptions = efforts
		}
	}
	return status, nil
}

func (s ControllerService) controllerModelOptions(ctx context.Context) []ControllerConfigChoice {
	choices, err := s.services.Models().List(ctx)
	if err != nil || len(choices) == 0 {
		return nil
	}
	out := make([]ControllerConfigChoice, 0, len(choices))
	for _, choice := range choices {
		value := firstNonEmpty(choice.Alias, choice.ID, choice.Model)
		if value == "" {
			continue
		}
		out = append(out, ControllerConfigChoice{
			Value:       value,
			Name:        firstNonEmpty(choice.Alias, choice.Model, value),
			Description: strings.TrimSpace(choice.Detail),
		})
	}
	return out
}

func (s ControllerService) controllerEffortOptions(ctx context.Context, modelRef string) []ControllerConfigChoice {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return nil
	}
	cfg, err := s.services.Models().Resolve(ctx, modelRef)
	if err != nil {
		return nil
	}
	levels := s.services.Models().ReasoningLevels(cfg.Provider, cfg.Model)
	if len(levels) == 0 {
		levels = append([]string(nil), cfg.ReasoningLevels...)
	}
	out := make([]ControllerConfigChoice, 0, len(levels))
	for _, level := range levels {
		level = strings.TrimSpace(level)
		if level == "" {
			continue
		}
		out = append(out, ControllerConfigChoice{Value: level, Name: level})
	}
	return out
}

func matchControllerReasoningEffort(options []ControllerConfigChoice, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false
	}
	for _, option := range options {
		for _, candidate := range []string{option.Value, option.Name} {
			if strings.EqualFold(strings.TrimSpace(candidate), requested) {
				return firstNonEmpty(strings.TrimSpace(option.Value), strings.TrimSpace(option.Name), requested), true
			}
		}
	}
	return "", false
}

func controllerModeOptions() []ControllerMode {
	choices := sessionModeChoices()
	out := make([]ControllerMode, 0, len(choices))
	for _, choice := range choices {
		out = append(out, ControllerMode{
			ID:          strings.TrimSpace(choice.ID),
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func nextControllerMode(status ControllerStatus) (ControllerMode, error) {
	modes := compactControllerModes(status.ModeOptions)
	if len(modes) == 0 {
		return ControllerMode{}, errors.New("app/services: remote ACP controller did not declare session modes")
	}
	current := strings.TrimSpace(status.Mode)
	if current == "" {
		return modes[0], nil
	}
	for i, mode := range modes {
		if strings.EqualFold(strings.TrimSpace(mode.ID), current) || strings.EqualFold(strings.TrimSpace(mode.Name), current) {
			return modes[(i+1)%len(modes)], nil
		}
	}
	return modes[0], nil
}

func compactControllerModes(modes []ControllerMode) []ControllerMode {
	if len(modes) == 0 {
		return nil
	}
	out := make([]ControllerMode, 0, len(modes))
	seen := map[string]struct{}{}
	for _, mode := range modes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func applyRemoteControllerConfigOptions(status ControllerStatus, options []control.ConfigOption) ControllerStatus {
	if models := controllerConfigChoices(options, "model"); len(models) > 0 {
		status.ModelOptions = models
		if status.Model == "" {
			status.Model, _ = currentControllerConfigValue(options, "model")
		}
	}
	if status.ReasoningEffort == "" {
		status.ReasoningEffort, _ = currentControllerConfigValue(options, "reasoning")
	}
	if modes := controllerConfigModes(options); len(modes) > 0 {
		status.ModeOptions = modes
		if status.Mode == "" {
			status.Mode, _ = currentControllerConfigValue(options, "mode")
		}
	}
	return status
}

func controllerConfigOptionsFromState(state session.State) []control.ConfigOption {
	if state == nil {
		return nil
	}
	return parseControllerConfigOptions(state[StateControllerConfigOptions])
}

func parseControllerConfigOptions(raw any) []control.ConfigOption {
	switch typed := raw.(type) {
	case nil:
		return nil
	case []control.ConfigOption:
		return cloneControllerConfigOptions(typed)
	case control.ConfigOption:
		return []control.ConfigOption{cloneControllerConfigOption(typed)}
	case json.RawMessage:
		var out []control.ConfigOption
		if err := json.Unmarshal(typed, &out); err == nil {
			return cloneControllerConfigOptions(out)
		}
	case []byte:
		var out []control.ConfigOption
		if err := json.Unmarshal(typed, &out); err == nil {
			return cloneControllerConfigOptions(out)
		}
	default:
		rawJSON, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		var out []control.ConfigOption
		if err := json.Unmarshal(rawJSON, &out); err == nil {
			return cloneControllerConfigOptions(out)
		}
	}
	return nil
}

func cloneControllerConfigOptions(in []control.ConfigOption) []control.ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]control.ConfigOption, 0, len(in))
	for _, option := range in {
		out = append(out, cloneControllerConfigOption(option))
	}
	return out
}

func cloneControllerConfigOption(option control.ConfigOption) control.ConfigOption {
	option.ID = strings.TrimSpace(option.ID)
	option.Name = strings.TrimSpace(option.Name)
	option.Description = strings.TrimSpace(option.Description)
	option.Category = strings.TrimSpace(option.Category)
	option.CurrentValue = strings.TrimSpace(option.CurrentValue)
	option.Options = append([]control.ConfigChoice(nil), option.Options...)
	return option
}

func currentControllerConfigValue(options []control.ConfigOption, kind string) (string, bool) {
	for _, option := range options {
		if control.ConfigOptionKind(option) == kind {
			value := strings.TrimSpace(option.CurrentValue)
			return value, value != ""
		}
	}
	return "", false
}

func controllerConfigChoices(options []control.ConfigOption, kind string) []ControllerConfigChoice {
	option, ok := findControllerConfigOption(options, kind)
	if !ok || len(option.Options) == 0 {
		return nil
	}
	out := make([]ControllerConfigChoice, 0, len(option.Options))
	for _, choice := range option.Options {
		value := strings.TrimSpace(choice.Value)
		name := strings.TrimSpace(choice.Name)
		if value == "" && name == "" {
			continue
		}
		out = append(out, ControllerConfigChoice{
			Value:       firstNonEmpty(value, name),
			Name:        firstNonEmpty(name, value),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func controllerConfigModes(options []control.ConfigOption) []ControllerMode {
	option, ok := findControllerConfigOption(options, "mode")
	if !ok || len(option.Options) == 0 {
		return nil
	}
	out := make([]ControllerMode, 0, len(option.Options))
	for _, choice := range option.Options {
		id := strings.TrimSpace(choice.Value)
		name := strings.TrimSpace(choice.Name)
		if id == "" && name == "" {
			continue
		}
		out = append(out, ControllerMode{
			ID:          firstNonEmpty(id, name),
			Name:        firstNonEmpty(name, id),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func findControllerConfigOption(options []control.ConfigOption, kind string) (control.ConfigOption, bool) {
	for _, option := range options {
		if control.ConfigOptionKind(option) == kind {
			return cloneControllerConfigOption(option), true
		}
	}
	return control.ConfigOption{}, false
}

func controllerFromSnapshot(snapshot session.Snapshot) session.ControllerBinding {
	controller := normalizeControllerBinding(snapshot.Session.Controller)
	for _, event := range snapshot.Events {
		if event.Scope == nil {
			continue
		}
		next := normalizeControllerBinding(event.Scope.Controller)
		if event.Type == session.EventHandoff {
			if next.Kind != "" || strings.TrimSpace(next.ID) != "" {
				controller = next
			}
			continue
		}
		if sameControllerBinding(controller, next) {
			controller = mergeControllerBinding(controller, next)
		}
	}
	return controller
}

func sameControllerBinding(active session.ControllerBinding, next session.ControllerBinding) bool {
	active = normalizeControllerBinding(active)
	next = normalizeControllerBinding(next)
	if active.Kind == "" || next.Kind == "" || active.Kind != next.Kind {
		return false
	}
	if active.Kind != session.ControllerACP {
		return false
	}
	if active.EpochID != "" && next.EpochID != "" && active.EpochID != next.EpochID {
		return false
	}
	activeID := strings.ToLower(firstNonEmpty(active.ID, active.AgentName, active.Label))
	nextID := strings.ToLower(firstNonEmpty(next.ID, next.AgentName, next.Label))
	return activeID != "" && activeID == nextID
}

func mergeControllerBinding(existing session.ControllerBinding, next session.ControllerBinding) session.ControllerBinding {
	existing = normalizeControllerBinding(existing)
	next = normalizeControllerBinding(next)
	existing.Kind = firstControllerKind(existing.Kind, next.Kind)
	existing.ID = firstNonEmpty(existing.ID, next.ID)
	existing.AgentName = firstNonEmpty(existing.AgentName, next.AgentName)
	existing.Label = firstNonEmpty(existing.Label, next.Label)
	existing.EpochID = firstNonEmpty(existing.EpochID, next.EpochID)
	existing.RemoteSessionID = firstNonEmpty(next.RemoteSessionID, existing.RemoteSessionID)
	if next.ContextSyncSeq != 0 {
		existing.ContextSyncSeq = next.ContextSyncSeq
	}
	if !next.AttachedAt.IsZero() {
		existing.AttachedAt = next.AttachedAt
	}
	existing.Source = firstNonEmpty(next.Source, existing.Source)
	return existing
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

func firstControllerKind(values ...session.ControllerKind) session.ControllerKind {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func controllerConfigRef(controller session.ControllerBinding) string {
	return firstNonEmpty(controller.EpochID, controller.ID, controller.AgentName, controller.Label)
}

func controllerConfigRefMatches(state session.State, controller session.ControllerBinding) bool {
	ref := strings.TrimSpace(stateString(state, StateControllerConfigRef))
	return ref != "" && ref == controllerConfigRef(controller)
}

func controllerHandoffEvent(controller session.ControllerBinding, source string, reason string) session.Event {
	return session.Event{
		Type:       session.EventHandoff,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "caelis", Name: "caelis"},
		Scope: &session.EventScope{
			Source:     firstNonEmpty(source, "app_command_handoff"),
			Controller: controller,
		},
		Meta: map[string]any{
			"reason": strings.TrimSpace(reason),
		},
	}
}

func controllerEpochID() string {
	return fmt.Sprintf("controller-%d", time.Now().UTC().UnixNano())
}
