package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Mode describes how one approval request should be resolved.
type Mode string

const (
	ModeAutoReview Mode = "auto-review"
	ModeManual     Mode = "manual"
)

// NormalizeMode collapses compatibility spellings into the public approval
// modes used across runtime, gateway, and ACP adapters.
func NormalizeMode(mode string) Mode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "manual":
		return ModeManual
	case "auto-review", "auto_review", "autoreview":
		return ModeAutoReview
	default:
		return ModeAutoReview
	}
}

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusSelected Status = "selected"
)

type ReviewStatus string

const (
	ReviewStatusInProgress ReviewStatus = "in_progress"
	ReviewStatusApproved   ReviewStatus = "approved"
	ReviewStatusDenied     ReviewStatus = "denied"
	ReviewStatusTimedOut   ReviewStatus = "timed_out"
	ReviewStatusFailed     ReviewStatus = "failed"
)

type Option struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type Payload struct {
	Context            *session.PermissionContext        `json:"context,omitempty"`
	Subject            *session.ApprovalSubject          `json:"subject,omitempty"`
	ToolCallID         string                            `json:"tool_call_id,omitempty"`
	ToolName           string                            `json:"tool_name,omitempty"`
	ToolKind           string                            `json:"tool_kind,omitempty"`
	ToolTitle          string                            `json:"tool_title,omitempty"`
	ToolStatus         string                            `json:"tool_status,omitempty"`
	RawInput           map[string]any                    `json:"raw_input,omitempty"`
	RawOutput          map[string]any                    `json:"raw_output,omitempty"`
	Content            []session.ProtocolToolCallContent `json:"content,omitempty"`
	Reason             string                            `json:"reason,omitempty"`
	Justification      string                            `json:"justification,omitempty"`
	SandboxPermissions string                            `json:"sandbox_permissions,omitempty"`
	Status             Status                            `json:"status,omitempty"`
	Options            []Option                          `json:"options,omitempty"`
	ReviewID           string                            `json:"review_id,omitempty"`
	ReviewStatus       ReviewStatus                      `json:"review_status,omitempty"`
	ReviewText         string                            `json:"review_text,omitempty"`
	Risk               string                            `json:"risk,omitempty"`
	Authorization      string                            `json:"authorization,omitempty"`
	DecisionSource     string                            `json:"decision_source,omitempty"`
}

type ReviewRequest struct {
	SessionRef session.SessionRef
	RunID      string
	TurnID     string
	Mode       Mode
	ReviewID   string
	Model      model.LLM
	// Judgment optionally screens the action before Agent review. An incomplete
	// judgment falls through to Model without changing the approval request.
	Judgment judgment.Evaluator
	// ResolveModel lazily supplies the Agent model when review needs it. When
	// present it takes precedence over Model, within the same review context.
	ResolveModel   func(context.Context) (model.LLM, error)
	Approval       *Payload
	RuntimeRequest agentsdk.ApprovalRequest
}

type ReviewResult struct {
	Approved       bool
	Outcome        string
	OptionID       string
	Risk           string
	Authorization  string
	Rationale      string
	DisplayText    string
	DecisionSource string
}

// Reviewer produces an approval assessment. Implementations may return raw
// model output; callers that need an executable decision must pass the result
// through FinalizeReviewResult or use ReviewerAdapter.
type Reviewer interface {
	ReviewApproval(context.Context, ReviewRequest) (ReviewResult, error)
}

// JudgmentResolver optionally selects a typed classifier for approval screening.
// A nil evaluator or resolution error leaves the Agent review path in place.
type JudgmentResolver interface {
	ResolveApprovalJudgment(context.Context, session.SessionRef) (judgment.Evaluator, error)
}

// ConfigurationSnapshotter freezes mutable selection inputs for one review.
// The gateway resolves its Agent model once before screening; model construction
// errors remain deferred until screening actually needs that Agent. No model
// request is issued by this hook, and the returned context retains cancellation.
type ConfigurationSnapshotter interface {
	SnapshotApprovalConfiguration(context.Context, session.SessionRef) (context.Context, error)
}

type ModelResolver interface {
	ResolveApprovalModel(context.Context, session.SessionRef) (model.LLM, error)
}

func PayloadFromRuntimeRequest(req agentsdk.ApprovalRequest) *Payload {
	payload := &Payload{
		ToolCallID: strings.TrimSpace(req.Call.ID),
		ToolName:   strings.TrimSpace(req.Tool.Name),
		Status:     StatusPending,
	}
	if payload.ToolName == "" {
		payload.ToolName = strings.TrimSpace(req.Call.Name)
	}
	if req.Approval != nil {
		payload.Context = session.ClonePermissionContext(req.Approval.Context)
		if payload.Context != nil {
			// Cosmetic remote claims are not part of the canonical review
			// action, its cache identity, or the evidence sent to a reviewer.
			payload.Context.DisplaySubjectHint = nil
		}
		payload.Subject = session.ApprovalSubjectOf(req.Approval)
		if callID := strings.TrimSpace(req.Approval.ToolCall.ID); callID != "" {
			payload.ToolCallID = callID
		}
		if toolName := strings.TrimSpace(req.Approval.ToolCall.Name); toolName != "" {
			payload.ToolName = toolName
		}
		payload.ToolKind = strings.TrimSpace(req.Approval.ToolCall.Kind)
		payload.ToolTitle = strings.TrimSpace(req.Approval.ToolCall.Title)
		payload.ToolStatus = strings.TrimSpace(req.Approval.ToolCall.Status)
		if req.Approval.Context != nil {
			// Independent permissions carry original endpoint evidence, including
			// an optional actual tool call. Do not normalize its identity or text.
			payload.ToolCallID, payload.ToolName = req.Approval.ToolCall.ID, req.Approval.ToolCall.Name
			payload.ToolKind, payload.ToolTitle, payload.ToolStatus = req.Approval.ToolCall.Kind, req.Approval.ToolCall.Title, req.Approval.ToolCall.Status
		}
		payload.RawInput = jsonvalue.CloneMap(req.Approval.ToolCall.RawInput)
		payload.RawOutput = jsonvalue.CloneMap(req.Approval.ToolCall.RawOutput)
		payload.Content = session.CloneProtocolToolCallContent(req.Approval.ToolCall.Content)
		if len(req.Approval.Options) > 0 {
			payload.Options = NormalizeProtocolOptions(req.Approval.Options)
			if req.Approval.Context != nil {
				for i, option := range req.Approval.Options {
					payload.Options[i] = Option{ID: option.ID, Name: option.Name, Kind: option.Kind}
				}
			}
		}
	}
	if len(payload.RawInput) == 0 {
		payload.RawInput = rawInputFromJSONString(string(req.Call.Input))
	}
	payload.Reason = firstNonEmpty(metadataString(req.Metadata, "approval_reason"), rawString(payload.RawInput, "approval_reason"))
	payload.Justification = firstNonEmpty(metadataString(req.Metadata, "justification"), rawString(payload.RawInput, "justification"))
	payload.SandboxPermissions = firstNonEmpty(metadataString(req.Metadata, "sandbox_permissions"), rawString(payload.RawInput, "sandbox_permissions"))
	if payload.ToolName == "" && len(payload.RawInput) == 0 && len(payload.Options) == 0 && payload.Reason == "" {
		return nil
	}
	return payload
}

// DisplayPayloadFromRuntimeRequest creates a separate presentation snapshot.
// Reviewers and execution/resolve must use PayloadFromRuntimeRequest instead.
func DisplayPayloadFromRuntimeRequest(req agentsdk.ApprovalRequest) *Payload {
	payload := PayloadFromRuntimeRequest(req)
	if payload == nil {
		return nil
	}
	if req.Approval != nil {
		payload.Subject = session.ApprovalDisplaySubjectOf(req.Approval)
	}
	redactedInput, sensitive := RedactInputForDisplay(payload.RawInput, req.Tool.InputSchema)
	payload.RawInput = redactedInput
	sensitive = sensitive || (payload.Subject != nil && payload.Subject.SensitiveInput)
	opaqueInput := payload.Subject != nil && payload.Subject.OpaqueInput
	if sensitive || opaqueInput {
		// Titles, output and content may quote the marked arguments. The
		// authoritative Runtime request remains intact, outside this display copy.
		payload.ToolTitle = payload.ToolName
		payload.RawInput = nil
		payload.Reason = sensitiveInputDisplay
		if !sensitive {
			payload.Reason = "[opaque input withheld]"
		}
		payload.Justification = ""
		payload.SandboxPermissions = ""
		payload.RawOutput = nil
		payload.Content = nil
	}
	if !sensitive && !opaqueInput {
		payload.Reason = firstNonEmpty(metadataString(req.Metadata, "approval_reason"), rawString(payload.RawInput, "approval_reason"))
		payload.Justification = firstNonEmpty(metadataString(req.Metadata, "justification"), rawString(payload.RawInput, "justification"))
		payload.SandboxPermissions = firstNonEmpty(metadataString(req.Metadata, "sandbox_permissions"), rawString(payload.RawInput, "sandbox_permissions"))
	}
	if InputFieldSensitive(req.Tool.InputSchema, "approval_reason") {
		payload.Reason = sensitiveInputDisplay
	}
	if InputFieldSensitive(req.Tool.InputSchema, "justification") {
		payload.Justification = sensitiveInputDisplay
	}
	if InputFieldSensitive(req.Tool.InputSchema, "sandbox_permissions") {
		payload.SandboxPermissions = sensitiveInputDisplay
	}
	if payload.Subject == nil {
		payload.Subject = session.ApprovalSubjectOf(ProtocolApprovalFromPayload(payload))
	}
	payload.Subject.SensitiveInput = payload.Subject.SensitiveInput || sensitive
	payload.Subject = session.CloneApprovalSubject(payload.Subject)
	if payload.Context != nil {
		payload.Context.RawSubject, payload.Context.NativeRequestID = nil, nil
		payload.Context.Title = ""
		payload.Context.Description = nil
		if !sensitive && !payload.Subject.OpaqueInput {
			if payload.Subject.Title != nil {
				payload.Context.Title = payload.Subject.Title.Text
			}
			if payload.Subject.Description != nil {
				payload.Context.Description = new(payload.Subject.Description.Text)
			}
		}
	}
	return payload
}

// DisplayReviewText keeps model-produced rationale out of ordinary presentation
// when it could quote explicitly marked request values. The typed verdict stays
// available; private review evidence remains unchanged.
func DisplayReviewText(text string, subject *session.ApprovalSubject) string {
	if subject != nil && subject.SensitiveInput {
		return "Sensitive action details withheld."
	}
	if subject != nil && subject.OpaqueInput {
		return "Opaque action details withheld."
	}
	return text
}

// CloneRuntimeRequest returns an isolated approval request snapshot for a
// Control-owned admission boundary. Runtime producers may reuse their request
// buffers after handing an approval to Control; the action, protocol payload,
// options, and provenance metadata must remain the exact values that entered
// the approval queue.
func CloneRuntimeRequest(in agentsdk.ApprovalRequest) agentsdk.ApprovalRequest {
	out := in
	if in.Origin != nil {
		origin := *in.Origin
		out.Origin = &origin
	}
	out.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	out.Session = session.CloneSession(in.Session)
	out.RunID = strings.TrimSpace(in.RunID)
	out.TurnID = strings.TrimSpace(in.TurnID)
	out.PauseTokenID = strings.TrimSpace(in.PauseTokenID)
	out.OperationTaskID = strings.TrimSpace(in.OperationTaskID)
	out.Tool = tool.CloneDefinition(in.Tool)
	out.Call = tool.CloneCall(in.Call)
	out.ModelStep = tool.CloneModelStepRef(in.ModelStep)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	if in.Approval != nil {
		approval := session.CloneProtocolApproval(*in.Approval)
		out.Approval = &approval
	}
	return out
}

// ProtocolApprovalFromPayload returns the normalized transport-neutral ACP
// permission payload represented by one approval payload.
func ProtocolApprovalFromPayload(payload *Payload) *session.ProtocolApproval {
	if payload == nil {
		return nil
	}
	options := make([]session.ProtocolApprovalOption, 0, len(payload.Options))
	for _, option := range payload.Options {
		options = append(options, session.ProtocolApprovalOption{
			ID: strings.TrimSpace(option.ID), Name: strings.TrimSpace(option.Name), Kind: strings.TrimSpace(option.Kind),
		})
		if payload.Context != nil {
			options[len(options)-1].Name = option.Name
		}
	}
	rawInput := jsonvalue.CloneMap(payload.RawInput)
	rawInput = putRawStringIfMissing(rawInput, "approval_reason", payload.Reason)
	rawInput = putRawStringIfMissing(rawInput, "justification", payload.Justification)
	rawInput = putRawStringIfMissing(rawInput, "sandbox_permissions", payload.SandboxPermissions)
	if payload.Context != nil && payload.ToolCallID == "" && payload.ToolName == "" {
		rawInput = nil
	}
	out := &session.ProtocolApproval{
		Context: session.ClonePermissionContext(payload.Context),
		Subject: session.CloneApprovalSubject(payload.Subject),
		ToolCall: session.ProtocolToolCall{
			ID: strings.TrimSpace(payload.ToolCallID), Name: strings.TrimSpace(payload.ToolName),
			Kind: strings.TrimSpace(payload.ToolKind), Title: strings.TrimSpace(payload.ToolTitle),
			Status:   approvalToolStatus(payload),
			RawInput: rawInput, RawOutput: jsonvalue.CloneMap(payload.RawOutput),
			Content: session.CloneProtocolToolCallContent(payload.Content),
		},
		Options: options,
	}
	if payload.Context != nil {
		out.ToolCall.ID, out.ToolCall.Name = payload.ToolCallID, payload.ToolName
		out.ToolCall.Kind, out.ToolCall.Title, out.ToolCall.Status = payload.ToolKind, payload.ToolTitle, payload.ToolStatus
	}
	return out
}

func approvalToolStatus(payload *Payload) string {
	if payload.Context != nil {
		return payload.ToolStatus
	}
	return firstNonEmpty(strings.TrimSpace(payload.ToolStatus), strings.TrimSpace(string(payload.Status)))
}

func putRawStringIfMissing(values map[string]any, key string, value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	if values == nil {
		values = map[string]any{}
	}
	if _, exists := values[key]; !exists {
		values[key] = value
	}
	return values
}

// NormalizeProtocolOptions converts protocol approval options into the stable
// approval option shape and applies canonical option normalization.
func NormalizeProtocolOptions(options []session.ProtocolApprovalOption) []Option {
	if len(options) == 0 {
		return nil
	}
	out := make([]Option, 0, len(options))
	for _, option := range options {
		out = append(out, Option{
			ID:   option.ID,
			Name: option.Name,
			Kind: option.Kind,
		})
	}
	return NormalizeOptions(out)
}

// NormalizeOptions returns the stable approval option shape used across
// gateway, runtime, and system-managed reviewers.
func NormalizeOptions(options []Option) []Option {
	if len(options) == 0 {
		return nil
	}
	out := make([]Option, 0, len(options))
	for _, option := range options {
		normalized := Option{
			ID:   strings.TrimSpace(option.ID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(option.Kind),
		}
		if normalized.ID == "" && normalized.Name == "" && normalized.Kind == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

// RuntimeResponseFromFinalReview converts an already-finalized review result
// into the runtime approval response shape.
func RuntimeResponseFromFinalReview(result ReviewResult) agentsdk.ApprovalResponse {
	return agentsdk.ApprovalResponse{
		Outcome:    strings.TrimSpace(result.Outcome),
		OptionID:   strings.TrimSpace(result.OptionID),
		Approved:   result.Approved,
		Reason:     strings.TrimSpace(result.Rationale),
		ReviewText: strings.TrimSpace(firstNonEmpty(result.DisplayText, result.Rationale)),
	}
}

// FinalizeReviewResult applies shared option/outcome resolution and fills the
// display text when the reviewer did not provide one.
func FinalizeReviewResult(payload *Payload, result ReviewResult) ReviewResult {
	result = ResolveReviewResult(payload, result)
	if strings.TrimSpace(result.DisplayText) == "" {
		result.DisplayText = FormatReviewText(result.Approved, result.Risk, result.Authorization, result.Rationale)
	}
	return result
}

// ResolveReviewResult applies the shared approval decision precedence. A valid
// selected option is authoritative; when no valid option is selected, the
// review falls back to the approved/outcome decision and existing option
// matching.
func ResolveReviewResult(payload *Payload, result ReviewResult) ReviewResult {
	options := []Option(nil)
	if payload != nil {
		options = payload.Options
	}
	optionID := strings.TrimSpace(result.OptionID)
	if optionID != "" {
		if option, ok := lookupOptionByID(options, optionID); ok {
			result.OptionID = strings.TrimSpace(option.ID)
			result.Approved = optionMatchesDecision(option, true)
			result.Outcome = string(StatusSelected)
			return result
		}
	}
	result.Approved = approvedFromReviewOutcome(result)
	result.OptionID = optionIDForDecision(options, result.Approved)
	if result.OptionID != "" {
		result.Outcome = string(StatusSelected)
		return result
	}
	if strings.TrimSpace(result.Outcome) == "" {
		if result.Approved {
			result.Outcome = string(StatusApproved)
		} else {
			result.Outcome = string(StatusRejected)
		}
	}
	return result
}

func ReviewID(prefix string, payload *Payload) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "approval-review"
	}
	if payload == nil {
		return prefix
	}
	if callID := strings.TrimSpace(payload.ToolCallID); callID != "" {
		return prefix + "-" + callID
	}
	return prefix
}

func ClonePayload(in *Payload) *Payload {
	if in == nil {
		return nil
	}
	out := *in
	out.Context = session.ClonePermissionContext(in.Context)
	out.Subject = session.CloneApprovalSubject(in.Subject)
	out.RawInput = jsonvalue.CloneMap(in.RawInput)
	out.RawOutput = jsonvalue.CloneMap(in.RawOutput)
	out.Content = session.CloneProtocolToolCallContent(in.Content)
	if len(in.Options) > 0 {
		out.Options = append([]Option(nil), in.Options...)
	}
	return &out
}

func FormatReviewText(approved bool, risk string, authorization string, rationale string) string {
	verdict := "denied"
	if approved {
		verdict = "approved"
	}
	risk = firstNonEmpty(strings.TrimSpace(risk), "unknown")
	authorization = firstNonEmpty(strings.TrimSpace(authorization), "unknown")
	rationale = firstNonEmpty(strings.TrimSpace(rationale), "no rationale provided")
	return fmt.Sprintf("Automatic approval review %s (risk: %s, authorization: %s): %s", verdict, risk, authorization, rationale)
}

func ReviewTerminalStatus(result ReviewResult) ReviewStatus {
	if result.Approved {
		return ReviewStatusApproved
	}
	return ReviewStatusDenied
}

// ReviewErrorOutcome maps reviewer execution errors to terminal review status
// and user-visible rationale text.
func ReviewErrorOutcome(err error) (ReviewStatus, string, bool) {
	if err == nil {
		return "", "", false
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReviewStatusTimedOut, "automatic approval review timed out: " + err.Error(), true
	case errors.Is(err, context.Canceled):
		return ReviewStatusFailed, "automatic approval review cancelled", true
	default:
		return ReviewStatusFailed, "automatic approval review failed: " + err.Error(), true
	}
}

func optionIDForDecision(options []Option, approved bool) string {
	for _, option := range options {
		if optionMatchesDecision(option, approved) {
			return strings.TrimSpace(option.ID)
		}
	}
	return ""
}

func optionMatchesDecision(option Option, approved bool) bool {
	value := strings.ToLower(strings.TrimSpace(strings.Join([]string{option.Kind, option.ID, option.Name}, " ")))
	if approved {
		return strings.HasPrefix(value, "allow") || strings.Contains(value, " allow")
	}
	return strings.HasPrefix(value, "reject") || strings.Contains(value, " reject") ||
		strings.HasPrefix(value, "deny") || strings.Contains(value, " deny")
}

func lookupOptionByID(options []Option, optionID string) (Option, bool) {
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return Option{}, false
	}
	for _, option := range options {
		if strings.TrimSpace(option.ID) == optionID {
			return option, true
		}
	}
	return Option{}, false
}

func approvedFromReviewOutcome(result ReviewResult) bool {
	switch strings.ToLower(strings.TrimSpace(result.Outcome)) {
	case "allow", "approved":
		return true
	case "deny", "denied", "reject", "rejected":
		return false
	default:
		return result.Approved
	}
}

func rawInputFromJSONString(text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil
	}
	return out
}

func metadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	text, ok := meta[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func rawString(raw map[string]any, key string) string {
	if len(raw) == 0 {
		return ""
	}
	text, ok := raw[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
