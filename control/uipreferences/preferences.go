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
	// DefaultHorizontalRatio and DefaultVerticalRatio are presentation defaults
	// when a ratio has not been chosen. Surfaces send these values explicitly
	// to reset a persisted split.
	DefaultHorizontalRatio = 55
	DefaultVerticalRatio   = 50
)

// Preferences retains layout and theme choices independently of Session or pane
// state. Zero values describe a user who has not chosen a layout or theme yet.
// Theme is an opaque selected name; the TUI owns palette names and the default
// "auto". Host does not interpret or validate palettes.
type Preferences struct {
	SubagentLayout  Layout `json:"subagent_layout,omitempty"`
	HorizontalRatio int    `json:"horizontal_ratio,omitempty"`
	VerticalRatio   int    `json:"vertical_ratio,omitempty"`
	Theme           string `json:"theme,omitempty"`
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

// Merge copies each nonzero field from update. Omitted and zero fields leave
// the receiver unchanged. Surfaces persist Theme as an opaque selected name,
// including "auto", and reset layout or ratios by sending explicit values such
// as overlay and the default split ratios.
func (p Preferences) Merge(update Preferences) Preferences {
	if update.SubagentLayout != "" {
		p.SubagentLayout = update.SubagentLayout
	}
	if update.HorizontalRatio != 0 {
		p.HorizontalRatio = update.HorizontalRatio
	}
	if update.VerticalRatio != 0 {
		p.VerticalRatio = update.VerticalRatio
	}
	if update.Theme != "" {
		p.Theme = update.Theme
	}
	return p
}

// WithDefaults supplies presentation defaults after validation. It does not
// select a theme; an empty Theme remains unset so surfaces can apply "auto".
func (p Preferences) WithDefaults() Preferences {
	if p.SubagentLayout == "" {
		p.SubagentLayout = Overlay
	}
	if p.HorizontalRatio == 0 {
		p.HorizontalRatio = DefaultHorizontalRatio
	}
	if p.VerticalRatio == 0 {
		p.VerticalRatio = DefaultVerticalRatio
	}
	return p
}
