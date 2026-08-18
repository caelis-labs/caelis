package transcript

import (
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

func transcriptToolIsPlan(name string) bool {
	return name == plan.ToolName
}

func transcriptToolIsExploration(name string) bool {
	switch name {
	case filesystem.ReadToolName, filesystem.ViewImageToolName, filesystem.GlobToolName,
		filesystem.SearchToolName, skilltool.ToolName, web.SearchToolName, web.FetchToolName:
		return true
	default:
		return false
	}
}

func transcriptToolIsTerminalPanel(name string) bool {
	switch name {
	case shell.RunCommandToolName, spawn.ToolName:
		return true
	default:
		return false
	}
}
