package local

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

type controllerRunManager struct {
	store   session.Store
	configs []acpexternal.Config
	journal *controllerRunJournal
	now     func() time.Time

	mu     sync.Mutex
	active map[string]controllerRunActivity
}

type controllerRunTracker struct {
	manager *controllerRunManager
	id      string
}

type controllerRunActivity struct {
	recovering bool
}

func newControllerRunManager(store session.Store, configs []acpexternal.Config, stateDir string) *controllerRunManager {
	if store == nil || len(configs) == 0 || strings.TrimSpace(stateDir) == "" {
		return nil
	}
	out := make([]acpexternal.Config, 0, len(configs))
	seen := map[string]struct{}{}
	for _, cfg := range configs {
		id := strings.ToLower(firstNonEmpty(cfg.AgentID, cfg.AgentName, cfg.Command))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, cfg)
	}
	if len(out) == 0 {
		return nil
	}
	return &controllerRunManager{
		store:   store,
		configs: out,
		journal: newControllerRunJournal(stateDir),
		now:     func() time.Time { return time.Now().UTC() },
		active:  map[string]controllerRunActivity{},
	}
}

func (m *controllerRunManager) tracker() control.ControllerInvocationTracker {
	if m == nil || m.journal == nil {
		return nil
	}
	return &controllerRunTracker{manager: m}
}

func (m *controllerRunManager) trackerForRecord(id string) control.ControllerInvocationTracker {
	if m == nil || m.journal == nil {
		return nil
	}
	return &controllerRunTracker{manager: m, id: strings.TrimSpace(id)}
}

func (m *controllerRunManager) StartRecovery(ctx context.Context) error {
	if m == nil || m.journal == nil {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	records, err := m.journal.readRunning(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		record := normalizeControllerRunJournalRecord(record)
		cfg, ok := m.configForController(record.Controller)
		if !ok || strings.TrimSpace(record.Input) == "" && len(record.ContentParts) == 0 {
			continue
		}
		if !m.markActive(record.ID, true) {
			continue
		}
		go m.recover(context.Background(), record, cfg)
	}
	return nil
}

func (m *controllerRunManager) recover(ctx context.Context, record controllerRunJournalRecord, cfg acpexternal.Config) {
	defer m.clearActive(record.ID)
	client, err := acpexternal.StartProcess(ctx, cfg)
	if err != nil {
		record = normalizeControllerRunJournalRecord(record)
		record.Running = false
		record.Phase = control.ControllerInvocationFailed
		record.Error = strings.TrimSpace(err.Error())
		record.UpdatedAt = m.now()
		_ = m.journal.write(context.Background(), record)
		return
	}
	defer client.Close()
	controller := record.Controller
	controller.RemoteSessionID = firstNonEmpty(record.RemoteSessionID, controller.RemoteSessionID)
	runner := control.ControllerRunner{
		Store:   m.store,
		Tracker: m.trackerForRecord(record.ID),
	}
	_, _ = runner.Invoke(ctx, control.ControllerRequest{
		SessionRef:                record.SessionRef,
		Workspace:                 record.Workspace,
		TurnID:                    record.TurnID,
		Controller:                controller,
		ControllerModel:           record.ControllerModel,
		ControllerReasoningEffort: record.ControllerReasoningEffort,
		ControllerMode:            record.ControllerMode,
		Input:                     record.Input,
		ContentParts:              model.CloneContentParts(record.ContentParts),
		Agent:                     externalAgentSession{client: client},
	})
}

func (m *controllerRunManager) configForController(controller session.ControllerBinding) (acpexternal.Config, bool) {
	if m == nil {
		return acpexternal.Config{}, false
	}
	candidates := []string{controller.AgentName, controller.ID, controller.Label}
	for _, cfg := range m.configs {
		cfgCandidates := []string{cfg.AgentID, cfg.AgentName, cfg.Command}
		for _, requested := range candidates {
			requested = strings.TrimSpace(requested)
			if requested == "" {
				continue
			}
			for _, candidate := range cfgCandidates {
				if strings.EqualFold(strings.TrimSpace(candidate), requested) {
					return cfg, strings.TrimSpace(cfg.Command) != ""
				}
			}
		}
	}
	return acpexternal.Config{}, false
}

func (m *controllerRunManager) markActive(id string, recovering bool) bool {
	if m == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.active[id]; ok {
		if recovering && !existing.recovering {
			m.active[id] = controllerRunActivity{recovering: true}
		}
		return false
	}
	m.active[id] = controllerRunActivity{recovering: recovering}
	return true
}

func (m *controllerRunManager) clearActive(id string) {
	if m == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

func (m *controllerRunManager) activeState(id string) (bool, bool) {
	if m == nil {
		return false, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.active[id]
	return ok, ok && state.recovering
}

func (m *controllerRunManager) ControllerRuns(ctx context.Context, query services.ControllerRunQuery) ([]services.ControllerRunStatus, error) {
	if m == nil || m.journal == nil {
		return nil, nil
	}
	records, err := m.journal.readAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]services.ControllerRunStatus, 0, len(records))
	for _, record := range records {
		record = normalizeControllerRunJournalRecord(record)
		if !controllerRunRecordMatches(record, query) {
			continue
		}
		active, recovering := m.activeState(record.ID)
		out = append(out, services.ControllerRunStatus{
			ID:              strings.TrimSpace(record.ID),
			Phase:           record.Phase,
			SessionRef:      record.SessionRef,
			TurnID:          strings.TrimSpace(record.TurnID),
			Controller:      record.Controller,
			RemoteSessionID: firstNonEmpty(record.RemoteSessionID, record.Controller.RemoteSessionID),
			Running:         record.Running,
			Active:          active,
			Recovering:      recovering,
			Error:           strings.TrimSpace(record.Error),
			StartedAt:       record.StartedAt,
			UpdatedAt:       record.UpdatedAt,
		})
	}
	return out, nil
}

func (t *controllerRunTracker) ControllerInvocationChanged(ctx context.Context, state control.ControllerInvocationState) error {
	if t == nil || t.manager == nil || t.manager.journal == nil {
		return nil
	}
	if t.id == "" {
		t.id = controllerRunStateID(state)
	}
	switch state.Phase {
	case control.ControllerInvocationStarted, control.ControllerInvocationRemoteSession:
		t.manager.markActive(t.id, false)
		record := controllerRunRecordFromState(t.id, state)
		record.Running = true
		return t.manager.journal.write(ctx, record)
	case control.ControllerInvocationCompleted, control.ControllerInvocationFailed:
		defer t.manager.clearActive(t.id)
		if state.Phase == control.ControllerInvocationFailed {
			record := controllerRunRecordFromState(t.id, state)
			record.Running = false
			return t.manager.journal.write(ctx, record)
		}
		return t.manager.journal.delete(ctx, t.id)
	default:
		return nil
	}
}

func controllerRunRecordMatches(record controllerRunJournalRecord, query services.ControllerRunQuery) bool {
	query.SessionRef = session.NormalizeRef(query.SessionRef)
	if query.SessionRef.SessionID != "" && query.SessionRef.SessionID != record.SessionRef.SessionID {
		return false
	}
	query.Controller = normalizeControllerBinding(query.Controller)
	if query.Controller.Kind != "" && record.Controller.Kind != "" && query.Controller.Kind != record.Controller.Kind {
		return false
	}
	queryID := strings.ToLower(firstNonEmpty(query.Controller.EpochID, query.Controller.ID, query.Controller.AgentName, query.Controller.Label))
	if queryID == "" {
		return true
	}
	for _, value := range []string{record.Controller.EpochID, record.Controller.ID, record.Controller.AgentName, record.Controller.Label} {
		if strings.EqualFold(strings.TrimSpace(value), queryID) {
			return true
		}
	}
	return false
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

func controllerRunStateID(state control.ControllerInvocationState) string {
	return firstNonEmpty(
		state.TurnID,
		controllerRunCompositeID(state.SessionRef.SessionID, state.Controller.EpochID),
		controllerRunCompositeID(state.SessionRef.SessionID, firstNonEmpty(state.Controller.ID, state.Controller.AgentName, state.Controller.Label)),
		state.SessionRef.SessionID,
	)
}

func controllerRunRecordFromState(id string, state control.ControllerInvocationState) controllerRunJournalRecord {
	record := controllerRunJournalRecord{
		ID:                        strings.TrimSpace(id),
		SessionRef:                state.SessionRef,
		Workspace:                 state.Workspace,
		TurnID:                    strings.TrimSpace(state.TurnID),
		Controller:                state.Controller,
		RemoteSessionID:           firstNonEmpty(state.RemoteSessionID, state.Controller.RemoteSessionID),
		ControllerModel:           strings.TrimSpace(state.ControllerModel),
		ControllerReasoningEffort: strings.TrimSpace(state.ControllerReasoningEffort),
		ControllerMode:            strings.TrimSpace(state.ControllerMode),
		Input:                     strings.TrimSpace(state.Input),
		ContentParts:              model.CloneContentParts(state.ContentParts),
		ConfigOptions:             cloneControlConfigOptions(state.ConfigOptions),
		Phase:                     state.Phase,
		Error:                     strings.TrimSpace(state.Error),
		StartedAt:                 state.Time,
		UpdatedAt:                 state.Time,
	}
	return normalizeControllerRunJournalRecord(record)
}

func cloneControlConfigOptions(in []control.ConfigOption) []control.ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]control.ConfigOption, 0, len(in))
	for _, option := range in {
		option.ID = strings.TrimSpace(option.ID)
		option.Type = strings.TrimSpace(option.Type)
		option.Name = strings.TrimSpace(option.Name)
		option.Description = strings.TrimSpace(option.Description)
		option.Category = strings.TrimSpace(option.Category)
		option.CurrentValue = strings.TrimSpace(option.CurrentValue)
		option.Options = append([]control.ConfigChoice(nil), option.Options...)
		out = append(out, option)
	}
	return out
}
