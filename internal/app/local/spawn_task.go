package local

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

type spawnTaskManager struct {
	store   session.Store
	configs []acpexternal.Config
	journal *spawnTaskJournal
	now     func() time.Time

	mu    sync.RWMutex
	tasks map[string]*spawnTaskSession
}

type spawnTaskSession struct {
	manager *spawnTaskManager
	cfg     acpexternal.Config
	parent  session.Session
	turnID  string
	taskID  string
	agent   string

	mu              sync.RWMutex
	client          *acpexternal.Client
	remoteSessionID string
	cancel          context.CancelFunc
	done            chan struct{}
	running         bool
	state           sandbox.SessionState
	exitCode        int
	errText         string
	output          string
	pendingPrompt   string
	events          []session.Event
	recordAsync     bool
	startedAt       time.Time
	updatedAt       time.Time
}

const spawnTaskOutputPreviewCap = 16 * 1024

func newSpawnTaskManager(store session.Store, configs []acpexternal.Config, stateDir string) *spawnTaskManager {
	if len(configs) == 0 {
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
	return &spawnTaskManager{
		store:   store,
		configs: out,
		journal: newSpawnTaskJournal(stateDir),
		now:     func() time.Time { return time.Now().UTC() },
		tasks:   map[string]*spawnTaskSession{},
	}
}

func (m *spawnTaskManager) Start(req spawnTaskStartRequest) (*spawnTaskSession, error) {
	if m == nil {
		return nil, errors.New("app/local: SPAWN task manager is not configured")
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.Agent = strings.TrimSpace(req.Agent)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.TaskID == "" {
		return nil, errors.New("app/local: SPAWN task id is required")
	}
	if req.Prompt == "" {
		return nil, errors.New("app/local: SPAWN prompt is required")
	}
	now := m.clock()
	task := &spawnTaskSession{
		manager:   m,
		cfg:       req.Config,
		parent:    session.CloneSession(req.Session),
		turnID:    strings.TrimSpace(req.TurnID),
		taskID:    req.TaskID,
		agent:     req.Agent,
		done:      make(chan struct{}),
		running:   true,
		state:     sandbox.SessionRunning,
		exitCode:  -1,
		startedAt: now,
		updatedAt: now,
	}
	m.mu.Lock()
	m.tasks[task.taskID] = task
	m.mu.Unlock()
	task.persist(context.Background())
	task.startPrompt(req.Prompt, false)
	return task, nil
}

type spawnTaskStartRequest struct {
	Session session.Session
	TurnID  string
	TaskID  string
	Config  acpexternal.Config
	Agent   string
	Prompt  string
}

func (m *spawnTaskManager) OpenTask(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, bool, error) {
	if m == nil {
		return nil, false, nil
	}
	taskID := strings.TrimSpace(ref.ID)
	if taskID == "" {
		return nil, false, nil
	}
	m.mu.RLock()
	task := m.tasks[taskID]
	m.mu.RUnlock()
	if task == nil {
		return m.openJournalTask(ctx, ref)
	}
	return task, true, nil
}

func (m *spawnTaskManager) ListTasks(ctx context.Context, query sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error) {
	if m == nil {
		return nil, nil
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	m.mu.RLock()
	tasks := make([]*spawnTaskSession, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()
	out := make([]sandbox.SessionSnapshot, 0, len(tasks))
	for _, task := range tasks {
		snapshot, err := task.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	if m.journal != nil {
		archived, err := m.listJournalTasks(ctx)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(out))
		for _, snapshot := range out {
			if id := strings.TrimSpace(snapshot.Ref.ID); id != "" {
				seen[id] = struct{}{}
			}
		}
		for _, snapshot := range archived {
			id := strings.TrimSpace(snapshot.Ref.ID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			out = append(out, snapshot)
		}
	}
	sort.SliceStable(out, func(i int, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (m *spawnTaskManager) clock() time.Time {
	if m != nil && m.now != nil {
		return m.now()
	}
	return time.Now().UTC()
}

func (m *spawnTaskManager) openJournalTask(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, bool, error) {
	if m == nil || m.journal == nil {
		return nil, false, nil
	}
	if err := contextErr(ctx); err != nil {
		return nil, false, err
	}
	taskID := strings.TrimSpace(ref.ID)
	if taskID == "" {
		return nil, false, nil
	}
	record, err := m.journal.read(taskID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, false, err
		}
		return m.journal.open(ctx, ref)
	}
	record = normalizeSpawnTaskJournalRecord(record)
	if ref.Backend != "" && ref.Backend != record.Snapshot.Ref.Backend {
		return nil, false, fmt.Errorf("app/local: archived SPAWN task backend mismatch: %s", ref.Backend)
	}
	cfg, ok := m.configForAgent(record.Agent)
	if ok {
		if task, resumed, err := m.recoverLiveJournalTask(ctx, record, cfg); err != nil {
			return nil, false, err
		} else if resumed {
			return task, true, nil
		}
	}
	record = m.recoveredJournalRecord(record)
	if !ok || strings.TrimSpace(record.RemoteSessionID) == "" {
		return archivedSpawnTaskSession{record: record}, true, nil
	}
	task, err := m.recoverJournalTask(ctx, record, cfg)
	if err != nil {
		return nil, false, err
	}
	return task, true, nil
}

func (m *spawnTaskManager) listJournalTasks(ctx context.Context) ([]sandbox.SessionSnapshot, error) {
	if m == nil || m.journal == nil {
		return nil, nil
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	records, err := m.journal.readAll()
	if err != nil {
		return nil, err
	}
	out := make([]sandbox.SessionSnapshot, 0, len(records))
	for _, record := range records {
		if cfg, ok := m.configForAgent(record.Agent); ok {
			task, resumed, err := m.recoverLiveJournalTask(ctx, normalizeSpawnTaskJournalRecord(record), cfg)
			if err != nil {
				return nil, err
			}
			if resumed {
				snapshot, err := task.Snapshot(ctx)
				if err != nil {
					return nil, err
				}
				out = append(out, snapshot)
				continue
			}
		}
		out = append(out, m.recoveredJournalRecord(record).Snapshot)
	}
	sort.SliceStable(out, func(i int, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *spawnTaskManager) recoverLiveJournalTask(ctx context.Context, record spawnTaskJournalRecord, cfg acpexternal.Config) (*spawnTaskSession, bool, error) {
	if err := contextErr(ctx); err != nil {
		return nil, false, err
	}
	record = normalizeSpawnTaskJournalRecord(record)
	if !record.Snapshot.Running || strings.TrimSpace(record.PendingPrompt) == "" || strings.TrimSpace(cfg.Command) == "" {
		return nil, false, nil
	}
	taskID := strings.TrimSpace(record.Snapshot.Ref.ID)
	if taskID == "" {
		return nil, false, errors.New("app/local: SPAWN task id is required")
	}
	m.mu.RLock()
	if existing := m.tasks[taskID]; existing != nil {
		m.mu.RUnlock()
		return existing, true, nil
	}
	m.mu.RUnlock()
	parent := m.parentSessionForRecord(ctx, record)
	startedAt := record.Snapshot.StartedAt
	if startedAt.IsZero() {
		startedAt = record.Snapshot.UpdatedAt
	}
	if startedAt.IsZero() {
		startedAt = m.clock()
	}
	updatedAt := record.Snapshot.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = startedAt
	}
	task := &spawnTaskSession{
		manager:         m,
		cfg:             cfg,
		parent:          parent,
		turnID:          record.TurnID,
		taskID:          taskID,
		agent:           firstNonEmpty(record.Agent, cfg.AgentName, cfg.AgentID),
		remoteSessionID: record.RemoteSessionID,
		done:            make(chan struct{}),
		running:         true,
		state:           sandbox.SessionRunning,
		exitCode:        -1,
		output:          record.Stdout,
		pendingPrompt:   record.PendingPrompt,
		recordAsync:     true,
		startedAt:       startedAt,
		updatedAt:       updatedAt,
	}
	m.mu.Lock()
	if existing := m.tasks[taskID]; existing != nil {
		m.mu.Unlock()
		return existing, true, nil
	}
	m.tasks[taskID] = task
	m.mu.Unlock()
	task.persist(context.Background())
	task.startPrompt(record.PendingPrompt, true)
	return task, true, nil
}

func (m *spawnTaskManager) recoverJournalTask(ctx context.Context, record spawnTaskJournalRecord, cfg acpexternal.Config) (*spawnTaskSession, error) {
	taskID := strings.TrimSpace(record.Snapshot.Ref.ID)
	if taskID == "" {
		return nil, errors.New("app/local: SPAWN task id is required")
	}
	m.mu.RLock()
	if existing := m.tasks[taskID]; existing != nil {
		m.mu.RUnlock()
		return existing, nil
	}
	m.mu.RUnlock()
	parent := m.parentSessionForRecord(ctx, record)
	done := make(chan struct{})
	close(done)
	state := record.Snapshot.State
	if state == "" {
		state = sandbox.SessionFailed
	}
	startedAt := record.Snapshot.StartedAt
	if startedAt.IsZero() {
		startedAt = record.Snapshot.UpdatedAt
	}
	task := &spawnTaskSession{
		manager:         m,
		cfg:             cfg,
		parent:          parent,
		turnID:          record.TurnID,
		taskID:          taskID,
		agent:           firstNonEmpty(record.Agent, cfg.AgentName, cfg.AgentID),
		remoteSessionID: record.RemoteSessionID,
		done:            done,
		running:         false,
		state:           state,
		exitCode:        record.Snapshot.ExitCode,
		errText:         strings.TrimSpace(record.Snapshot.Error),
		output:          record.Stdout,
		startedAt:       startedAt,
		updatedAt:       record.Snapshot.UpdatedAt,
	}
	if task.updatedAt.IsZero() {
		task.updatedAt = m.clock()
	}
	m.mu.Lock()
	if existing := m.tasks[taskID]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	m.tasks[taskID] = task
	m.mu.Unlock()
	task.persist(context.Background())
	return task, nil
}

func (m *spawnTaskManager) recoveredJournalRecord(record spawnTaskJournalRecord) spawnTaskJournalRecord {
	record = recoveredSpawnTaskJournalRecord(record)
	reconnectable := strings.TrimSpace(record.RemoteSessionID) != ""
	if reconnectable {
		_, reconnectable = m.configForAgent(record.Agent)
	}
	record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
	record.Snapshot.Metadata["reconnectable"] = reconnectable
	if reconnectable {
		record.Snapshot.SupportsInput = true
		record.Snapshot.Metadata["supports_input"] = true
		if record.Snapshot.Error == "app/local: SPAWN task recovered without a live controller" {
			record.Snapshot.Error = "app/local: SPAWN task recovered without a live controller; remote session can be continued"
		}
	} else {
		record.Snapshot.Metadata["supports_input"] = record.Snapshot.SupportsInput
	}
	return record
}

func (m *spawnTaskManager) parentSessionForRecord(ctx context.Context, record spawnTaskJournalRecord) session.Session {
	if m != nil && m.store != nil && strings.TrimSpace(record.Parent.SessionID) != "" {
		snapshot, err := m.store.Load(ctx, record.Parent)
		if err == nil {
			return session.CloneSession(snapshot.Session)
		}
	}
	return session.Session{
		Ref:       session.NormalizeRef(record.Parent),
		Workspace: record.Workspace,
	}
}

func (m *spawnTaskManager) configForAgent(agent string) (acpexternal.Config, bool) {
	if m == nil {
		return acpexternal.Config{}, false
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return acpexternal.Config{}, false
	}
	for _, cfg := range m.configs {
		candidates := []string{cfg.AgentID, cfg.AgentName, cfg.Command}
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(candidate), agent) {
				return cfg, strings.TrimSpace(cfg.Command) != ""
			}
		}
	}
	return acpexternal.Config{}, false
}

func (s *spawnTaskSession) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{ID: s.taskID, Backend: sandbox.BackendCustom}
}

func (s *spawnTaskSession) Snapshot(ctx context.Context) (sandbox.SessionSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(), nil
}

func (s *spawnTaskSession) Read(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.OutputSnapshot{}, err
	}
	s.mu.RLock()
	output := s.output
	s.mu.RUnlock()
	data, next, dropped := outputSince(output, cursor.Stdout)
	return sandbox.OutputSnapshot{
		Stdout:             string(data),
		Cursor:             sandbox.OutputCursor{Stdout: next},
		StdoutDroppedBytes: dropped,
	}, nil
}

func (s *spawnTaskSession) Write(ctx context.Context, input []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	prompt := strings.TrimSpace(string(input))
	if prompt == "" {
		return errors.New("app/local: TASK write prompt is required for SPAWN task")
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("app/local: SPAWN task %q is still running; use TASK wait before TASK write", s.taskID)
	}
	if !s.supportsInputLocked() {
		s.mu.Unlock()
		return fmt.Errorf("app/local: SPAWN task %q is %s and cannot be continued", s.taskID, s.state)
	}
	s.done = make(chan struct{})
	s.running = true
	s.state = sandbox.SessionRunning
	s.exitCode = -1
	s.errText = ""
	s.touchLocked()
	s.mu.Unlock()
	s.persist(context.Background())
	s.startPrompt(prompt, true)
	return nil
}

func (s *spawnTaskSession) Cancel(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	client := s.client
	s.running = false
	s.state = sandbox.SessionCancelled
	s.exitCode = -1
	s.errText = "cancelled"
	s.pendingPrompt = ""
	s.touchLocked()
	done := s.done
	s.mu.Unlock()
	s.persist(context.Background())
	if cancel != nil {
		cancel()
	}
	if client != nil {
		_ = client.Close()
	}
	closeDone(done)
	return nil
}

func (s *spawnTaskSession) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.RLock()
	done := s.done
	s.mu.RUnlock()
	select {
	case <-done:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return sandbox.CommandResult{
			Stdout:   s.output,
			Error:    s.errText,
			ExitCode: s.exitCode,
			Route:    sandbox.RouteHost,
			Backend:  sandbox.BackendCustom,
		}, nil
	case <-ctx.Done():
		return sandbox.CommandResult{}, ctx.Err()
	}
}

func (s *spawnTaskSession) Close() error {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

func (s *spawnTaskSession) TaskMeta() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta := map[string]any{
		"task_kind": "subagent",
		"agent":     s.agent,
		"source":    "spawn",
	}
	if s.remoteSessionID != "" {
		meta["remote_session_id"] = s.remoteSessionID
	}
	return meta
}

func (s *spawnTaskSession) MarkAsync() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false
	}
	s.recordAsync = true
	return true
}

func (s *spawnTaskSession) Events() []session.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneEvents(s.events)
}

func (s *spawnTaskSession) startPrompt(prompt string, recordEvents bool) {
	ctx, cancel := context.WithCancel(context.Background())
	prompt = strings.TrimSpace(prompt)
	s.mu.Lock()
	s.cancel = cancel
	s.pendingPrompt = prompt
	if recordEvents {
		s.recordAsync = true
	}
	s.touchLocked()
	s.mu.Unlock()
	s.persist(context.Background())
	go s.runPrompt(ctx, prompt)
}

func (s *spawnTaskSession) runPrompt(ctx context.Context, prompt string) {
	events, err := s.prompt(ctx, prompt)
	finalMessage := lastAssistantText(events)
	s.mu.Lock()
	if err != nil {
		s.running = false
		if s.state != sandbox.SessionCancelled {
			s.state = sandbox.SessionFailed
			s.exitCode = -1
			s.errText = strings.TrimSpace(err.Error())
			if s.errText != "" {
				s.appendOutputLocked("error: " + s.errText + "\n")
			}
		}
	} else {
		s.running = false
		s.state = sandbox.SessionCompleted
		s.exitCode = 0
		s.errText = ""
		if strings.TrimSpace(finalMessage) != "" {
			s.appendOutputLocked(finalMessage)
		}
		s.events = append(s.events, cloneEvents(events)...)
	}
	recordAsync := s.recordAsync
	done := s.done
	s.pendingPrompt = ""
	s.touchLocked()
	s.mu.Unlock()
	if recordAsync && len(events) > 0 && s.manager != nil && s.manager.store != nil {
		_, _ = s.manager.store.Append(context.Background(), s.parent.Ref, events)
	}
	s.persist(context.Background())
	closeDone(done)
}

func (s *spawnTaskSession) prompt(ctx context.Context, prompt string) ([]session.Event, error) {
	s.mu.RLock()
	client := s.client
	remoteSessionID := s.remoteSessionID
	s.mu.RUnlock()
	if client == nil {
		started, err := acpexternal.StartProcess(ctx, s.cfg)
		if err != nil {
			return nil, err
		}
		client = started
		if err := client.InitializeSession(ctx); err != nil {
			_ = client.Close()
			return nil, err
		}
		if strings.TrimSpace(remoteSessionID) != "" {
			remote, err := client.ResumeCoreSession(ctx, remoteSessionID, s.parent.Workspace)
			if err != nil {
				_ = client.Close()
				return nil, err
			}
			remoteSessionID = remote
		} else {
			remote, err := client.NewCoreSession(ctx, s.parent.Workspace)
			if err != nil {
				_ = client.Close()
				return nil, err
			}
			remoteSessionID = remote
		}
		s.mu.Lock()
		s.client = client
		s.remoteSessionID = remoteSessionID
		s.touchLocked()
		s.mu.Unlock()
		s.persist(context.Background())
	}
	events, err := client.PromptCore(ctx, remoteSessionID, []model.ContentPart{{
		Type: model.ContentPartText,
		Text: strings.TrimSpace(prompt),
	}})
	if err != nil {
		return nil, err
	}
	participant := session.ParticipantBinding{
		ID:           s.taskID,
		Kind:         session.ParticipantSubagent,
		Role:         session.ParticipantDelegated,
		AgentName:    s.agent,
		Label:        s.agent,
		SessionID:    remoteSessionID,
		Source:       "spawn",
		ParentTurnID: strings.TrimSpace(s.turnID),
		DelegationID: s.taskID,
		AttachedAt:   s.startedAt,
	}
	events = control.NormalizeParticipantEvents(s.parent.SessionID, remoteSessionID, participant, events, s.manager.clock())
	for idx := range events {
		if events[idx].Scope == nil {
			continue
		}
		events[idx].Scope.Source = "spawn"
		events[idx].Scope.Participant = participant
	}
	return events, nil
}

func (s *spawnTaskSession) snapshotLocked() sandbox.SessionSnapshot {
	supportsInput := s.supportsInputLocked()
	metadata := map[string]any{
		"task_kind":      "subagent",
		"source":         "spawn",
		"agent":          s.agent,
		"state":          string(s.state),
		"running":        s.running,
		"supports_input": supportsInput,
	}
	if s.remoteSessionID != "" {
		metadata["remote_session_id"] = s.remoteSessionID
		metadata["reconnectable"] = supportsInput
	}
	if !s.running {
		metadata["exit_code"] = s.exitCode
	} else if s.pendingPrompt != "" {
		metadata["pending_prompt"] = true
	}
	return sandbox.SessionSnapshot{
		Ref:           s.Ref(),
		Command:       strings.TrimSpace("SPAWN " + s.agent),
		State:         s.state,
		Running:       s.running,
		SupportsInput: supportsInput,
		ExitCode:      s.exitCode,
		Error:         s.errText,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
		Terminal: sandbox.TerminalRef{
			ID:        "spawn-" + s.taskID,
			SessionID: s.taskID,
		},
		OutputPreview: spawnTaskOutputPreview(s.output, spawnTaskOutputPreviewCap),
		Metadata:      metadata,
	}
}

func (s *spawnTaskSession) persist(ctx context.Context) {
	if s == nil || s.manager == nil || s.manager.journal == nil {
		return
	}
	s.mu.RLock()
	record := spawnTaskJournalRecord{
		Parent:           s.parent.Ref,
		Workspace:        s.parent.Workspace,
		TurnID:           s.turnID,
		Agent:            s.agent,
		RemoteSessionID:  s.remoteSessionID,
		PendingPrompt:    pendingPromptForJournal(s.running, s.pendingPrompt),
		Snapshot:         s.snapshotLocked(),
		Stdout:           s.output,
		StdoutTotalBytes: int64(len([]byte(s.output))),
		UpdatedAt:        s.updatedAt,
	}
	s.mu.RUnlock()
	_ = s.manager.journal.write(ctx, record)
}

func (s *spawnTaskSession) supportsInputLocked() bool {
	if s == nil || s.running {
		return false
	}
	return strings.TrimSpace(s.remoteSessionID) != "" && strings.TrimSpace(s.cfg.Command) != ""
}

func pendingPromptForJournal(running bool, prompt string) string {
	if !running {
		return ""
	}
	return strings.TrimSpace(prompt)
}

func (s *spawnTaskSession) appendOutputLocked(text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	if s.output != "" {
		s.output += "\n"
	}
	s.output += text + "\n"
}

func (s *spawnTaskSession) touchLocked() {
	s.updatedAt = s.manager.clock()
}

func outputSince(text string, cursor int64) ([]byte, int64, int64) {
	data := []byte(text)
	total := int64(len(data))
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		return nil, total, 0
	}
	start := int(cursor)
	if start > len(data) {
		start = len(data)
	}
	return append([]byte(nil), data[start:]...), total, 0
}

func spawnTaskOutputPreview(text string, limit int) *sandbox.OutputSnapshot {
	data := []byte(text)
	total := int64(len(data))
	if total == 0 {
		return nil
	}
	dropped := int64(0)
	if limit > 0 && len(data) > limit {
		drop := len(data) - limit
		data = append([]byte(nil), data[drop:]...)
		dropped = int64(drop)
	}
	return &sandbox.OutputSnapshot{
		Stdout:             string(data),
		Cursor:             sandbox.OutputCursor{Stdout: total},
		StdoutDroppedBytes: dropped,
	}
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func closeDone(done chan struct{}) {
	defer func() { _ = recover() }()
	close(done)
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

var _ sandbox.Session = (*spawnTaskSession)(nil)
