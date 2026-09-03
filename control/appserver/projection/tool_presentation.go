package projection

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/memorytool"
)

const (
	projectedToolKindRead    = "read"
	projectedToolKindEdit    = "edit"
	projectedToolKindSearch  = "search"
	projectedToolKindExecute = "execute"
	projectedToolKindOther   = "other"
)

type projectedToolTitleStyle uint8

const (
	projectedTitleNone projectedToolTitleStyle = iota
	projectedTitlePath
	projectedTitleSkill
	projectedTitleQuery
	projectedTitleURL
	projectedTitleCommandAction
	projectedTitleSpawn
	projectedTitleMessage
	projectedTitleMemoryUpdate
	projectedTitleSearchQuery
)

type projectedToolResultStyle uint8

const (
	projectedResultGeneric projectedToolResultStyle = iota
	projectedResultRead
	projectedResultGlob
	projectedResultSearch
	projectedResultWebSearch
	projectedResultWebFetch
	projectedResultMutation
	projectedResultCommand
	projectedResultSpawn
	projectedResultTask
)

type projectedToolProfile struct {
	kind          string
	title         projectedToolTitleStyle
	result        projectedToolResultStyle
	terminalKnown bool
	terminalPanel bool
	// suppressSuccessfulEmpty omits presentation-only completion text when the
	// durable result is already represented by the lifecycle status.
	suppressSuccessfulEmpty bool
}

// projectedBuiltinToolProfile is deliberately private to the ACP projection
// owner. Exact Definition.Name values opt into built-in presentation; caller
// tools and historical aliases remain generic and retain their exact names.
func projectedBuiltinToolProfile(name string) (projectedToolProfile, bool) {
	switch name {
	case filesystem.ReadToolName, filesystem.ViewImageToolName:
		return projectedToolProfile{kind: projectedToolKindRead, title: projectedTitlePath, result: projectedResultRead}, true
	case filesystem.WriteToolName, filesystem.PatchToolName:
		return projectedToolProfile{kind: projectedToolKindEdit, title: projectedTitlePath, result: projectedResultMutation}, true
	case filesystem.GlobToolName:
		return projectedToolProfile{kind: projectedToolKindSearch, title: projectedTitlePath, result: projectedResultGlob}, true
	case filesystem.SearchToolName:
		return projectedToolProfile{kind: projectedToolKindSearch, title: projectedTitlePath, result: projectedResultSearch}, true
	case shell.RunCommandToolName:
		return projectedToolProfile{kind: projectedToolKindExecute, title: projectedTitleCommandAction, result: projectedResultCommand, terminalKnown: true, terminalPanel: true}, true
	case tasktool.ToolName:
		return projectedToolProfile{kind: projectedToolKindOther, title: projectedTitleCommandAction, result: projectedResultTask, terminalKnown: true}, true
	case plan.ToolName, tool.ToolSearchToolName:
		return projectedToolProfile{kind: projectedToolKindOther}, true
	case skilltool.ToolName:
		return projectedToolProfile{kind: projectedToolKindRead, title: projectedTitleSkill}, true
	case web.SearchToolName:
		return projectedToolProfile{kind: projectedToolKindSearch, title: projectedTitleQuery, result: projectedResultWebSearch}, true
	case web.FetchToolName:
		return projectedToolProfile{kind: projectedToolKindSearch, title: projectedTitleURL, result: projectedResultWebFetch}, true
	case spawn.ToolName:
		return projectedToolProfile{kind: projectedToolKindExecute, title: projectedTitleSpawn, result: projectedResultSpawn, terminalKnown: true, terminalPanel: true}, true
	case sendmessage.ToolName:
		return projectedToolProfile{kind: projectedToolKindExecute, title: projectedTitleMessage}, true
	case memorytool.RememberToolName:
		return projectedToolProfile{kind: projectedToolKindEdit, title: projectedTitleMemoryUpdate, suppressSuccessfulEmpty: true}, true
	case memorytool.RecallToolName:
		return projectedToolProfile{kind: projectedToolKindSearch, title: projectedTitleSearchQuery}, true
	default:
		return projectedToolProfile{}, false
	}
}

func projectedToolKind(name string) string {
	if profile, ok := projectedBuiltinToolProfile(name); ok {
		return profile.kind
	}
	return projectedToolKindOther
}

func projectedToolTitle(name string, args map[string]any, status string) string {
	profile, known := projectedBuiltinToolProfile(name)
	if !known {
		return name
	}
	switch profile.title {
	case projectedTitlePath:
		if path := strings.TrimSpace(display.MapString(args, "path")); path != "" {
			return name + " " + path
		}
	case projectedTitleSkill:
		if skillName := strings.TrimSpace(display.MapString(args, "name")); skillName != "" {
			return name + " " + skillName
		}
	case projectedTitleQuery:
		if query := strings.TrimSpace(display.MapString(args, "query")); query != "" {
			return name + " " + query
		}
	case projectedTitleURL:
		if url := strings.TrimSpace(display.MapString(args, "url")); url != "" {
			return name + " " + url
		}
	case projectedTitleCommandAction:
		if command := strings.TrimSpace(display.MapString(args, "command")); command != "" {
			return name + " " + command
		}
		if action := strings.TrimSpace(display.MapString(args, "action")); action != "" {
			if taskID := strings.TrimSpace(display.MapString(args, "task_id")); taskID != "" {
				return name + " " + action + " " + taskID
			}
			return name + " " + action
		}
	case projectedTitleSpawn:
		if summary := strings.TrimSpace(display.SpawnFullDisplayArgs(args)); summary != "" {
			return name + " " + summary
		}
	case projectedTitleMessage:
		if summary := strings.TrimSpace(display.AgentMessageFullDisplayArgs(args)); summary != "" {
			return "Send message " + summary
		}
	case projectedTitleMemoryUpdate:
		switch acpToolStatus(status) {
		case eventstream.ToolStatusCompleted:
			return "Updated memory"
		case eventstream.ToolStatusFailed:
			return "Update memory failed"
		default:
			return "Updating memory"
		}
	case projectedTitleSearchQuery:
		if query := strings.TrimSpace(display.MapString(args, "query")); query != "" {
			return "Search " + query
		}
		return "Search"
	}
	return name
}

// projectedToolLifecycleTitle preserves producer titles except for built-ins
// whose title is itself lifecycle state. Those exact names opt into Control's
// canonical presentation and cannot retain a stale producer title after a
// status transition.
func projectedToolLifecycleTitle(name string, args map[string]any, status string, explicit string) string {
	derived := projectedToolTitle(name, args, status)
	if projectedToolTitleTracksLifecycle(name) {
		return firstNonEmpty(derived, strings.TrimSpace(explicit), strings.TrimSpace(name))
	}
	return firstNonEmpty(strings.TrimSpace(explicit), derived, strings.TrimSpace(name))
}

func projectedToolTitleTracksLifecycle(name string) bool {
	profile, ok := projectedBuiltinToolProfile(name)
	return ok && profile.title == projectedTitleMemoryUpdate
}

func projectedDisplayTerminalID(toolCallID string, name string) (string, bool) {
	profile, known := projectedBuiltinToolProfile(name)
	if !known || !profile.terminalKnown || !profile.terminalPanel {
		return "", false
	}
	id := strings.TrimSpace(toolCallID)
	return id, id != ""
}
