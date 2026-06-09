package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
	ToolCallID         string         `json:"tool_call_id,omitempty"`
	ToolName           string         `json:"tool_name,omitempty"`
	RawInput           map[string]any `json:"raw_input,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	Justification      string         `json:"justification,omitempty"`
	SandboxPermissions string         `json:"sandbox_permissions,omitempty"`
	Status             Status         `json:"status,omitempty"`
	Options            []Option       `json:"options,omitempty"`
	ReviewID           string         `json:"review_id,omitempty"`
	ReviewStatus       ReviewStatus   `json:"review_status,omitempty"`
	ReviewText         string         `json:"review_text,omitempty"`
	Risk               string         `json:"risk,omitempty"`
	Authorization      string         `json:"authorization,omitempty"`
	DecisionSource     string         `json:"decision_source,omitempty"`
}

type UsageSnapshot struct {
	PromptTokens      int `json:"prompt_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CompletionTokens  int `json:"completion_tokens,omitempty"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
}

type ReviewRequest struct {
	SessionRef     session.SessionRef
	RunID          string
	TurnID         string
	Mode           Mode
	ReviewID       string
	Model          model.LLM
	Approval       *Payload
	RuntimeRequest agent.ApprovalRequest
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
	Usage          *UsageSnapshot
	Invocation     *session.EventInvocation
}

type Reviewer interface {
	ReviewApproval(context.Context, ReviewRequest) (ReviewResult, error)
}

type ModelResolver interface {
	ResolveApprovalModel(context.Context, session.SessionRef) (model.LLM, error)
}

func PayloadFromRuntimeRequest(req agent.ApprovalRequest) *Payload {
	payload := &Payload{
		ToolCallID: strings.TrimSpace(req.Call.ID),
		ToolName:   strings.TrimSpace(req.Tool.Name),
		Status:     StatusPending,
	}
	if payload.ToolName == "" {
		payload.ToolName = strings.TrimSpace(req.Call.Name)
	}
	if req.Approval != nil {
		if callID := strings.TrimSpace(req.Approval.ToolCall.ID); callID != "" {
			payload.ToolCallID = callID
		}
		if toolName := strings.TrimSpace(req.Approval.ToolCall.Name); toolName != "" {
			payload.ToolName = toolName
		}
		payload.RawInput = maps.Clone(req.Approval.ToolCall.RawInput)
		if len(req.Approval.Options) > 0 {
			payload.Options = make([]Option, 0, len(req.Approval.Options))
			for _, option := range req.Approval.Options {
				payload.Options = append(payload.Options, Option{
					ID:   strings.TrimSpace(option.ID),
					Name: strings.TrimSpace(option.Name),
					Kind: strings.TrimSpace(option.Kind),
				})
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

func RuntimeResponseFromReview(payload *Payload, result ReviewResult) agent.ApprovalResponse {
	optionID := strings.TrimSpace(result.OptionID)
	if optionID == "" && payload != nil {
		optionID = optionIDForDecision(payload.Options, result.Approved)
	}
	outcome := strings.TrimSpace(result.Outcome)
	if optionID != "" {
		outcome = string(StatusSelected)
	} else if outcome == "" {
		if result.Approved {
			outcome = string(StatusApproved)
		} else {
			outcome = string(StatusRejected)
		}
	}
	return agent.ApprovalResponse{
		Outcome:    outcome,
		OptionID:   optionID,
		Approved:   result.Approved,
		Reason:     strings.TrimSpace(result.Rationale),
		ReviewText: strings.TrimSpace(firstNonEmpty(result.DisplayText, result.Rationale)),
	}
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
	out.RawInput = maps.Clone(in.RawInput)
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

func metadataMap(meta map[string]any, key string) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	return anyMap(meta[key])
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

func rawMap(raw map[string]any, key string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	return anyMap(raw[key])
}

func anyMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return maps.Clone(typed)
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmptyMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return maps.Clone(value)
		}
	}
	return nil
}
