package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
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
	status.EffortOptions = s.controllerEffortOptions(ctx, status.Model)
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
