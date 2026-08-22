package tuiapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/display"
)

func skillContentDisplayNameFromRaw(raw map[string]any) string {
	return display.SkillContentNameFromHint("", toolPath(raw))
}
