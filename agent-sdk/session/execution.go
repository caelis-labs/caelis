package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
)

const ToolExecutionSchemaVersion = 1

// ExecutionJournalSchemaVersion is the current durable execution journal
// schema. Journal migrations are registered by the session schema layer.
const ExecutionJournalSchemaVersion = 1

type JournalKind string

const (
	JournalKindRun           JournalKind = "run"
	JournalKindTurn          JournalKind = "turn"
	JournalKindStep          JournalKind = "step"
	JournalKindToolExecution JournalKind = "tool_execution"
	JournalKindPauseToken    JournalKind = "pause_token"
)

// PauseTokenStatus identifies one durable approval pause state.
type PauseTokenStatus string

const (
	PauseTokenPending   PauseTokenStatus = "pending"
	PauseTokenResolved  PauseTokenStatus = "resolved"
	PauseTokenCancelled PauseTokenStatus = "cancelled"
)

// PauseToken is the durable handoff between a waiting run and an approval
// resolver. It contains only semantic request/decision data.
type PauseToken struct {
	Schema     int               `json:"schema"`
	TokenID    string            `json:"token_id"`
	SessionID  string            `json:"session_id"`
	RunID      string            `json:"run_id"`
	TurnID     string            `json:"turn_id"`
	ToolCallID string            `json:"tool_call_id"`
	ToolName   string            `json:"tool_name"`
	Revision   uint64            `json:"revision"`
	Status     PauseTokenStatus  `json:"status"`
	Input      json.RawMessage   `json:"input,omitempty"`
	Approval   *ProtocolApproval `json:"approval,omitempty"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	OptionID   string            `json:"option_id,omitempty"`
	Approved   bool              `json:"approved,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	ReviewText string            `json:"review_text,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ExecutionStatus identifies one durable Run, Turn, or Step state.
type ExecutionStatus string

const (
	ExecutionPrepared        ExecutionStatus = "prepared"
	ExecutionStarted         ExecutionStatus = "started"
	ExecutionWaitingApproval ExecutionStatus = "waiting_approval"
	ExecutionSucceeded       ExecutionStatus = "succeeded"
	ExecutionFailed          ExecutionStatus = "failed"
	ExecutionCancelRequested ExecutionStatus = "cancel_requested"
	ExecutionCancelled       ExecutionStatus = "cancelled"
	ExecutionInterrupted     ExecutionStatus = "interrupted"
	ExecutionUnknownOutcome  ExecutionStatus = "unknown_outcome"
)

// ExecutionRecord is the durable state of one Run, Turn, or Step. Identity is
// derived from Kind and the non-empty execution IDs.
type ExecutionRecord struct {
	Schema        int             `json:"schema"`
	Kind          JournalKind     `json:"kind"`
	SessionID     string          `json:"session_id"`
	RunID         string          `json:"run_id"`
	TurnID        string          `json:"turn_id,omitempty"`
	StepID        string          `json:"step_id,omitempty"`
	Identity      string          `json:"identity"`
	Revision      uint64          `json:"revision"`
	Status        ExecutionStatus `json:"status"`
	RecoveredFrom ExecutionStatus `json:"recovered_from,omitempty"`
	Error         string          `json:"error,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type ToolExecutionStatus string

const (
	ToolExecutionPrepared        ToolExecutionStatus = "prepared"
	ToolExecutionApproved        ToolExecutionStatus = "approved"
	ToolExecutionStarted         ToolExecutionStatus = "started"
	ToolExecutionSucceeded       ToolExecutionStatus = "succeeded"
	ToolExecutionFailed          ToolExecutionStatus = "failed"
	ToolExecutionCancelRequested ToolExecutionStatus = "cancel_requested"
	ToolExecutionCancelled       ToolExecutionStatus = "cancelled"
	ToolExecutionUnknownOutcome  ToolExecutionStatus = "unknown_outcome"
)

type ExecutionKey struct {
	SessionID  string `json:"session_id"`
	RunID      string `json:"run_id"`
	TurnID     string `json:"turn_id"`
	StepID     string `json:"step_id"`
	ToolCallID string `json:"tool_call_id"`
}

func NormalizeExecutionKey(in ExecutionKey) ExecutionKey {
	return ExecutionKey{SessionID: strings.TrimSpace(in.SessionID), RunID: strings.TrimSpace(in.RunID), TurnID: strings.TrimSpace(in.TurnID), StepID: strings.TrimSpace(in.StepID), ToolCallID: strings.TrimSpace(in.ToolCallID)}
}

func ExecutionIdentity(key ExecutionKey) string {
	key = NormalizeExecutionKey(key)
	raw, _ := json.Marshal([]string{key.SessionID, key.RunID, key.TurnID, key.StepID, key.ToolCallID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type ToolExecution struct {
	Schema        int                 `json:"schema"`
	Key           ExecutionKey        `json:"key"`
	Identity      string              `json:"identity"`
	Revision      uint64              `json:"revision"`
	ToolName      string              `json:"tool_name"`
	EffectClass   string              `json:"effect_class"`
	Status        ToolExecutionStatus `json:"status"`
	RecoveredFrom ToolExecutionStatus `json:"recovered_from,omitempty"`
	Input         json.RawMessage     `json:"input,omitempty"`
	Result        json.RawMessage     `json:"result,omitempty"`
	Error         string              `json:"error,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type ExecutionJournalEntry struct {
	Schema        int              `json:"schema"`
	Kind          JournalKind      `json:"kind"`
	Execution     *ExecutionRecord `json:"execution,omitempty"`
	ToolExecution *ToolExecution   `json:"tool_execution,omitempty"`
	PauseToken    *PauseToken      `json:"pause_token,omitempty"`
}

// ExecutionTransitionError reports an invalid durable Run, Turn, or Step
// state transition.
type ExecutionTransitionError struct {
	Identity string
	Kind     JournalKind
	From     ExecutionStatus
	To       ExecutionStatus
	Detail   string
}

func (e *ExecutionTransitionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/session: %s execution %q transition %q -> %q: %s", e.Kind, e.Identity, e.From, e.To, strings.TrimSpace(e.Detail))
}

type ToolExecutionTransitionError struct {
	Identity string
	From     ToolExecutionStatus
	To       ToolExecutionStatus
	Detail   string
}

// PauseTokenTransitionError reports an invalid durable approval resolution.
type PauseTokenTransitionError struct {
	TokenID string
	From    PauseTokenStatus
	To      PauseTokenStatus
	Detail  string
}

func (e *PauseTokenTransitionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/session: pause token %q transition %q -> %q: %s", e.TokenID, e.From, e.To, strings.TrimSpace(e.Detail))
}

func (e *ToolExecutionTransitionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/session: tool execution %q transition %q -> %q: %s", e.Identity, e.From, e.To, strings.TrimSpace(e.Detail))
}

func NormalizeToolExecution(in ToolExecution) ToolExecution {
	out := in
	out.Key = NormalizeExecutionKey(in.Key)
	out.Identity = strings.TrimSpace(in.Identity)
	if out.Identity == "" {
		out.Identity = ExecutionIdentity(out.Key)
	}
	out.ToolName = strings.TrimSpace(in.ToolName)
	out.EffectClass = strings.TrimSpace(in.EffectClass)
	out.Status = ToolExecutionStatus(strings.TrimSpace(string(in.Status)))
	out.RecoveredFrom = ToolExecutionStatus(strings.TrimSpace(string(in.RecoveredFrom)))
	out.Input = append(json.RawMessage(nil), in.Input...)
	out.Result = append(json.RawMessage(nil), in.Result...)
	out.Error = strings.TrimSpace(in.Error)
	out.Reason = strings.TrimSpace(in.Reason)
	return out
}

func CloneToolExecution(in ToolExecution) ToolExecution { return NormalizeToolExecution(in) }

func CloneExecutionJournalEntry(in ExecutionJournalEntry) ExecutionJournalEntry {
	out := in
	out.Kind = JournalKind(strings.TrimSpace(string(in.Kind)))
	if in.Execution != nil {
		record := NormalizeExecutionRecord(*in.Execution)
		out.Execution = &record
	}
	if in.ToolExecution != nil {
		record := CloneToolExecution(*in.ToolExecution)
		out.ToolExecution = &record
	}
	if in.PauseToken != nil {
		token := ClonePauseToken(*in.PauseToken)
		out.PauseToken = &token
	}
	return out
}

// ClonePauseToken returns one recursively isolated normalized token.
func ClonePauseToken(in PauseToken) PauseToken {
	out := in
	out.TokenID = strings.TrimSpace(in.TokenID)
	out.SessionID = strings.TrimSpace(in.SessionID)
	out.RunID = strings.TrimSpace(in.RunID)
	out.TurnID = strings.TrimSpace(in.TurnID)
	out.ToolCallID = strings.TrimSpace(in.ToolCallID)
	out.ToolName = strings.TrimSpace(in.ToolName)
	out.Status = PauseTokenStatus(strings.TrimSpace(string(in.Status)))
	out.Input = append(json.RawMessage(nil), in.Input...)
	if in.Approval != nil {
		approval := CloneProtocolApproval(*in.Approval)
		out.Approval = &approval
	}
	out.Metadata = CloneState(in.Metadata)
	out.Outcome = strings.TrimSpace(in.Outcome)
	out.OptionID = strings.TrimSpace(in.OptionID)
	out.Reason = strings.TrimSpace(in.Reason)
	out.ReviewText = strings.TrimSpace(in.ReviewText)
	return out
}

// ValidatePauseTokenTransition validates one durable approval pause update.
func ValidatePauseTokenTransition(previous PauseToken, next PauseToken) error {
	previous = ClonePauseToken(previous)
	next = ClonePauseToken(next)
	invalid := func(detail string) error {
		return &PauseTokenTransitionError{TokenID: next.TokenID, From: previous.Status, To: next.Status, Detail: detail}
	}
	if next.Schema != ExecutionJournalSchemaVersion || next.TokenID == "" || next.SessionID == "" || next.RunID == "" || next.TurnID == "" {
		return invalid("invalid schema or identity")
	}
	if previous.Status == "" {
		if next.Status == PauseTokenPending && next.Revision == 1 {
			return nil
		}
		return invalid("first record must be pending revision 1")
	}
	if previous.TokenID != next.TokenID || previous.SessionID != next.SessionID || previous.RunID != next.RunID || next.Revision != previous.Revision+1 {
		return invalid("identity or revision mismatch")
	}
	if previous.Status != PauseTokenPending || (next.Status != PauseTokenResolved && next.Status != PauseTokenCancelled) {
		return invalid("transition is not allowed")
	}
	return nil
}

// ExecutionRecordIdentity returns the stable identity for one Run, Turn, or
// Step record.
func ExecutionRecordIdentity(record ExecutionRecord) string {
	raw, _ := json.Marshal([]string{
		strings.TrimSpace(string(record.Kind)),
		strings.TrimSpace(record.SessionID),
		strings.TrimSpace(record.RunID),
		strings.TrimSpace(record.TurnID),
		strings.TrimSpace(record.StepID),
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// NormalizeExecutionRecord returns one isolated normalized journal record.
func NormalizeExecutionRecord(in ExecutionRecord) ExecutionRecord {
	out := in
	out.Kind = JournalKind(strings.TrimSpace(string(in.Kind)))
	out.SessionID = strings.TrimSpace(in.SessionID)
	out.RunID = strings.TrimSpace(in.RunID)
	out.TurnID = strings.TrimSpace(in.TurnID)
	out.StepID = strings.TrimSpace(in.StepID)
	out.Identity = strings.TrimSpace(in.Identity)
	if out.Identity == "" {
		out.Identity = ExecutionRecordIdentity(out)
	}
	out.Status = ExecutionStatus(strings.TrimSpace(string(in.Status)))
	out.RecoveredFrom = ExecutionStatus(strings.TrimSpace(string(in.RecoveredFrom)))
	out.Error = strings.TrimSpace(in.Error)
	out.Reason = strings.TrimSpace(in.Reason)
	return out
}

// ValidateExecutionTransition validates one Run, Turn, or Step state update.
func ValidateExecutionTransition(previous ExecutionRecord, next ExecutionRecord) error {
	previous = NormalizeExecutionRecord(previous)
	next = NormalizeExecutionRecord(next)
	invalid := func(detail string) error {
		return &ExecutionTransitionError{Identity: next.Identity, Kind: next.Kind, From: previous.Status, To: next.Status, Detail: detail}
	}
	if next.Schema != ExecutionJournalSchemaVersion || next.SessionID == "" || next.RunID == "" {
		return invalid("invalid schema or execution key")
	}
	switch next.Kind {
	case JournalKindRun:
		if next.TurnID != "" || next.StepID != "" {
			return invalid("run record cannot carry turn or step identity")
		}
	case JournalKindTurn:
		if next.TurnID == "" || next.StepID != "" {
			return invalid("turn record requires turn identity and no step identity")
		}
	case JournalKindStep:
		if next.TurnID == "" || next.StepID == "" {
			return invalid("step record requires turn and step identity")
		}
	default:
		return invalid("unsupported execution kind")
	}
	if err := jsonvalue.Validate(next); err != nil {
		return invalid(err.Error())
	}
	if previous.Status == "" {
		if next.Status == ExecutionPrepared && next.Revision == 1 {
			return nil
		}
		return invalid("first record must be prepared revision 1")
	}
	if previous.Identity != next.Identity || previous.Kind != next.Kind || next.Revision != previous.Revision+1 {
		return invalid("identity, kind, or revision mismatch")
	}
	allowed := map[ExecutionStatus]map[ExecutionStatus]bool{
		ExecutionPrepared:        {ExecutionStarted: true, ExecutionFailed: true, ExecutionCancelled: true},
		ExecutionStarted:         {ExecutionWaitingApproval: true, ExecutionSucceeded: true, ExecutionFailed: true, ExecutionCancelRequested: true, ExecutionCancelled: true, ExecutionInterrupted: true, ExecutionUnknownOutcome: true},
		ExecutionWaitingApproval: {ExecutionStarted: true, ExecutionFailed: true, ExecutionCancelRequested: true, ExecutionCancelled: true, ExecutionInterrupted: true},
		ExecutionCancelRequested: {ExecutionCancelled: true, ExecutionSucceeded: true, ExecutionFailed: true, ExecutionInterrupted: true, ExecutionUnknownOutcome: true},
	}
	if !allowed[previous.Status][next.Status] {
		return invalid("transition is not allowed")
	}
	return nil
}

// ValidateExecutionJournalEntry validates the self-contained schema and
// identity invariants of one durable journal entry. Cross-entry transitions
// are validated by the runtime before append.
func ValidateExecutionJournalEntry(in ExecutionJournalEntry) error {
	entry := CloneExecutionJournalEntry(in)
	if entry.Schema != ExecutionJournalSchemaVersion {
		return fmt.Errorf("agent-sdk/session: unsupported execution journal schema %d", entry.Schema)
	}
	if entry.Execution == nil && entry.ToolExecution == nil && entry.PauseToken == nil {
		return fmt.Errorf("agent-sdk/session: execution journal entry is empty")
	}
	if entry.Execution != nil {
		record := NormalizeExecutionRecord(*entry.Execution)
		if record.Kind != entry.Kind && entry.Kind != JournalKindToolExecution {
			return fmt.Errorf("agent-sdk/session: execution journal kind %q does not match record kind %q", entry.Kind, record.Kind)
		}
		if record.Identity != ExecutionRecordIdentity(record) || record.Revision == 0 || !validExecutionStatus(record.Status) {
			return fmt.Errorf("agent-sdk/session: invalid %s execution identity, revision, or status", record.Kind)
		}
		if err := validateExecutionRecordKey(record); err != nil {
			return err
		}
	}
	if entry.ToolExecution != nil {
		record := NormalizeToolExecution(*entry.ToolExecution)
		if entry.Kind != JournalKindToolExecution || record.Schema != ToolExecutionSchemaVersion || record.Identity != ExecutionIdentity(record.Key) || record.Revision == 0 || !validToolExecutionStatus(record.Status) {
			return fmt.Errorf("agent-sdk/session: invalid tool execution schema, identity, revision, or status")
		}
		if record.Key.SessionID == "" || record.Key.RunID == "" || record.Key.TurnID == "" || record.Key.ToolCallID == "" {
			return fmt.Errorf("agent-sdk/session: invalid tool execution key")
		}
		if entry.Execution != nil {
			step := NormalizeExecutionRecord(*entry.Execution)
			if step.Kind != JournalKindStep || step.SessionID != record.Key.SessionID || step.RunID != record.Key.RunID || step.TurnID != record.Key.TurnID || step.StepID != record.Key.StepID {
				return fmt.Errorf("agent-sdk/session: tool execution does not match step record")
			}
		}
	}
	if entry.PauseToken != nil {
		token := ClonePauseToken(*entry.PauseToken)
		if entry.Kind != JournalKindPauseToken || token.Schema != ExecutionJournalSchemaVersion || token.TokenID == "" || token.SessionID == "" || token.RunID == "" || token.TurnID == "" || token.Revision == 0 {
			return fmt.Errorf("agent-sdk/session: invalid pause token schema or identity")
		}
		switch token.Status {
		case PauseTokenPending:
			if token.Revision != 1 {
				return fmt.Errorf("agent-sdk/session: pending pause token must be revision 1")
			}
		case PauseTokenResolved, PauseTokenCancelled:
			if token.Revision != 2 {
				return fmt.Errorf("agent-sdk/session: terminal pause token must be revision 2")
			}
		default:
			return fmt.Errorf("agent-sdk/session: invalid pause token status %q", token.Status)
		}
	}
	return jsonvalue.Validate(entry)
}

func validateExecutionRecordKey(record ExecutionRecord) error {
	if record.Schema != ExecutionJournalSchemaVersion || record.SessionID == "" || record.RunID == "" {
		return fmt.Errorf("agent-sdk/session: invalid execution record schema or key")
	}
	switch record.Kind {
	case JournalKindRun:
		if record.TurnID == "" && record.StepID == "" {
			return nil
		}
	case JournalKindTurn:
		if record.TurnID != "" && record.StepID == "" {
			return nil
		}
	case JournalKindStep:
		if record.TurnID != "" && record.StepID != "" {
			return nil
		}
	}
	return fmt.Errorf("agent-sdk/session: invalid %s execution key", record.Kind)
}

func validExecutionStatus(status ExecutionStatus) bool {
	switch status {
	case ExecutionPrepared, ExecutionStarted, ExecutionWaitingApproval, ExecutionSucceeded, ExecutionFailed, ExecutionCancelRequested, ExecutionCancelled, ExecutionInterrupted, ExecutionUnknownOutcome:
		return true
	default:
		return false
	}
}

func validToolExecutionStatus(status ToolExecutionStatus) bool {
	switch status {
	case ToolExecutionPrepared, ToolExecutionApproved, ToolExecutionStarted, ToolExecutionSucceeded, ToolExecutionFailed, ToolExecutionCancelRequested, ToolExecutionCancelled, ToolExecutionUnknownOutcome:
		return true
	default:
		return false
	}
}

func ValidateToolExecutionTransition(previous ToolExecution, next ToolExecution) error {
	next = NormalizeToolExecution(next)
	previous = NormalizeToolExecution(previous)
	if next.Schema != ToolExecutionSchemaVersion || next.Identity == "" || next.Key.SessionID == "" || next.Key.RunID == "" || next.Key.TurnID == "" || next.Key.ToolCallID == "" {
		return &ToolExecutionTransitionError{Identity: next.Identity, From: previous.Status, To: next.Status, Detail: "invalid schema or execution key"}
	}
	if err := jsonvalue.Validate(next); err != nil {
		return &ToolExecutionTransitionError{Identity: next.Identity, From: previous.Status, To: next.Status, Detail: err.Error()}
	}
	if previous.Status == "" {
		if next.Status == ToolExecutionPrepared && next.Revision == 1 {
			return nil
		}
		return &ToolExecutionTransitionError{Identity: next.Identity, To: next.Status, Detail: "first record must be prepared revision 1"}
	}
	if previous.Identity != next.Identity || next.Revision != previous.Revision+1 {
		return &ToolExecutionTransitionError{Identity: next.Identity, From: previous.Status, To: next.Status, Detail: "identity or revision mismatch"}
	}
	allowed := map[ToolExecutionStatus]map[ToolExecutionStatus]bool{
		ToolExecutionPrepared:        {ToolExecutionApproved: true, ToolExecutionFailed: true, ToolExecutionCancelled: true},
		ToolExecutionApproved:        {ToolExecutionStarted: true, ToolExecutionCancelled: true},
		ToolExecutionStarted:         {ToolExecutionSucceeded: true, ToolExecutionFailed: true, ToolExecutionCancelRequested: true, ToolExecutionUnknownOutcome: true},
		ToolExecutionCancelRequested: {ToolExecutionCancelled: true, ToolExecutionSucceeded: true, ToolExecutionFailed: true, ToolExecutionUnknownOutcome: true},
	}
	if !allowed[previous.Status][next.Status] {
		return &ToolExecutionTransitionError{Identity: next.Identity, From: previous.Status, To: next.Status, Detail: "transition is not allowed"}
	}
	return nil
}

func RecoverIncompleteToolExecutions(events []*Event) []ToolExecution {
	latest := map[string]ToolExecution{}
	order := make([]string, 0)
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		record := NormalizeToolExecution(*event.Journal.ToolExecution)
		if _, ok := latest[record.Identity]; !ok {
			order = append(order, record.Identity)
		}
		if prior, ok := latest[record.Identity]; !ok || record.Revision > prior.Revision {
			latest[record.Identity] = record
		}
	}
	out := make([]ToolExecution, 0)
	for _, identity := range order {
		record := latest[identity]
		if record.Status != ToolExecutionStarted && record.Status != ToolExecutionCancelRequested {
			continue
		}
		record.Revision++
		record.RecoveredFrom = record.Status
		record.Status = ToolExecutionUnknownOutcome
		record.Reason = "runtime recovered without a durable terminal result"
		out = append(out, record)
	}
	return out
}
