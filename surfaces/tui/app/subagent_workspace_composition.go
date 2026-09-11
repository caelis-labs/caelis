package tuiapp

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Split panes occupy disjoint rectangles, so they can be joined without
// decoding the entire screen into cells twice. Retain the extracted main rows
// while only the child scrolls; frame and surface changes still compose anew.
type splitWorkspaceFrameCache struct {
	layout                      workspaceLayout
	base, child, divider, frame string
	mainRows                    []string
}

func (c *splitWorkspaceFrameCache) compose(base, child, divider string, layout workspaceLayout) string {
	if c.layout == layout && c.base == base && c.child == child && c.divider == divider && c.frame != "" {
		return c.frame
	}
	if c.layout != layout || c.base != base {
		lines := strings.Split(base, "\n")
		c.mainRows = make([]string, layout.main.height)
		for i := range c.mainRows {
			if y := layout.main.y + i; y < len(lines) {
				c.mainRows[i] = ansi.Cut(lines[y], layout.main.x, layout.main.x+layout.main.width)
			}
		}
	}
	childRows := strings.Split(child, "\n")
	var rows []string
	if layout.divider.width == 1 {
		rows = make([]string, layout.main.height)
		for i := range rows {
			childRow := strings.Repeat(" ", layout.child.width)
			if i < len(childRows) {
				childRow = childRows[i]
			}
			if layout.child.x > layout.main.x {
				rows[i] = c.mainRows[i] + divider + childRow
			} else {
				rows[i] = childRow + divider + c.mainRows[i]
			}
		}
	} else if layout.child.y > layout.main.y {
		rows = append(rows, c.mainRows...)
		rows = append(rows, divider)
		rows = append(rows, childRows...)
	} else {
		rows = append(rows, childRows...)
		rows = append(rows, divider)
		rows = append(rows, c.mainRows...)
	}
	c.layout, c.base, c.child, c.divider = layout, base, child, divider
	c.frame = strings.Join(rows, "\n")
	return c.frame
}
