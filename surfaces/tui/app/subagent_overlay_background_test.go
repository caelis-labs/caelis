package tuiapp

import (
	"image/color"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestSubagentConfigurationOverlayPaintsOneBackgroundAcrossStyledRows(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	model.theme = subagentOverlayBackgroundTestTheme()
	model.themeCacheKey = ""

	frame := model.renderSubagentOverlay()
	geometry := model.subagentOverlay.geometry
	assertOverlayCellBackgrounds(
		t,
		frame,
		geometry.width,
		geometry.height,
		model.theme.ModalBg,
		model.theme.SelectionBg,
	)
}

func TestSubagentRosterOverlayPaintsOneBackgroundAcrossStyledRows(t *testing.T) {
	model := newSubagentRosterTestModel()
	model.theme = subagentOverlayBackgroundTestTheme()
	model.themeCacheKey = ""
	now := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.Local)
	addSubagentRosterTestView(
		model,
		"spawn-rhea",
		"rhea",
		"rhea[reviewer]: audit overlay physical backgrounds",
		"running",
		now.Add(-time.Minute),
		time.Time{},
	)
	addSubagentRosterTestView(
		model,
		"spawn-milo",
		"milo",
		"milo[breeze]: trace nested ANSI resets",
		"completed",
		now.Add(-2*time.Minute),
		now,
	)
	if !model.openSubagentRosterOverlay() {
		t.Fatal("openSubagentRosterOverlay() = false")
	}

	frame := model.renderSubagentRosterOverlay()
	geometry := model.subagentRosterOverlay.geometry
	assertOverlayCellBackgrounds(
		t,
		frame,
		geometry.width,
		geometry.height,
		model.theme.ModalBg,
		model.theme.SelectionBg,
	)
}

func subagentOverlayBackgroundTestTheme() tuikit.Theme {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	theme.ModalBg = lipgloss.Color("#263244")
	theme.InvalidateTokens()
	return theme
}

func assertOverlayCellBackgrounds(
	t *testing.T,
	frame string,
	width int,
	height int,
	allowed ...color.Color,
) {
	t.Helper()
	screen := uv.NewScreenBuffer(width, height)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(frame).Draw(screen, screen.Bounds())
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cell := screen.CellAt(x, y)
			if cell != nil && cell.Width == 0 {
				continue
			}
			if cell == nil || cell.Style.Bg == nil {
				t.Fatalf("overlay cell (%d,%d) has no background", x, y)
			}
			if !colorInSet(cell.Style.Bg, allowed) {
				t.Fatalf("overlay cell (%d,%d) background = %v, want one of %v", x, y, cell.Style.Bg, allowed)
			}
		}
	}
}

func colorInSet(got color.Color, allowed []color.Color) bool {
	if got == nil {
		return false
	}
	gotR, gotG, gotB, gotA := got.RGBA()
	for _, candidate := range allowed {
		if candidate == nil {
			continue
		}
		wantR, wantG, wantB, wantA := candidate.RGBA()
		if gotR == wantR && gotG == wantG && gotB == wantB && gotA == wantA {
			return true
		}
	}
	return false
}
