package display

import names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"

func ExplorationVerbForTool(name string) string {
	if info, ok := names.Lookup(name); ok {
		return info.ExplorationVerb
	}
	return ""
}

func IsExplorationTool(name string) bool {
	return ExplorationVerbForTool(name) != ""
}
