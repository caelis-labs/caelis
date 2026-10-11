package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	approvalStatusApproved = "approved"
	approvalStatusRejected = "rejected"
	approvalStatusSelected = "selected"
)

type approvalPayload struct {
	Subject            *session.ApprovalSubject
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
	subject := (*session.ApprovalSubject)(nil)
	if req != nil {
		subject = session.CloneApprovalSubject(req.Subject)
	}
	if subject == nil {
		subject = session.ApprovalSubjectOf(nil)
	}
	msg := PromptRequestMsg{
		Title: "Approval Required", Prompt: approvalFieldDisplay(subject.Tool), Response: response,
	}
	if subject.Title != nil && (subject.Title.State == session.ApprovalFieldKnown || subject.Title.State == session.ApprovalFieldTruncated) {
		msg.Title = approvalFieldDisplay(*subject.Title)
	}
	if subject.Description != nil {
		msg.Details = append(msg.Details, PromptDetail{Label: "Description", Value: approvalFieldDisplay(*subject.Description)})
	}
	if subject.Reported {
		msg.Details = append(msg.Details, PromptDetail{Label: "Source", Value: "Context reported by the remote agent.", Tone: PromptToneWarning})
	}
	msg.Details = append(msg.Details, PromptDetail{Label: "Operation", Value: approvalFieldDisplay(subject.Operation)})
	for i, target := range subject.Targets {
		label := "Target"
		if subject.Kind == "command" {
			if i == 0 {
				label = "Command"
			} else {
				label = "Working directory"
			}
		}
		msg.Details = append(msg.Details, PromptDetail{Label: label, Value: approvalFieldDisplay(target), Emphasis: true})
	}
	msg.Details = append(msg.Details, PromptDetail{Label: "Effect", Value: approvalFieldDisplay(subject.Effect)}, PromptDetail{Label: "Scope", Value: approvalFieldDisplay(subject.Scope)})
	incomplete := approvalSubjectIncomplete(subject)
	if subject.SensitiveInput {
		msg.Details = append(msg.Details, PromptDetail{Label: "Sensitive input", Value: "Sensitive values are hidden."})
	}
	if incomplete {
		msg.Details = append(msg.Details, PromptDetail{Label: "Context", Value: "Incomplete or opaque; inspect the original request before granting access.", Tone: PromptToneWarning})
	}
	if req != nil && !subject.SensitiveInput && !subject.OpaqueInput {
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
	}
	if req != nil {
		msg.Choices = make([]PromptChoice, 0, len(req.Options))
		for i, opt := range req.Options {
			value := strings.TrimSpace(opt.ID)
			if value == "" {
				continue
			}
			msg.Choices = append(msg.Choices, PromptChoice{
				Label:  firstNonEmpty(strings.TrimSpace(opt.Name), value),
				Value:  value,
				Detail: approvalChoiceDetail(subject, opt),
			})
			if i == 0 && msg.DefaultChoice == "" {
				msg.DefaultChoice = value
			}
		}
		if incomplete {
			msg.DefaultChoice = ""
			for _, opt := range req.Options {
				if opt.Kind == "reject_once" || opt.Kind == "reject_always" {
					msg.DefaultChoice = opt.ID
					break
				}
			}
		}
		if defaultChoice := approvalDefaultChoiceLabel(msg.Choices, msg.DefaultChoice); defaultChoice != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Default", Value: defaultChoice})
		}
	}
	msg.AllowFreeformInput = len(msg.Choices) == 0
	return msg
}

func approvalFieldDisplay(field session.ApprovalField) string {
	if field.Sensitive {
		return "[sensitive field supplied]"
	}
	switch field.State {
	case session.ApprovalFieldKnown:
		return field.Text
	case session.ApprovalFieldTruncated:
		return field.Text + " [truncated]"
	case session.ApprovalFieldOpaque:
		return "[opaque]"
	default:
		return "[unavailable]"
	}
}

func approvalSubjectIncomplete(subject *session.ApprovalSubject) bool {
	if subject == nil || subject.Kind == "opaque" || subject.Reported || subject.TargetsTruncated || subject.GrantsTruncated {
		return true
	}
	fields := append([]session.ApprovalField{subject.Tool, subject.Operation, subject.Effect, subject.Scope}, subject.Targets...)
	for _, grant := range subject.Grants {
		fields = append(fields, grant.Scope)
	}
	for _, field := range fields {
		if field.State != session.ApprovalFieldKnown || field.Sensitive {
			return true
		}
	}
	return false
}

func approvalChoiceDetail(subject *session.ApprovalSubject, opt approvalOption) string {
	for _, grant := range subject.Grants {
		if grant.OptionID == opt.ID {
			return opt.Kind + "; scope: " + approvalFieldDisplay(grant.Scope)
		}
	}
	return opt.Kind + "; scope: [unavailable]"
}

// Legacy review preview is limited to completed decisions predating typed
// subjects; new permission prompts use only their common Subject.
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

func approvalToolDisplayLabel(toolName string) string {
	semanticName := toolName
	switch semanticName {
	case "":
		return ""
	case "UNKNOWN":
		return ""
	case surfaceToolRunCommand:
		return "Ran"
	case surfaceToolSpawn:
		return "Spawned"
	case surfaceToolTask:
		return "Task"
	case surfaceToolRead:
		return "Read"
	case surfaceToolViewImage:
		return "Viewed"
	case surfaceToolGlob:
		return "Glob"
	case surfaceToolGrep:
		return "Search"
	case surfaceToolWrite, surfaceToolPatch:
		return "Edit"
	default:
		return strings.TrimSpace(toolName)
	}
}

func approvalRiskLabel(req *approvalPayload) string {
	if req == nil {
		return ""
	}
	return strings.TrimSpace(req.Risk)
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
