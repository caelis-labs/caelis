// Package uipreferences defines persisted presentation choices. These values
// never select an Agent, change execution, or enter model context.
package uipreferences

import "fmt"

// Layout places the single subagent pane relative to the main pane.
type Layout string

const (
	Overlay Layout = "overlay"
	Left    Layout = "left"
	Right   Layout = "right"
	Up      Layout = "up"
	Down    Layout = "down"
	// MinRatio and MaxRatio bound the main pane's percentage of the available
	// split axis. Surfaces additionally enforce a minimum number of cells.
	MinRatio = 30
	MaxRatio = 70
)

// Preferences retains layout choices independently of Session or pane state.
// Zero values describe a user who has not chosen a layout yet.
type Preferences struct {
	SubagentLayout  Layout `json:"subagent_layout,omitempty"`
	HorizontalRatio int    `json:"horizontal_ratio,omitempty"`
	VerticalRatio   int    `json:"vertical_ratio,omitempty"`
}

// Validate rejects unsupported layouts and persisted ratios outside the bounds.
func (p Preferences) Validate() error {
	switch p.SubagentLayout {
	case "", Overlay, Left, Right, Up, Down:
	default:
		return fmt.Errorf("invalid subagent layout %q", p.SubagentLayout)
	}
	for _, ratio := range []int{p.HorizontalRatio, p.VerticalRatio} {
		if ratio != 0 && (ratio < MinRatio || ratio > MaxRatio) {
			return fmt.Errorf("split ratio must be between %d and %d", MinRatio, MaxRatio)
		}
	}
	return nil
}

// WithDefaults supplies presentation defaults after validation.
func (p Preferences) WithDefaults() Preferences {
	if p.SubagentLayout == "" {
		p.SubagentLayout = Overlay
	}
	if p.HorizontalRatio == 0 {
		p.HorizontalRatio = 55
	}
	if p.VerticalRatio == 0 {
		p.VerticalRatio = 50
	}
	return p
}
