package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
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

type ControllerConfigOption struct {
	ID           string                   `json:"id,omitempty"`
	Name         string                   `json:"name,omitempty"`
	Type         string                   `json:"type,omitempty"`
	Category     string                   `json:"category,omitempty"`
	Description  string                   `json:"description,omitempty"`
	CurrentValue string                   `json:"current_value,omitempty"`
	Options      []ControllerConfigChoice `json:"options,omitempty"`
}

type ControllerLifecycle struct {
	RunID           string    `json:"run_id,omitempty"`
	Phase           string    `json:"phase,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	Running         bool      `json:"running,omitempty"`
	Active          bool      `json:"active,omitempty"`
	Recovering      bool      `json:"recovering,omitempty"`
	RemoteSessionID string    `json:"remote_session_id,omitempty"`
	Error           string    `json:"error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type ControllerDiagnostic struct {
	Severity string            `json:"severity,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Message  string            `json:"message,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
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
	ConfigOptions   []ControllerConfigOption `json:"config_options,omitempty"`
	Lifecycle       *ControllerLifecycle     `json:"lifecycle,omitempty"`
	Diagnostics     []ControllerDiagnostic   `json:"diagnostics,omitempty"`
}

type ControllerRunQuery struct {
	SessionRef session.Ref               `json:"session_ref,omitempty"`
	Controller session.ControllerBinding `json:"controller,omitempty"`
}

type ControllerRunStatus struct {
	ID              string                            `json:"id,omitempty"`
	Phase           control.ControllerInvocationPhase `json:"phase,omitempty"`
	SessionRef      session.Ref                       `json:"session_ref,omitempty"`
	TurnID          string                            `json:"turn_id,omitempty"`
	Controller      session.ControllerBinding         `json:"controller,omitempty"`
	RemoteSessionID string                            `json:"remote_session_id,omitempty"`
	Running         bool                              `json:"running,omitempty"`
	Active          bool                              `json:"active,omitempty"`
	Recovering      bool                              `json:"recovering,omitempty"`
	Error           string                            `json:"error,omitempty"`
	StartedAt       time.Time                         `json:"started_at,omitempty"`
	UpdatedAt       time.Time                         `json:"updated_at,omitempty"`
}

type ControllerRunSource interface {
	ControllerRuns(context.Context, ControllerRunQuery) ([]ControllerRunStatus, error)
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
		status.ConfigOptions = controllerConfigOptions(remoteOptions)
		status = applyRemoteControllerConfigOptions(status, remoteOptions)
	}
	status.EffortOptions = s.controllerEffortOptions(ctx, status.Model)
	if len(remoteOptions) > 0 {
		if efforts := controllerConfigChoices(remoteOptions, "reasoning"); len(efforts) > 0 {
			status.EffortOptions = efforts
		}
	}
	runs := controllerLifecycleRunsFromEvents(snapshot.Events, controller)
	if s.services.controllerRuns != nil {
		sourceRuns, err := s.services.controllerRuns.ControllerRuns(ctx, ControllerRunQuery{
			SessionRef: snapshot.Session.Ref,
			Controller: controller,
		})
		if err != nil {
			return ControllerStatus{}, err
		}
		runs = append(runs, sourceRuns...)
	}
	if len(runs) > 0 {
		status = applyControllerRuns(status, runs)
	}
	return status, nil
}

func controllerLifecycleRunsFromEvents(events []session.Event, controller session.ControllerBinding) []ControllerRunStatus {
	return controllerRunStatusesFromEventsForController(events, controller, true)
}

func controllerRunStatusesFromEvents(events []session.Event) []ControllerRunStatus {
	return controllerRunStatusesFromEventsForController(events, session.ControllerBinding{}, false)
}

func controllerRunStatusesFromEventsForController(events []session.Event, controller session.ControllerBinding, filter bool) []ControllerRunStatus {
	if len(events) == 0 {
		return nil
	}
	byID := map[string]ControllerRunStatus{}
	for _, event := range events {
		for _, run := range controllerRunStatusesFromEvent(event, controller, filter) {
			key := controllerRunHistoryKey(run)
			if key == "" {
				continue
			}
			existing, exists := byID[key]
			if !exists {
				byID[key] = run
				continue
			}
			byID[key] = mergeControllerRunHistory(existing, run)
		}
	}
	if len(byID) == 0 {
		return nil
	}
	out := make([]ControllerRunStatus, 0, len(byID))
	for _, run := range byID {
		out = append(out, run)
	}
	sortControllerRuns(out)
	return out
}

func controllerRunStatusFromLifecycleEvent(event session.Event, controller session.ControllerBinding) (ControllerRunStatus, bool) {
	if event.Type != session.EventLifecycle || event.Lifecycle == nil {
		return ControllerRunStatus{}, false
	}
	meta := session.RuntimeControllerMeta(event.Meta)
	if len(meta) == 0 {
		return ControllerRunStatus{}, false
	}
	return controllerRunStatusFromRuntimeControllerMeta(meta, event, controller, true)
}

func controllerRunStatusesFromEvent(event session.Event, controller session.ControllerBinding, filter bool) []ControllerRunStatus {
	if runs, ok := controllerRunStatusFromCompactEvent(event, controller, filter); ok {
		return runs
	}
	if event.Type != session.EventLifecycle || event.Lifecycle == nil {
		return nil
	}
	meta := session.RuntimeControllerMeta(event.Meta)
	if len(meta) == 0 {
		return nil
	}
	run, ok := controllerRunStatusFromRuntimeControllerMeta(meta, event, controller, filter)
	if !ok {
		return nil
	}
	return []ControllerRunStatus{run}
}

func controllerRunStatusFromCompactEvent(event session.Event, controller session.ControllerBinding, filter bool) ([]ControllerRunStatus, bool) {
	if !isCompactCheckpoint(event) {
		return nil, false
	}
	compact, ok := mapAny(event.Meta[compactMetaKey])
	if !ok {
		return nil, false
	}
	entries := compactControllerIndexEntries(compact[compactControllerIndexKey])
	if len(entries) == 0 {
		return nil, false
	}
	out := make([]ControllerRunStatus, 0, len(entries))
	for _, entry := range entries {
		run, ok := controllerRunStatusFromRuntimeControllerMeta(entry, event, controller, filter)
		if ok {
			out = append(out, run)
		}
	}
	return out, true
}

func compactControllerIndexEntries(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := mapAny(item); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func controllerRunStatusFromRuntimeControllerMeta(meta map[string]any, event session.Event, controller session.ControllerBinding, filter bool) (ControllerRunStatus, bool) {
	eventController := controllerBindingFromLifecycleMeta(meta)
	if event.Scope != nil {
		eventController = mergeControllerBinding(eventController, event.Scope.Controller)
	}
	if filter {
		if !controllerLifecycleMatches(controller, eventController) {
			return ControllerRunStatus{}, false
		}
	} else if eventController.Kind != session.ControllerACP || firstNonEmpty(eventController.ID, eventController.AgentName, eventController.Label) == "" {
		return ControllerRunStatus{}, false
	}
	runID := firstNonEmpty(
		stringFromAny(meta["run_id"]),
		eventTurnID(event),
		strings.TrimSpace(event.ID),
	)
	if runID == "" {
		return ControllerRunStatus{}, false
	}
	lifecycleStatus := session.LifecycleStatus(stringFromAny(meta["status"]))
	if event.Lifecycle != nil && lifecycleStatus == "" {
		lifecycleStatus = event.Lifecycle.Status
	}
	phase := control.ControllerInvocationPhase(firstNonEmpty(
		stringFromAny(meta["phase"]),
		controllerPhaseFromLifecycleStatus(lifecycleStatus),
	))
	running, ok := firstBool(meta["running"])
	if !ok {
		running = lifecycleStatus == session.LifecycleRunning ||
			phase == control.ControllerInvocationStarted ||
			phase == control.ControllerInvocationRemoteSession
	}
	active, ok := firstBool(meta["active"])
	if !ok {
		active = running
	}
	updatedAt := firstTime(meta["updated_at"])
	if updatedAt.IsZero() {
		updatedAt = event.Time
	}
	startedAt := firstTime(meta["started_at"])
	if startedAt.IsZero() {
		startedAt = updatedAt
	}
	return normalizeControllerRunStatus(ControllerRunStatus{
		ID:              runID,
		Phase:           phase,
		SessionRef:      session.Ref{SessionID: firstNonEmpty(stringFromAny(meta["session_id"]), strings.TrimSpace(event.SessionID))},
		TurnID:          firstNonEmpty(stringFromAny(meta["turn_id"]), eventTurnID(event)),
		Controller:      eventController,
		RemoteSessionID: firstNonEmpty(stringFromAny(meta["remote_session_id"]), eventController.RemoteSessionID, eventACPRemoteSessionID(event)),
		Running:         running,
		Active:          active,
		Recovering:      boolFromAny(meta["recovering"]),
		Error:           stringFromAny(meta["error"]),
		StartedAt:       startedAt,
		UpdatedAt:       updatedAt,
	}), true
}

func controllerRunRetentionMeta(run ControllerRunStatus) map[string]any {
	run = normalizeControllerRunStatus(run)
	if strings.TrimSpace(run.ID) == "" {
		return nil
	}
	meta := map[string]any{
		"schema":          session.RuntimeControllerMetaName,
		"schema_version":  session.RuntimeControllerMetaVersion,
		"source":          "compact",
		"run_id":          strings.TrimSpace(run.ID),
		"phase":           strings.TrimSpace(string(run.Phase)),
		"running":         run.Running,
		"active":          run.Active,
		"recovering":      run.Recovering,
		"controller_kind": strings.TrimSpace(string(run.Controller.Kind)),
		"controller_id":   strings.TrimSpace(run.Controller.ID),
		"agent":           firstNonEmpty(run.Controller.AgentName, run.Controller.Label, run.Controller.ID),
		"label":           strings.TrimSpace(run.Controller.Label),
		"epoch_id":        strings.TrimSpace(run.Controller.EpochID),
	}
	for key, value := range map[string]string{
		"session_id":        run.SessionRef.SessionID,
		"turn_id":           run.TurnID,
		"remote_session_id": firstNonEmpty(run.RemoteSessionID, run.Controller.RemoteSessionID),
		"error":             run.Error,
	} {
		if text := strings.TrimSpace(value); text != "" {
			meta[key] = text
		}
	}
	if !run.StartedAt.IsZero() {
		meta["started_at"] = run.StartedAt.Format(time.RFC3339Nano)
	}
	if !run.UpdatedAt.IsZero() {
		meta["updated_at"] = run.UpdatedAt.Format(time.RFC3339Nano)
	}
	return meta
}

func controllerRunHistoryKey(run ControllerRunStatus) string {
	run = normalizeControllerRunStatus(run)
	parts := []string{
		strings.TrimSpace(string(run.Controller.Kind)),
		strings.TrimSpace(run.Controller.EpochID),
		strings.TrimSpace(firstNonEmpty(run.Controller.ID, run.Controller.AgentName, run.Controller.Label)),
		strings.TrimSpace(run.ID),
	}
	return strings.Join(commandNonEmpty(parts), "\x00")
}

func sortControllerRuns(runs []ControllerRunStatus) {
	sort.SliceStable(runs, func(i, j int) bool {
		left := normalizeControllerRunStatus(runs[i])
		right := normalizeControllerRunStatus(runs[j])
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			if left.UpdatedAt.IsZero() {
				return false
			}
			if right.UpdatedAt.IsZero() {
				return true
			}
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return controllerRunHistoryKey(left) < controllerRunHistoryKey(right)
	})
}

func mergeControllerRunHistory(existing ControllerRunStatus, next ControllerRunStatus) ControllerRunStatus {
	existing = normalizeControllerRunStatus(existing)
	next = normalizeControllerRunStatus(next)
	startedAt := existing.StartedAt
	if startedAt.IsZero() || (!next.StartedAt.IsZero() && next.StartedAt.Before(startedAt)) {
		startedAt = next.StartedAt
	}
	latest := existing
	if existing.UpdatedAt.IsZero() || (!next.UpdatedAt.IsZero() && !next.UpdatedAt.Before(existing.UpdatedAt)) {
		latest = next
	}
	latest.StartedAt = startedAt
	if latest.RemoteSessionID == "" {
		latest.RemoteSessionID = firstNonEmpty(next.RemoteSessionID, existing.RemoteSessionID)
	}
	if latest.Controller.RemoteSessionID == "" {
		latest.Controller.RemoteSessionID = latest.RemoteSessionID
	}
	return normalizeControllerRunStatus(latest)
}

func controllerBindingFromLifecycleMeta(meta map[string]any) session.ControllerBinding {
	controller := session.ControllerBinding{
		Kind:            session.ControllerKind(stringFromAny(meta["controller_kind"])),
		ID:              stringFromAny(meta["controller_id"]),
		AgentName:       stringFromAny(meta["agent"]),
		Label:           stringFromAny(meta["label"]),
		EpochID:         stringFromAny(meta["epoch_id"]),
		RemoteSessionID: stringFromAny(meta["remote_session_id"]),
	}
	return normalizeControllerBinding(controller)
}

func controllerLifecycleMatches(active session.ControllerBinding, eventController session.ControllerBinding) bool {
	active = normalizeControllerBinding(active)
	eventController = normalizeControllerBinding(eventController)
	if active.EpochID != "" {
		return eventController.EpochID == active.EpochID
	}
	return sameControllerBinding(active, eventController)
}

func controllerPhaseFromLifecycleStatus(status session.LifecycleStatus) string {
	switch status {
	case session.LifecycleFailed:
		return string(control.ControllerInvocationFailed)
	case session.LifecycleCompleted:
		return string(control.ControllerInvocationCompleted)
	default:
		return string(control.ControllerInvocationStarted)
	}
}

func eventACPRemoteSessionID(event session.Event) string {
	if event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.ACP.SessionID)
}

func applyControllerRuns(status ControllerStatus, runs []ControllerRunStatus) ControllerStatus {
	if len(runs) == 0 {
		return status
	}
	var latest *ControllerRunStatus
	for idx := range runs {
		run := normalizeControllerRunStatus(runs[idx])
		if strings.TrimSpace(run.ID) == "" {
			continue
		}
		if latest == nil || controllerRunAfter(run, *latest) {
			latest = &run
		}
	}
	if latest == nil {
		return status
	}
	status.Lifecycle = &ControllerLifecycle{
		RunID:           strings.TrimSpace(latest.ID),
		Phase:           strings.TrimSpace(string(latest.Phase)),
		TurnID:          strings.TrimSpace(latest.TurnID),
		Running:         latest.Running,
		Active:          latest.Active,
		Recovering:      latest.Recovering,
		RemoteSessionID: firstNonEmpty(strings.TrimSpace(latest.RemoteSessionID), status.RemoteSessionID),
		Error:           strings.TrimSpace(latest.Error),
		StartedAt:       latest.StartedAt,
		UpdatedAt:       latest.UpdatedAt,
	}
	status.Diagnostics = append(status.Diagnostics, controllerRunDiagnostics(*latest)...)
	if status.RemoteSessionID == "" {
		status.RemoteSessionID = status.Lifecycle.RemoteSessionID
	}
	return status
}

func normalizeControllerRunStatus(in ControllerRunStatus) ControllerRunStatus {
	in.ID = strings.TrimSpace(in.ID)
	in.Phase = control.ControllerInvocationPhase(strings.TrimSpace(string(in.Phase)))
	in.SessionRef = session.NormalizeRef(in.SessionRef)
	in.TurnID = strings.TrimSpace(in.TurnID)
	in.Controller = normalizeControllerBinding(in.Controller)
	in.RemoteSessionID = strings.TrimSpace(in.RemoteSessionID)
	in.Error = strings.TrimSpace(in.Error)
	if in.Phase == "" {
		if in.Running {
			in.Phase = control.ControllerInvocationStarted
		} else if in.Error != "" {
			in.Phase = control.ControllerInvocationFailed
		} else {
			in.Phase = control.ControllerInvocationCompleted
		}
	}
	return in
}

func controllerRunAfter(left ControllerRunStatus, right ControllerRunStatus) bool {
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		if left.UpdatedAt.IsZero() {
			return false
		}
		if right.UpdatedAt.IsZero() {
			return true
		}
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	if left.Running != right.Running {
		return left.Running
	}
	return strings.TrimSpace(left.ID) > strings.TrimSpace(right.ID)
}

func controllerRunDiagnostics(run ControllerRunStatus) []ControllerDiagnostic {
	var out []ControllerDiagnostic
	if run.Error != "" || run.Phase == control.ControllerInvocationFailed {
		out = append(out, ControllerDiagnostic{
			Severity: "error",
			Kind:     "controller_lifecycle",
			Message:  firstNonEmpty(run.Error, "remote ACP controller invocation failed"),
			Meta:     controllerRunDiagnosticMeta(run),
		})
		return out
	}
	if run.Running {
		severity := "info"
		message := "remote ACP controller invocation is running"
		if run.Recovering {
			severity = "warning"
			message = "remote ACP controller invocation is being recovered"
		} else if !run.Active {
			severity = "warning"
			message = "remote ACP controller invocation is pending recovery"
		}
		out = append(out, ControllerDiagnostic{
			Severity: severity,
			Kind:     "controller_lifecycle",
			Message:  message,
			Meta:     controllerRunDiagnosticMeta(run),
		})
	}
	return out
}

func controllerRunDiagnosticMeta(run ControllerRunStatus) map[string]string {
	meta := map[string]string{}
	if id := strings.TrimSpace(run.ID); id != "" {
		meta["run_id"] = id
	}
	if phase := strings.TrimSpace(string(run.Phase)); phase != "" {
		meta["phase"] = phase
	}
	if turnID := strings.TrimSpace(run.TurnID); turnID != "" {
		meta["turn_id"] = turnID
	}
	if remote := strings.TrimSpace(run.RemoteSessionID); remote != "" {
		meta["remote_session_id"] = remote
	}
	if agent := firstNonEmpty(run.Controller.AgentName, run.Controller.Label, run.Controller.ID); agent != "" {
		meta["agent"] = agent
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func controllerStatusView(status ControllerStatus) *appviewmodel.ControllerStatus {
	out := appviewmodel.ControllerStatus{
		SessionRef:      session.NormalizeRef(status.SessionRef),
		Agent:           strings.TrimSpace(status.Agent),
		RemoteSessionID: strings.TrimSpace(status.RemoteSessionID),
		Model:           strings.TrimSpace(status.Model),
		ModelOptions:    controllerStatusViewChoices(status.ModelOptions),
		ReasoningEffort: strings.TrimSpace(status.ReasoningEffort),
		EffortOptions:   controllerStatusViewChoices(status.EffortOptions),
		Mode:            strings.TrimSpace(status.Mode),
		ModeOptions:     controllerStatusViewModes(status.ModeOptions),
		ConfigOptions:   controllerStatusViewConfigOptions(status.ConfigOptions),
		Diagnostics:     controllerStatusViewDiagnostics(status.Diagnostics),
	}
	if status.Lifecycle != nil {
		lifecycle := *status.Lifecycle
		out.Lifecycle = &appviewmodel.ControllerLifecycle{
			RunID:           strings.TrimSpace(lifecycle.RunID),
			Phase:           strings.TrimSpace(lifecycle.Phase),
			TurnID:          strings.TrimSpace(lifecycle.TurnID),
			Running:         lifecycle.Running,
			Active:          lifecycle.Active,
			Recovering:      lifecycle.Recovering,
			RemoteSessionID: strings.TrimSpace(lifecycle.RemoteSessionID),
			Error:           strings.TrimSpace(lifecycle.Error),
			StartedAt:       lifecycle.StartedAt,
			UpdatedAt:       lifecycle.UpdatedAt,
		}
	}
	return &out
}

func controllerStatusViewChoices(in []ControllerConfigChoice) []appviewmodel.ControllerConfigChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigChoice, 0, len(in))
	for _, choice := range in {
		out = append(out, appviewmodel.ControllerConfigChoice{
			Value:       strings.TrimSpace(choice.Value),
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func controllerStatusViewModes(in []ControllerMode) []appviewmodel.ControllerMode {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerMode, 0, len(in))
	for _, mode := range in {
		out = append(out, appviewmodel.ControllerMode{
			ID:          strings.TrimSpace(mode.ID),
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func controllerStatusViewConfigOptions(in []ControllerConfigOption) []appviewmodel.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigOption, 0, len(in))
	for _, option := range in {
		out = append(out, appviewmodel.ControllerConfigOption{
			ID:           strings.TrimSpace(option.ID),
			Name:         strings.TrimSpace(option.Name),
			Type:         strings.TrimSpace(option.Type),
			Category:     strings.TrimSpace(option.Category),
			Description:  strings.TrimSpace(option.Description),
			CurrentValue: strings.TrimSpace(option.CurrentValue),
			Options:      controllerStatusViewChoices(option.Options),
		})
	}
	return out
}

func controllerStatusViewDiagnostics(in []ControllerDiagnostic) []appviewmodel.ControllerDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerDiagnostic, 0, len(in))
	for _, diagnostic := range in {
		out = append(out, appviewmodel.ControllerDiagnostic{
			Severity: strings.TrimSpace(diagnostic.Severity),
			Kind:     strings.TrimSpace(diagnostic.Kind),
			Message:  strings.TrimSpace(diagnostic.Message),
			Meta:     maps.Clone(diagnostic.Meta),
		})
	}
	return out
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
	option.Type = strings.TrimSpace(option.Type)
	option.ID = strings.TrimSpace(option.ID)
	option.Name = strings.TrimSpace(option.Name)
	option.Description = strings.TrimSpace(option.Description)
	option.Category = strings.TrimSpace(option.Category)
	option.CurrentValue = strings.TrimSpace(option.CurrentValue)
	option.Options = append([]control.ConfigChoice(nil), option.Options...)
	return option
}

func controllerConfigOptions(options []control.ConfigOption) []ControllerConfigOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]ControllerConfigOption, 0, len(options))
	for _, option := range options {
		option = cloneControllerConfigOption(option)
		if option.ID == "" {
			continue
		}
		out = append(out, ControllerConfigOption{
			ID:           option.ID,
			Name:         option.Name,
			Type:         option.Type,
			Category:     option.Category,
			Description:  option.Description,
			CurrentValue: option.CurrentValue,
			Options:      controllerConfigChoicesFromControl(option.Options),
		})
	}
	return out
}

func controllerConfigChoicesFromControl(choices []control.ConfigChoice) []ControllerConfigChoice {
	if len(choices) == 0 {
		return nil
	}
	out := make([]ControllerConfigChoice, 0, len(choices))
	for _, choice := range choices {
		value := strings.TrimSpace(choice.Value)
		name := strings.TrimSpace(choice.Name)
		description := strings.TrimSpace(choice.Description)
		if value == "" && name == "" {
			continue
		}
		out = append(out, ControllerConfigChoice{
			Value:       value,
			Name:        name,
			Description: description,
		})
	}
	return out
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
