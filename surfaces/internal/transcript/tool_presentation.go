package transcript

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

// ToolPresentation is the surface-owned display interpretation of one ACP
// tool identity. Name remains an exact runtime Definition.Name when available;
// Kind and Title are the standard ACP presentation fields and never become a
// substitute semantic identity.
type ToolPresentation struct {
	Name            string
	Kind            string
	Title           string
	DisplayName     string
	ExplorationVerb string
	TitleAsLabel    bool
}

// ResolveToolPresentation derives presentation-only behavior from standard ACP
// fields, with exact built-in names providing optional label enrichment. The
// result is ephemeral surface state, not a runtime or protocol authority.
func ResolveToolPresentation(name string, kind string, title string) ToolPresentation {
	resolved := ToolPresentation{
		Name:  strings.TrimSpace(name),
		Kind:  strings.ToLower(strings.TrimSpace(kind)),
		Title: strings.TrimSpace(title),
	}
	resolved.ExplorationVerb = toolExplorationVerb(resolved.Name, resolved.Kind)
	if resolved.Name != "" {
		resolved.DisplayName = resolved.Name
		return resolved
	}
	if resolved.Kind == "other" && resolved.Title != "" {
		resolved.DisplayName = resolved.Title
		resolved.TitleAsLabel = true
		return resolved
	}
	if label := standardToolKindLabel(resolved.Kind); label != "" {
		resolved.DisplayName = label
		return resolved
	}
	if resolved.Title != "" {
		resolved.DisplayName = resolved.Title
		resolved.TitleAsLabel = true
		return resolved
	}
	resolved.DisplayName = "Tool"
	return resolved
}

// ToolIsExploration reports whether a tool belongs in the shared compact
// exploration presentation. Standard ACP kind is sufficient; an exact built-in
// name only refines the verb.
func ToolIsExploration(name string, kind string) bool {
	return toolExplorationVerb(strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(kind))) != ""
}

func toolExplorationVerb(name string, kind string) string {
	switch kind {
	case "read":
		switch name {
		case filesystem.ViewImageToolName:
			return "View"
		case filesystem.GlobToolName:
			return "Glob"
		case skilltool.ToolName:
			return "Skill"
		default:
			return "Read"
		}
	case "search":
		switch name {
		case filesystem.GlobToolName:
			return "Glob"
		case web.FetchToolName:
			return "Fetch"
		}
		return "Search"
	case "fetch":
		return "Fetch"
	case "":
		switch name {
		case filesystem.ReadToolName:
			return "Read"
		case filesystem.ViewImageToolName:
			return "View"
		case filesystem.GlobToolName:
			return "Glob"
		case filesystem.SearchToolName, web.SearchToolName:
			return "Search"
		case skilltool.ToolName:
			return "Skill"
		case web.FetchToolName:
			return "Fetch"
		default:
			return ""
		}
	default:
		return ""
	}
}

func standardToolKindLabel(kind string) string {
	switch kind {
	case "read":
		return "Read"
	case "edit":
		return "Edit"
	case "delete":
		return "Delete"
	case "move":
		return "Move"
	case "search":
		return "Search"
	case "execute":
		return "Execute"
	case "think":
		return "Think"
	case "fetch":
		return "Fetch"
	case "switch_mode":
		return "Switch mode"
	case "other":
		return "Tool"
	default:
		return ""
	}
}

func transcriptToolIsPlan(name string) bool {
	return name == plan.ToolName
}

func transcriptToolIsTerminalPanel(name string) bool {
	switch name {
	case shell.RunCommandToolName, spawn.ToolName:
		return true
	default:
		return false
	}
}
