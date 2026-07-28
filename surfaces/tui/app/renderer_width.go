package tuiapp

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// forceRendererGraphemeWidthCmd keeps Bubble Tea's differential renderer on
// the same grapheme-width model used by Caelis layout and Lip Gloss.
//
// Bubble Tea otherwise starts in wcwidth mode and only switches after a
// terminal answers the DEC mode 2027 query. Some multiplexers render extended
// graphemes correctly but do not answer that query, which makes later stream
// appends land one cell early. Caelis already lays out every surface with
// grapheme width, so leaving the renderer on wcwidth is never internally
// consistent.
//
// This synthesizes a DECRPM report because Bubble Tea has no width-method
// option. ModeReset means “recognized and currently disabled”; it is a report,
// not a command to reset Unicode mode. It avoids falsely claiming that the
// terminal already has the mode set when no report was received. Bubble Tea
// treats that state as support, selects GraphemeWidth, and emits CSI ?2027h to
// enable Unicode core mode. The same report then reaches Model.Update, where it
// is intentionally a no-op.
//
// Remove this compatibility command when Bubble Tea exposes a program option
// for selecting the renderer width method.
func forceRendererGraphemeWidthCmd() tea.Cmd {
	return func() tea.Msg {
		return tea.ModeReportMsg{
			Mode:  ansi.ModeUnicodeCore,
			Value: ansi.ModeReset,
		}
	}
}
