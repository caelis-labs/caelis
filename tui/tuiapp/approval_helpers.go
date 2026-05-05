package tuiapp

import (
	"strings"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
)

func approvalToPromptRequest(req *appgateway.ApprovalPayload, response chan PromptResponse) PromptRequestMsg {
	toolName, command := approvalToolSummary(req)
	msg := PromptRequestMsg{
		Title:         "Approval Required",
		Prompt:        firstNonEmpty(toolName, "approval required"),
		Response:      response,
		Choices:       nil,
		DefaultChoice: "",
	}
	if command != "" {
		msg.Details = append(msg.Details, PromptDetail{Label: "Command", Value: command, Emphasis: true})
	}
	if req != nil {
		if value := strings.TrimSpace(req.Reason); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Reason", Value: value})
		}
		if value := strings.TrimSpace(req.Justification); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Justification", Value: value})
		}
		if value := strings.TrimSpace(req.SandboxPermissions); value != "" {
			msg.Details = append(msg.Details, PromptDetail{Label: "Sandbox", Value: value})
		}
		if len(req.PrefixRule) > 0 {
			msg.Details = append(msg.Details, PromptDetail{Label: "Prefix rule", Value: strings.Join(req.PrefixRule, " ")})
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
	}
	msg.AllowFreeformInput = len(msg.Choices) == 0
	return msg
}

func approvalToolSummary(req *appgateway.ApprovalPayload) (string, string) {
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
