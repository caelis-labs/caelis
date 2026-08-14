package tuiapp

import "strings"

func wrapToolOutputText(text string, width int) []string {
	if !renderableTextHasContent(text) {
		return nil
	}
	width = maxInt(1, width)
	parts := splitRenderableLines(text)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if !renderableLineHasContent(part) {
			continue
		}
		for _, segment := range strings.Split(hardWrapDisplayLine(part, width), "\n") {
			if renderableLineHasContent(segment) {
				out = append(out, segment)
			}
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func isSpawnLikeTool(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, "SPAWN")
}
