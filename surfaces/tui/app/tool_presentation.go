package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

const (
	surfaceToolRead        = filesystem.ReadToolName
	surfaceToolViewImage   = filesystem.ViewImageToolName
	surfaceToolWrite       = filesystem.WriteToolName
	surfaceToolPatch       = filesystem.PatchToolName
	surfaceToolGlob        = filesystem.GlobToolName
	surfaceToolGrep        = filesystem.SearchToolName
	surfaceToolRunCommand  = shell.RunCommandToolName
	surfaceToolTask        = tasktool.ToolName
	surfaceToolPlan        = plan.ToolName
	surfaceToolSkill       = skilltool.ToolName
	surfaceToolWebSearch   = web.SearchToolName
	surfaceToolWebFetch    = web.FetchToolName
	surfaceToolSpawn       = spawn.ToolName
	surfaceToolSendMessage = sendmessage.ToolName
	surfaceToolToolSearch  = tool.ToolSearchToolName
)

type surfaceToolKind uint8

const (
	surfaceToolKindOther surfaceToolKind = iota
	surfaceToolKindRead
	surfaceToolKindEdit
	surfaceToolKindSearch
	surfaceToolKindExecute
)

type surfaceToolResultStyle uint8

const (
	surfaceResultNone surfaceToolResultStyle = iota
	surfaceResultRead
	surfaceResultGlob
	surfaceResultSearch
	surfaceResultWebSearch
	surfaceResultWebFetch
	surfaceResultMutation
)

type surfaceToolProfileInfo struct {
	Name            string
	Kind            surfaceToolKind
	ResultStyle     surfaceToolResultStyle
	ExplorationVerb string
}

// surfaceToolProfile is the TUI's private presentation table. Only exact
// built-in Definition.Name values opt into built-in labels and panel routing.
func surfaceToolProfile(name string) (info surfaceToolProfileInfo, ok bool) {
	defer func() {
		if ok {
			info.Name = name
		}
	}()
	switch name {
	case surfaceToolRead:
		return surfaceToolProfileInfo{Kind: surfaceToolKindRead, ResultStyle: surfaceResultRead, ExplorationVerb: "Read"}, true
	case surfaceToolViewImage:
		return surfaceToolProfileInfo{Kind: surfaceToolKindRead, ResultStyle: surfaceResultRead, ExplorationVerb: "View"}, true
	case surfaceToolWrite, surfaceToolPatch:
		return surfaceToolProfileInfo{Kind: surfaceToolKindEdit, ResultStyle: surfaceResultMutation}, true
	case surfaceToolGlob:
		return surfaceToolProfileInfo{Kind: surfaceToolKindSearch, ResultStyle: surfaceResultGlob, ExplorationVerb: "Glob"}, true
	case surfaceToolGrep:
		return surfaceToolProfileInfo{Kind: surfaceToolKindSearch, ResultStyle: surfaceResultSearch, ExplorationVerb: "Search"}, true
	case surfaceToolRunCommand, surfaceToolTask, surfaceToolSpawn, surfaceToolSendMessage:
		return surfaceToolProfileInfo{Kind: surfaceToolKindExecute}, true
	case surfaceToolPlan, surfaceToolToolSearch:
		return surfaceToolProfileInfo{Kind: surfaceToolKindOther}, true
	case surfaceToolSkill:
		return surfaceToolProfileInfo{Kind: surfaceToolKindRead, ExplorationVerb: "Skill"}, true
	case surfaceToolWebSearch:
		return surfaceToolProfileInfo{Kind: surfaceToolKindSearch, ResultStyle: surfaceResultWebSearch, ExplorationVerb: "Search"}, true
	case surfaceToolWebFetch:
		return surfaceToolProfileInfo{Kind: surfaceToolKindSearch, ResultStyle: surfaceResultWebFetch, ExplorationVerb: "Fetch"}, true
	default:
		return surfaceToolProfileInfo{}, false
	}
}

func surfaceExplorationVerb(name string) string {
	if info, ok := surfaceToolProfile(name); ok {
		return info.ExplorationVerb
	}
	return ""
}

func surfaceIsExplorationTool(name string) bool {
	return surfaceExplorationVerb(name) != ""
}

func surfaceIsTerminalPanelTool(name string) bool {
	switch name {
	case surfaceToolRunCommand, surfaceToolSpawn:
		return true
	default:
		return false
	}
}

func surfaceSanitizeSpawnHeaderArgs(args string) string {
	args = strings.TrimSpace(args)
	if args == surfaceToolSpawn {
		return ""
	}
	prefix := surfaceToolSpawn + " "
	if strings.HasPrefix(args, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(args, prefix))
	}
	return args
}

func surfaceHydrateToolSummaryOutput(name string, output map[string]any, meta map[string]any) map[string]any {
	out := make(map[string]any, len(output))
	for key, value := range output {
		out[key] = value
	}
	info, known := surfaceToolProfile(name)
	if !known {
		if len(out) == 0 {
			return nil
		}
		return out
	}
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	for _, key := range surfaceSummaryMetadataKeys(info.ResultStyle) {
		if _, exists := out[key]; exists {
			continue
		}
		if value, exists := toolMeta[key]; exists {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func surfaceSummaryMetadataKeys(style surfaceToolResultStyle) []string {
	switch style {
	case surfaceResultRead:
		return []string{"path", "file_path", "start_line", "end_line", "next_offset", "has_more"}
	case surfaceResultGlob:
		return []string{"pattern", "count", "total_count"}
	case surfaceResultSearch:
		return []string{"pattern", "query", "count", "file_count"}
	case surfaceResultWebSearch:
		return []string{"query", "provider", "model", "status", "answer", "results", "message"}
	case surfaceResultWebFetch:
		return []string{"url", "final_url", "title", "status", "status_code", "content_type", "format", "message"}
	default:
		return nil
	}
}
