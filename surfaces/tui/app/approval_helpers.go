package tuiapp

import (
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/kernel"
)

func approvalToPromptRequest(req *kernel.ApprovalPayload, response chan PromptResponse) PromptRequestMsg {
	return approvalPromptRequest(approvalPromptDataFromKernel(req), response)
}

func approvalItemToPromptRequest(req *appviewmodel.ApprovalItem, response chan PromptResponse) PromptRequestMsg {
	return approvalPromptRequest(approvalPromptDataFromItem(req), response)
}

type approvalPromptData struct {
	ToolName           string
	Command            string
	Reason             string
	Justification      string
	SandboxPermissions string
	Risk               string
	Options            []approvalPromptOption
}

type approvalPromptOption struct {
	ID   string
	Name string
	Kind string
}

func approvalPromptRequest(data approvalPromptData, response chan PromptResponse) PromptRequestMsg {
	toolName, command := approvalToolSummary(data)
	msg := PromptRequestMsg{
		Title:         "Approval Required",
		Prompt:        firstNonEmpty(toolName, "approval required"),
		Response:      response,
		Choices:       nil,
		DefaultChoice: "",
	}
	if command != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Action", Value: approvalActionLabel(data)})
		msg.Details = append(msg.Details, PromptDetail{Label: "Command", Value: command, Emphasis: true})
	} else if action := approvalActionLabel(data); action != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Action", Value: action})
	}
	if risk := approvalRiskLabel(data); risk != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Risk", Value: risk, Emphasis: true})
	}
	if value := strings.TrimSpace(data.Reason); value != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Reason", Value: value})
	}
	if value := strings.TrimSpace(data.Justification); value != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Justification", Value: value})
	}
	if value := strings.TrimSpace(data.SandboxPermissions); value != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Sandbox", Value: value})
	}
	msg.Choices = make([]PromptChoice, 0, len(data.Options))
	for i, opt := range data.Options {
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
	msg.AllowFreeformInput = len(msg.Choices) == 0
	return msg
}

func approvalPromptDataFromKernel(req *kernel.ApprovalPayload) approvalPromptData {
	if req == nil {
		return approvalPromptData{}
	}
	data := approvalPromptData{
		ToolName:           strings.TrimSpace(req.ToolName),
		Command:            approvalCommandPreview(req.RawInput),
		Reason:             strings.TrimSpace(req.Reason),
		Justification:      strings.TrimSpace(req.Justification),
		SandboxPermissions: strings.TrimSpace(req.SandboxPermissions),
		Risk:               strings.TrimSpace(req.Risk),
		Options:            make([]approvalPromptOption, 0, len(req.Options)),
	}
	for _, opt := range req.Options {
		data.Options = append(data.Options, approvalPromptOption{
			ID:   strings.TrimSpace(opt.ID),
			Name: strings.TrimSpace(opt.Name),
			Kind: strings.TrimSpace(opt.Kind),
		})
	}
	return data
}

func approvalPromptDataFromItem(req *appviewmodel.ApprovalItem) approvalPromptData {
	if req == nil {
		return approvalPromptData{}
	}
	data := approvalPromptData{
		ToolName: strings.TrimSpace(req.Tool),
		Command:  strings.TrimSpace(req.Command),
		Reason:   strings.TrimSpace(req.Reason),
		Options:  make([]approvalPromptOption, 0, len(req.Options)),
	}
	for _, opt := range req.Options {
		data.Options = append(data.Options, approvalPromptOption{
			ID:   strings.TrimSpace(opt.ID),
			Name: strings.TrimSpace(opt.Name),
			Kind: strings.TrimSpace(opt.Kind),
		})
	}
	return data
}

func approvalToolSummary(data approvalPromptData) (string, string) {
	return strings.TrimSpace(data.ToolName), strings.TrimSpace(data.Command)
}

func approvalActionLabel(data approvalPromptData) string {
	tool := strings.ToLower(strings.TrimSpace(data.ToolName))
	switch {
	case strings.Contains(tool, "write"), strings.Contains(tool, "patch"):
		return "write"
	case strings.Contains(tool, "run_command"), strings.Contains(tool, "spawn"):
		return "execute"
	case strings.Contains(tool, "read"), strings.Contains(tool, "search"):
		return "read"
	case strings.TrimSpace(data.SandboxPermissions) != "":
		return "permission change"
	case tool != "":
		return tool
	default:
		return "approval"
	}
}

func approvalRiskLabel(data approvalPromptData) string {
	if value := strings.TrimSpace(data.Risk); value != "" {
		return value
	}
	parts := []string{}
	if strings.Contains(strings.ToLower(data.SandboxPermissions), "host") {
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
