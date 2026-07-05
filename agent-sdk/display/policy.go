package display

import "strings"

const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindSearch  = "search"
	ToolKindExecute = "execute"
	ToolKindOther   = "other"
)

func SemanticToolName(name string, kind string) string {
	name = strings.TrimSpace(name)
	switch strings.ToUpper(name) {
	case "RUN_COMMAND", "SPAWN", "TASK", "SKILL", "READ", "LIST", "GLOB", "SEARCH", "WEB_SEARCH", "WEB_FETCH", "RG", "FIND", "WRITE", "PATCH":
		return strings.ToUpper(name)
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "execute":
		return "RUN_COMMAND"
	case "read":
		return "READ"
	case "search", "fetch":
		return "SEARCH"
	case "edit", "delete", "move":
		return "PATCH"
	default:
		return name
	}
}

func SummarizeToolCallTitle(name string, args map[string]any) string {
	name = strings.TrimSpace(strings.ToUpper(name))
	switch name {
	case "READ", "WRITE", "PATCH", "SEARCH", "LIST", "GLOB":
		if path := MapString(args, "path"); strings.TrimSpace(path) != "" {
			return strings.TrimSpace(name + " " + path)
		}
	case "SKILL":
		if skillName := MapString(args, "name"); strings.TrimSpace(skillName) != "" {
			return strings.TrimSpace(name + " " + skillName)
		}
	case "WEB_SEARCH":
		if query := MapString(args, "query"); strings.TrimSpace(query) != "" {
			return strings.TrimSpace(name + " " + query)
		}
	case "WEB_FETCH":
		if url := MapString(args, "url"); strings.TrimSpace(url) != "" {
			return strings.TrimSpace(name + " " + url)
		}
	case "RUN_COMMAND", "TASK":
		if command := MapString(args, "command"); strings.TrimSpace(command) != "" {
			return strings.TrimSpace(name + " " + command)
		}
		if action := MapString(args, "action"); strings.TrimSpace(action) != "" {
			if taskID := MapString(args, "task_id"); strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(name + " " + action + " " + taskID)
			}
			return strings.TrimSpace(name + " " + action)
		}
	case "SPAWN":
		if display := SpawnFullDisplayArgs(args); strings.TrimSpace(display) != "" {
			return strings.TrimSpace(name + " " + display)
		}
	}
	return name
}

func ToolKindForName(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ", "SKILL":
		return ToolKindRead
	case "WRITE", "PATCH":
		return ToolKindEdit
	case "SEARCH", "GLOB", "LIST", "WEB_SEARCH", "WEB_FETCH":
		return ToolKindSearch
	case "RUN_COMMAND", "SPAWN", "TASK":
		return ToolKindExecute
	default:
		return ToolKindOther
	}
}
