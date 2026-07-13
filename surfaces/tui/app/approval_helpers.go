package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const (
	approvalStatusApproved = "approved"
	approvalStatusRejected = "rejected"
	approvalStatusSelected = "selected"

	approvalReviewStatusApproved = "approved"
	approvalReviewStatusDenied   = "denied"
	approvalReviewStatusTimedOut = "timed_out"
	approvalReviewStatusFailed   = "failed"
)

type approvalPayload struct {
	RequestID          eventstream.ApprovalRequestID
	ToolCallID         string
	ToolName           string
	RawInput           map[string]any
	Reason             string
	Justification      string
	SandboxPermissions string
	Status             string
	Options            []approvalOption
	ReviewID           string
	ReviewStatus       string
	ReviewText         string
	Risk               string
	Authorization      string
	DecisionSource     string
}

type approvalOption struct {
	ID   string
	Name string
	Kind string
}

func approvalToPromptRequest(req *approvalPayload, response chan PromptResponse) PromptRequestMsg {
	toolName, command := approvalToolSummary(req)
	msg := PromptRequestMsg{
		Title:         "Approval Required",
		Prompt:        firstNonEmpty(toolName, "approval required"),
		Response:      response,
		Choices:       nil,
		DefaultChoice: "",
	}
	if command != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Action", Value: approvalActionLabel(req)})
		msg.Details = append(msg.Details, PromptDetail{Label: "Command", Value: command, Emphasis: true})
	} else if action := approvalActionLabel(req); action != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Action", Value: action})
	}
	if req != nil {
		if risk := approvalRiskLabel(req); risk != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Risk", Value: risk, Emphasis: true})
		}
		if value := strings.TrimSpace(req.Reason); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Reason", Value: value})
		}
		if value := strings.TrimSpace(req.Justification); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Justification", Value: value})
		}
		if value := strings.TrimSpace(req.SandboxPermissions); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Sandbox", Value: value})
		}
		msg.Choices = make([]PromptChoice, 0, len(req.Options))
		for i, opt := range req.Options {
			value := strings.TrimSpace(opt.ID)
			if value == "" {
				continue
			}
			msg.Choices = append(msg.Choices, PromptChoice{
				Label:  firstNonEmpty(strings.TrimSpace(opt.Name), value),
				Value:  value,
				Detail: strings.TrimSpace(opt.Kind),
			})
			if i == 0 && msg.DefaultChoice == "" {
				msg.DefaultChoice = value
			}
		}
		if defaultChoice := approvalDefaultChoiceLabel(msg.Choices, msg.DefaultChoice); defaultChoice != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Default", Value: defaultChoice})
		}
	}
	msg.AllowFreeformInput = len(msg.Choices) == 0
	return msg
}

func approvalToolSummary(req *approvalPayload) (string, string) {
	if req == nil {
		return "", ""
	}
	return approvalToolDisplayLabel(req.ToolName), approvalCommandPreview(req.RawInput)
}

func approvalReviewPendingHint(toolName string, raw map[string]any, maxWidth int) string {
	detail := firstNonEmpty(approvalKnownInputPreview(raw), approvalReviewToolName(toolName), approvalCommandPreview(raw), "approval request")
	text := compactString("Reviewing approval request: "+detail, 0)
	if maxWidth > 0 {
		text = truncateTailDisplay(text, maxWidth)
	}
	return strings.TrimSpace(text)
}

func approvalKnownInputPreview(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}
	for _, key := range []string{"command", "cmd", "file_path", "path", "query", "url", "pattern", "text", "prompt", "input"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return compactString(strings.TrimSpace(key)+": "+value, 240)
		}
	}
	return ""
}

func approvalReviewToolName(toolName string) string {
	return approvalToolDisplayLabel(toolName)
}

func approvalToolDisplayLabel(toolName string) string {
	semanticName := display.SemanticToolName(toolName, toolName)
	switch names.CanonicalOrSelf(semanticName) {
	case "":
		return ""
	case "UNKNOWN":
		return ""
	case names.RunCommand:
		return "Ran"
	case names.Spawn:
		return "Spawned"
	case names.Task:
		return "Task"
	case names.Read:
		return "Read"
	case names.List:
		return "List"
	case names.Glob:
		return "Glob"
	case names.Grep, "RG", "FIND":
		return "Search"
	case names.Write:
		return "Wrote"
	case names.Patch:
		return "Patched"
	default:
		return strings.TrimSpace(toolName)
	}
}

func approvalActionLabel(req *approvalPayload) string {
	if req == nil {
		return ""
	}
	if info, ok := names.Lookup(req.ToolName); ok {
		switch info.Kind {
		case names.KindEdit:
			return "write"
		case names.KindExecute:
			return "execute"
		case names.KindRead, names.KindSearch:
			return "read"
		}
	}
	tool := strings.TrimSpace(req.ToolName)
	switch {
	case strings.TrimSpace(req.SandboxPermissions) != "":
		return "permission change"
	case tool != "":
		return tool
	default:
		return "approval"
	}
}

func approvalRiskLabel(req *approvalPayload) string {
	if req == nil {
		return ""
	}
	if value := strings.TrimSpace(req.Risk); value != "" {
		return value
	}
	parts := []string{}
	if strings.Contains(strings.ToLower(req.SandboxPermissions), "host") {
		parts = append(parts, "host execution")
	}
	return strings.Join(parts, "; ")
}

func approvalDefaultChoiceLabel(choices []PromptChoice, selected string) string {
	selected = strings.TrimSpace(selected)
	if selected == "" && len(choices) > 0 {
		selected = choices[0].Value
	}
	for _, choice := range choices {
		if strings.TrimSpace(choice.Value) == selected {
			return firstNonEmpty(strings.TrimSpace(choice.Label), selected)
		}
	}
	return selected
}

func compactString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if maxLen > 0 {
		runes := []rune(s)
		if len(runes) > maxLen {
			if maxLen <= 3 {
				return string(runes[:maxLen])
			}
			return string(runes[:maxLen-3]) + "..."
		}
	}
	return s
}
