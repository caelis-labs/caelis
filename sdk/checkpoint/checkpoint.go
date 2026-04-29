package checkpoint

import (
	"encoding/json"
	"maps"
	"strings"
	"time"
)

// State is the durable structured checkpoint kept in session state.
type State struct {
	Revision              int                `json:"revision,omitempty"`
	SummarizedThroughID   string             `json:"summarized_through_id,omitempty"`
	UpdatedAt             time.Time          `json:"updated_at,omitempty"`
	Trigger               string             `json:"trigger,omitempty"`
	Generator             string             `json:"generator,omitempty"`
	Objective             string             `json:"objective,omitempty"`
	UserConstraints       []string           `json:"user_constraints,omitempty"`
	DurableDecisions      []string           `json:"durable_decisions,omitempty"`
	VerifiedFacts         []string           `json:"verified_facts,omitempty"`
	CurrentProgress       []string           `json:"current_progress,omitempty"`
	OpenQuestionsAndRisks []string           `json:"open_questions_and_risks,omitempty"`
	NextActions           []string           `json:"next_actions,omitempty"`
	ActiveTasks           []TaskState        `json:"active_tasks,omitempty"`
	ActiveParticipants    []ParticipantState `json:"active_participants,omitempty"`
	LatestBlockers        []string           `json:"latest_blockers,omitempty"`
	OperationalAnnex      OperationalAnnex   `json:"operational_annex,omitempty"`
}

type TaskState struct {
	TaskID  string `json:"task_id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	State   string `json:"state,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ParticipantState struct {
	Agent     string `json:"agent,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	State     string `json:"state,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type OperationalAnnex struct {
	FilesTouched []string `json:"files_touched,omitempty"`
	CommandsRun  []string `json:"commands_run,omitempty"`
}

// Meta is the lightweight runtime bookkeeping stored alongside the checkpoint.
type Meta struct {
	Revision              int       `json:"revision,omitempty"`
	SummarizedThroughID   string    `json:"summarized_through_id,omitempty"`
	TailAnchorID          string    `json:"tail_anchor_id,omitempty"`
	LastCompactedAt       time.Time `json:"last_compacted_at,omitempty"`
	LastVisibleTokenCount int       `json:"last_visible_token_count,omitempty"`
	LastCompactReason     string    `json:"last_compact_reason,omitempty"`
}

func NormalizeState(in State) State {
	out := in
	out.SummarizedThroughID = strings.TrimSpace(in.SummarizedThroughID)
	out.Trigger = strings.TrimSpace(in.Trigger)
	out.Generator = strings.TrimSpace(in.Generator)
	out.Objective = strings.TrimSpace(in.Objective)
	out.UserConstraints = normalizeStringList(in.UserConstraints, 12)
	out.DurableDecisions = normalizeStringList(in.DurableDecisions, 12)
	out.VerifiedFacts = normalizeStringList(in.VerifiedFacts, 16)
	out.CurrentProgress = normalizeStringList(in.CurrentProgress, 12)
	out.OpenQuestionsAndRisks = normalizeStringList(in.OpenQuestionsAndRisks, 12)
	out.NextActions = normalizeStringList(in.NextActions, 12)
	out.LatestBlockers = normalizeStringList(in.LatestBlockers, 8)
	out.ActiveTasks = normalizeTasks(in.ActiveTasks)
	out.ActiveParticipants = normalizeParticipants(in.ActiveParticipants)
	out.OperationalAnnex = normalizeAnnex(in.OperationalAnnex)
	return out
}

func NormalizeMeta(in Meta) Meta {
	return Meta{
		Revision:              in.Revision,
		SummarizedThroughID:   strings.TrimSpace(in.SummarizedThroughID),
		TailAnchorID:          strings.TrimSpace(in.TailAnchorID),
		LastCompactedAt:       in.LastCompactedAt,
		LastVisibleTokenCount: in.LastVisibleTokenCount,
		LastCompactReason:     strings.TrimSpace(in.LastCompactReason),
	}
}

func Merge(base State, update State) State {
	base = NormalizeState(base)
	update = NormalizeState(update)
	out := base
	if update.Revision > 0 {
		out.Revision = update.Revision
	}
	if update.SummarizedThroughID != "" {
		out.SummarizedThroughID = update.SummarizedThroughID
	}
	if !update.UpdatedAt.IsZero() {
		out.UpdatedAt = update.UpdatedAt
	}
	if update.Trigger != "" {
		out.Trigger = update.Trigger
	}
	if update.Generator != "" {
		out.Generator = update.Generator
	}
	if update.Objective != "" {
		out.Objective = update.Objective
	}
	out.UserConstraints = mergeStringLists(base.UserConstraints, update.UserConstraints, 12)
	out.DurableDecisions = mergeStringLists(base.DurableDecisions, update.DurableDecisions, 12)
	out.VerifiedFacts = mergeStringLists(base.VerifiedFacts, update.VerifiedFacts, 16)
	out.CurrentProgress = preferList(update.CurrentProgress, base.CurrentProgress, 12)
	out.OpenQuestionsAndRisks = preferList(update.OpenQuestionsAndRisks, base.OpenQuestionsAndRisks, 12)
	out.NextActions = preferList(update.NextActions, base.NextActions, 12)
	out.LatestBlockers = preferList(update.LatestBlockers, base.LatestBlockers, 8)
	if len(update.ActiveTasks) > 0 {
		out.ActiveTasks = normalizeTasks(update.ActiveTasks)
	}
	if len(update.ActiveParticipants) > 0 {
		out.ActiveParticipants = normalizeParticipants(update.ActiveParticipants)
	}
	annex := base.OperationalAnnex
	if len(update.OperationalAnnex.FilesTouched) > 0 {
		annex.FilesTouched = mergeStringLists(base.OperationalAnnex.FilesTouched, update.OperationalAnnex.FilesTouched, 16)
	}
	if len(update.OperationalAnnex.CommandsRun) > 0 {
		annex.CommandsRun = mergeStringLists(base.OperationalAnnex.CommandsRun, update.OperationalAnnex.CommandsRun, 16)
	}
	out.OperationalAnnex = normalizeAnnex(annex)
	return NormalizeState(out)
}

func StateFromValue(raw any) State {
	if raw == nil {
		return State{}
	}
	switch typed := raw.(type) {
	case State:
		return NormalizeState(typed)
	case map[string]any:
		return fromMap[State](typed)
	default:
		return State{}
	}
}

func MetaFromValue(raw any) Meta {
	if raw == nil {
		return Meta{}
	}
	switch typed := raw.(type) {
	case Meta:
		return NormalizeMeta(typed)
	case map[string]any:
		return fromMap[Meta](typed)
	default:
		return Meta{}
	}
}

func StateValue(in State) map[string]any {
	return toMap(NormalizeState(in))
}

func MetaValue(in Meta) map[string]any {
	return toMap(NormalizeMeta(in))
}

func fromMap[T any](value map[string]any) T {
	var out T
	raw, _ := json.Marshal(value)
	_ = json.Unmarshal(raw, &out)
	switch any(out).(type) {
	case State:
		return any(NormalizeState(any(out).(State))).(T)
	case Meta:
		return any(NormalizeMeta(any(out).(Meta))).(T)
	default:
		return out
	}
}

func toMap(value any) map[string]any {
	raw, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 {
		return map[string]any{}
	}
	return out
}

func normalizeTasks(in []TaskState) []TaskState {
	if len(in) == 0 {
		return nil
	}
	out := make([]TaskState, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		item.TaskID = strings.TrimSpace(item.TaskID)
		item.Kind = strings.TrimSpace(item.Kind)
		item.State = strings.TrimSpace(item.State)
		item.Summary = strings.TrimSpace(item.Summary)
		key := item.TaskID + "\x00" + item.Kind
		if item.TaskID == "" || item.Summary == "" || key == "\x00" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizeParticipants(in []ParticipantState) []ParticipantState {
	if len(in) == 0 {
		return nil
	}
	out := make([]ParticipantState, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		item.Agent = strings.TrimSpace(item.Agent)
		item.SessionID = strings.TrimSpace(item.SessionID)
		item.State = strings.TrimSpace(item.State)
		item.Summary = strings.TrimSpace(item.Summary)
		key := item.Agent + "\x00" + item.SessionID
		if item.Agent == "" || item.Summary == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizeAnnex(in OperationalAnnex) OperationalAnnex {
	return OperationalAnnex{
		FilesTouched: normalizeStringList(in.FilesTouched, 16),
		CommandsRun:  normalizeStringList(in.CommandsRun, 16),
	}
}

func normalizeStringList(in []string, limit int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func mergeStringLists(base []string, update []string, limit int) []string {
	return normalizeStringList(append(append([]string{}, update...), base...), limit)
}

func preferList(primary []string, fallback []string, limit int) []string {
	if len(primary) > 0 {
		return normalizeStringList(primary, limit)
	}
	return normalizeStringList(fallback, limit)
}

func CloneStateMap(values map[string]any) map[string]any {
	return maps.Clone(values)
}
