package displaypolicy

func ExplorationVerbForTool(name string) string {
	switch SemanticToolName(name, "") {
	case "READ":
		return "Read"
	case "LIST":
		return "List"
	case "GLOB":
		return "Glob"
	case "SEARCH", "RG", "FIND", "WEB_SEARCH":
		return "Search"
	case "WEB_FETCH":
		return "Fetch"
	case "SKILL":
		return "Skill"
	default:
		return ""
	}
}

func IsExplorationTool(name string) bool {
	return ExplorationVerbForTool(name) != ""
}
