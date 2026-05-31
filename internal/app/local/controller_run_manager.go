package local

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

type controllerRunManager struct {
	store   session.Store
	configs []acpexternal.Config
	journal *controllerRunJournal
	now     func() time.Time

	mu     sync.Mutex
	active map[string]struct{}
}

type controllerRunTracker struct {
	manager *controllerRunManager
	id      string
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
		active:  map[string]struct{}{},
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
		if !m.markActive(record.ID) {
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
		_ = m.journal.delete(context.Background(), record.ID)
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

func (m *controllerRunManager) markActive(id string) bool {
	if m == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.active[id]; ok {
		return false
	}
	m.active[id] = struct{}{}
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

func (t *controllerRunTracker) ControllerInvocationChanged(ctx context.Context, state control.ControllerInvocationState) error {
	if t == nil || t.manager == nil || t.manager.journal == nil {
		return nil
	}
	if t.id == "" {
		t.id = controllerRunStateID(state)
	}
	switch state.Phase {
	case control.ControllerInvocationStarted, control.ControllerInvocationRemoteSession:
		record := controllerRunRecordFromState(t.id, state)
		record.Running = true
		return t.manager.journal.write(ctx, record)
	case control.ControllerInvocationCompleted, control.ControllerInvocationFailed:
		return t.manager.journal.delete(ctx, t.id)
	default:
		return nil
	}
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
