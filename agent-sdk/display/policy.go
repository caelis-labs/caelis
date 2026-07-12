package display

import (
	"strings"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindSearch  = "search"
	ToolKindExecute = "execute"
	ToolKindOther   = "other"
)

func SemanticToolName(name string, kind string) string {
	name = strings.TrimSpace(name)
	if info, ok := names.Lookup(name); ok {
		return info.Name
	}
	return name
}

func SummarizeToolCallTitle(name string, args map[string]any) string {
	info, known := names.Lookup(name)
	name = SemanticToolName(name, "")
	if !known {
		return name
	}
	switch info.TitleStyle {
	case names.TitlePath:
		if path := MapString(args, "path"); strings.TrimSpace(path) != "" {
			return strings.TrimSpace(name + " " + path)
		}
	case names.TitleSkill:
		if skillName := MapString(args, "name"); strings.TrimSpace(skillName) != "" {
			return strings.TrimSpace(name + " " + skillName)
		}
	case names.TitleQuery:
		if query := MapString(args, "query"); strings.TrimSpace(query) != "" {
			return strings.TrimSpace(name + " " + query)
		}
	case names.TitleURL:
		if url := MapString(args, "url"); strings.TrimSpace(url) != "" {
			return strings.TrimSpace(name + " " + url)
		}
	case names.TitleCommandAction:
		if command := MapString(args, "command"); strings.TrimSpace(command) != "" {
			return strings.TrimSpace(name + " " + command)
		}
		if action := MapString(args, "action"); strings.TrimSpace(action) != "" {
			if taskID := MapString(args, "task_id"); strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(name + " " + action + " " + taskID)
			}
			return strings.TrimSpace(name + " " + action)
		}
	case names.TitleSpawn:
		if display := SpawnFullDisplayArgs(args); strings.TrimSpace(display) != "" {
			return strings.TrimSpace(name + " " + display)
		}
	}
	return name
}

func ToolKindForName(name string) string {
	info, ok := names.Lookup(name)
	if !ok {
		return ToolKindOther
	}
	switch info.Kind {
	case names.KindRead:
		return ToolKindRead
	case names.KindEdit:
		return ToolKindEdit
	case names.KindSearch:
		return ToolKindSearch
	case names.KindExecute:
		return ToolKindExecute
	default:
		return ToolKindOther
	}
}
