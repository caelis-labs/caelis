package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
)

func overrideSkillContentRead(semanticName string, kind string, title string, raw map[string]any) string {
	if !strings.EqualFold(strings.TrimSpace(semanticName), "READ") && !strings.EqualFold(strings.TrimSpace(kind), "read") {
		return ""
	}
	if name := display.SkillContentNameFromHint(title, toolPath(raw)); name != "" {
		return "SKILL"
	}
	return ""
}

func skillContentDisplayNameFromRaw(raw map[string]any) string {
	return display.SkillContentNameFromHint("", toolPath(raw))
}
