package tuiapp

import (
	"strings"
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
	return strings.TrimSpace(req.ToolName), approvalCommandPreview(req.RawInput)
}

func approvalActionLabel(req *approvalPayload) string {
	if req == nil {
		return ""
	}
	tool := strings.ToLower(strings.TrimSpace(req.ToolName))
	switch {
	case strings.Contains(tool, "write"), strings.Contains(tool, "patch"):
		return "write"
	case strings.Contains(tool, "run_command"), strings.Contains(tool, "spawn"):
		return "execute"
	case strings.Contains(tool, "read"), strings.Contains(tool, "search"):
		return "read"
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
