package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func omitRedundantMutationDiffFileHeader(args string, text string, err bool, panel []RenderedRow) []RenderedRow {
	if err || len(panel) == 0 || !isDiffPanelText(text) {
		return panel
	}
	lines := parseDiffPanelText(text).Lines
	if len(lines) == 0 || lines[0].Kind != diffPanelLineMeta || strings.TrimSpace(lines[0].Path) == "" {
		return panel
	}
	if !diffFileHeaderMatchesToolArgs(args, lines[0].Text) {
		return panel
	}
	for _, line := range lines[1:] {
		if line.Kind == diffPanelLineMeta && strings.TrimSpace(line.Path) != "" {
			return panel
		}
	}
	return panel[1:]
}

func diffFileHeaderMatchesToolArgs(args string, header string) bool {
	args = strings.TrimSpace(args)
	header = strings.TrimSpace(header)
	if args == "" || header == "" {
		return false
	}
	if args == header {
		return true
	}
	argPath, _, _, argOK := tuikit.SplitDiffCountTokens(args)
	headerPath, _, _, headerOK := tuikit.SplitDiffCountTokens(header)
	if argOK && !headerOK && argPath == header {
		return true
	}
	return headerOK && !argOK && headerPath == args
}
