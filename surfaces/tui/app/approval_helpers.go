package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/kernel"
)

func approvalToPromptRequest(req *kernel.ApprovalPayload, response chan PromptResponse) PromptRequestMsg {
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
		if value := approvalPermissionsPreview(req.AdditionalPermissions); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Permissions", Value: value})
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

func approvalToolSummary(req *kernel.ApprovalPayload) (string, string) {
	if req == nil {
		return "", ""
	}
	return strings.TrimSpace(req.ToolName), approvalCommandPreview(req.RawInput)
}

func approvalPermissionsPreview(value map[string]any) string {
	if len(value) == 0 {
		return ""
	}
	parts := []string{}
	if network, ok := value["network"].(map[string]any); ok {
		if enabled, ok := network["enabled"].(bool); ok && enabled {
			parts = append(parts, "network: enabled")
		}
	}
	if fileSystem, ok := value["file_system"].(map[string]any); ok {
		if paths := compactStringList(fileSystem["read"], 3); paths != "" {
			parts = append(parts, "read: "+paths)
		}
		if paths := compactStringList(fileSystem["write"], 3); paths != "" {
			parts = append(parts, "write: "+paths)
		}
	}
	return compactString(strings.Join(parts, "; "), 240)
}

func approvalActionLabel(req *kernel.ApprovalPayload) string {
	if req == nil {
		return ""
	}
	tool := strings.ToLower(strings.TrimSpace(req.ToolName))
	switch {
	case strings.Contains(tool, "write"), strings.Contains(tool, "patch"):
		return "write"
	case strings.Contains(tool, "bash"), strings.Contains(tool, "spawn"):
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

func approvalRiskLabel(req *kernel.ApprovalPayload) string {
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
	if value := approvalPermissionsPreview(req.AdditionalPermissions); value != "" {
		parts = append(parts, value)
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

func compactStringList(value any, limit int) string {
	values := approvalStringList(value)
	if len(values) == 0 {
		return ""
	}
	if limit > 0 && len(values) > limit {
		values = append(append([]string(nil), values[:limit]...), "...")
	}
	return strings.Join(values, ", ")
}

func approvalStringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	}
	return nil
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
