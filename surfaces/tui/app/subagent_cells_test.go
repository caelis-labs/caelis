package tuiapp

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func teamCellTestModel(t *testing.T) (*Model, *subagentDelegationStub) {
	t.Helper()
	m, service := newSubagentOverlayTestModel(t)
	m.theme = subagentOverlayBackgroundTestTheme()
	speed := modelprofile.SpeedCapability{DefaultSpeed: "standard", Choices: []modelprofile.SpeedChoice{{Canonical: "standard"}, {Canonical: "fast"}}}
	m.subagentOverlay.status.Targets[0].Speed = speed
	for i := range m.subagentOverlay.status.Handles {
		item := &m.subagentOverlay.status.Handles[i]
		if item.Profile.ID == "provider:sol" {
			item.Profile.Speed = speed
		}
	}
	m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, guardianScreenTestProfile())
	return m, service
}

func TestTeamPickerArrowsEditEffortWithoutTab(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:orbit")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyRight))
	if m.currentSubagentRow().binding.Effort != "xhigh" {
		t.Fatal("right did not adjust the selected model's effort")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyLeft))
	if binding := m.currentSubagentRow().binding; binding.ProfileID != "acp:claude:opus" || binding.Effort != "none" {
		t.Fatalf("arrows did not select ACP or changed its fixed effort: %+v", binding)
	}
	frame := strings.Split(ansi.Strip(m.renderSubagentOverlay()), "\n")
	geometry := m.subagentOverlay.geometry
	row := geometry.rows[m.subagentOverlay.index] - geometry.y
	if line := frame[row]; !strings.Contains(line, "none") {
		t.Fatalf("model picker omitted the fixed effort: %q", line)
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyUp))
	if m.currentSubagentRow().binding.Effort != "xhigh" {
		t.Fatal("model navigation lost the effort draft")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEscape))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if m.currentSubagentRow().binding.Effort != "high" || service.bindRequest.ProfileID != "" {
		t.Fatal("Escape persisted a draft")
	}
}

// Coordinates come from visible glyphs, not the hit-test rectangles.
func clickTeamText(t *testing.T, m *Model, rowText, text string) tea.Cmd {
	t.Helper()
	frame := ansi.Strip(m.renderSubagentOverlay())
	for y, line := range strings.Split(frame, "\n") {
		if !strings.Contains(line, rowText) {
			continue
		}
		index := strings.LastIndex(line, text)
		if index < 0 {
			continue
		}
		g := m.subagentOverlay.geometry
		mouse := tea.Mouse{X: g.x + displayColumns(line[:index]), Y: g.y + y, Button: tea.MouseLeft}
		m.handleSubagentOverlayMouse(tea.MouseClickMsg(mouse))
		// The event loop paints between press and release in a real terminal.
		m.renderSubagentOverlay()
		_, cmd := m.handleSubagentOverlayMouse(tea.MouseReleaseMsg(mouse))
		return cmd
	}
	t.Fatalf("missing clickable %q in %q:\n%s", text, rowText, frame)
	return nil
}

func TestTeamPickerMouseEditsEffortAndAppliesModel(t *testing.T) {
	m, service := teamCellTestModel(t)
	alternate := testProviderModelProfile()
	alternate.ID, alternate.DisplayName = "provider:flash", "deepseek/deepseek-flash"
	m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, alternate)
	clickTeamText(t, m, "orbit", "openai-codex")
	if m.subagentOverlay.page != subagentPageBinding {
		t.Fatal("single click did not open model picker")
	}
	clickTeamText(t, m, "openai-codex", " ›")
	if m.currentSubagentRow().binding.Effort != "xhigh" || m.subagentOverlay.pending {
		t.Fatal("effort click applied or missed draft")
	}
	if cmd := clickTeamText(t, m, alternate.DisplayName, "gh"); cmd != nil || m.currentSubagentRow().binding.Effort != "xhigh" {
		t.Fatal("clicking effort on an unselected model did not edit its draft")
	}
	cmd := clickTeamText(t, m, alternate.DisplayName, alternate.DisplayName)
	if cmd == nil {
		t.Fatal("single model click did not apply")
	}
	m.Update(cmd())
	if service.bindRequest.Handle != agentbinding.HandleOrbit || service.bindRequest.ProfileID != alternate.ID || service.bindRequest.Effort != "xhigh" {
		t.Fatalf("wrong typed binding: %+v", service.bindRequest)
	}
}

func TestTeamOverviewHorizontalArrowsDoNotEdit(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:orbit")
	for _, key := range []rune{tea.KeyRight, tea.KeyLeft} {
		if cmd := m.handleSubagentOverlayKey(subagentSpecialKey(key)); cmd != nil {
			t.Fatal("overview effort shortcut dispatched a mutation")
		}
	}
	if m.currentSubagentRow().binding.Effort != "high" || service.bindRequest.ProfileID != "" {
		t.Fatal("overview arrows altered the binding")
	}
}

func TestTeamCellFocusLightTheme(t *testing.T) {
	m, _ := teamCellTestModel(t)
	m.theme = tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	selectSubagentTestRow(t, m, "handle:guardian")
	frame := m.renderSubagentOverlay()
	if plain := ansi.Strip(frame); strings.Contains(plain, "Purpose") || !strings.Contains(plain, "Quick checks") {
		t.Fatalf("overview did not display role descriptions without a heading:\n%s", plain)
	}
	g := m.subagentOverlay.geometry
	screen := uv.NewScreenBuffer(g.width, g.height)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(frame).Draw(screen, screen.Bounds())
	y := g.rows[m.subagentOverlay.index] - g.y
	line := strings.Split(ansi.Strip(frame), "\n")[y]
	if !strings.Contains(line, "openai-codex/gpt-5.6-sol [xhigh]") {
		t.Fatalf("effort is separated from the model: %q", line)
	}
	right := g.closeX - g.x + len("[x]") - 1
	if !colorInSet(screen.CellAt(right, y).Style.Bg, []color.Color{m.theme.SelectionBg}) {
		t.Fatal("selection stops short of the divider edge")
	}
	var colors []color.Color
	for _, value := range []string{"openai-codex", "[xhigh]", "Tool approvals"} {
		column := strings.Index(line, value)
		if column < 0 {
			t.Fatalf("selected Guardian row omitted %q: %q", value, line)
		}
		colors = append(colors, screen.CellAt(displayColumns(line[:column]), y).Style.Fg)
	}
	if colors[0] == nil || colors[1] == nil || colors[2] == nil ||
		colorInSet(colors[0], colors[1:]) || colorInSet(colors[1], colors[2:]) {
		t.Fatal("model, effort, and purpose must remain visually distinct on the selected row")
	}
}

func TestTeamGuardianPaneFocusAndAlignment(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, width := range []int{35, 120} {
			t.Run(fmt.Sprintf("dark=%t/width=%d", dark, width), func(t *testing.T) {
				m, _ := teamCellTestModel(t)
				m.theme = tuikit.ResolveThemeWithState(dark, false, colorprofile.TrueColor)
				m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
				selectSubagentTestRow(t, m, "handle:guardian")
				m.Update(subagentSpecialKey(tea.KeyEnter))
				assertFrame := func(focused, inactive, selected, other string) {
					t.Helper()
					frame := m.renderSubagentOverlay()
					g := m.subagentOverlay.geometry
					screen := uv.NewScreenBuffer(g.width, g.height)
					screen.Method = ansi.GraphemeWidth
					uv.NewStyledString(frame).Draw(screen, screen.Bounds())
					lines := strings.Split(ansi.Strip(frame), "\n")
					cell := func(value string) (int, *uv.Cell) {
						t.Helper()
						for y, line := range lines {
							if x := strings.Index(line, value); x >= 0 {
								column := displayColumns(line[:x])
								return column, screen.CellAt(column, y)
							}
						}
						t.Fatalf("missing %q in frame:\n%s", value, ansi.Strip(frame))
						return 0, nil
					}
					_, active := cell(focused)
					_, passive := cell(inactive)
					if colorInSet(active.Style.Fg, []color.Color{passive.Style.Fg}) {
						t.Fatal("focused and inactive headings share a foreground")
					}
					for _, heading := range []*uv.Cell{active, passive} {
						if corrected := m.theme.ReadableTextColor(heading.Style.Fg, heading.Style.Bg); !colorInSet(heading.Style.Fg, []color.Color{corrected}) {
							t.Fatal("a section heading is not readable on the panel")
						}
					}
					activeX, activeModel := cell(selected)
					passiveX, passiveModel := cell(other)
					if activeX != passiveX || !colorInSet(activeModel.Style.Bg, []color.Color{m.theme.CommandActiveStyle().GetBackground()}) ||
						colorInSet(passiveModel.Style.Bg, []color.Color{m.theme.CommandActiveStyle().GetBackground()}) {
						t.Fatalf("model alignment/highlight mismatch: active=%d inactive=%d", activeX, passiveX)
					}
				}
				assertFrame("Review model", "Classifier", "openai-", "Disabled")
				m.Update(subagentSpecialKey(tea.KeyDown))
				assertFrame("Classifier", "Review model", "Disabled", "openai-")
			})
		}
	}
}

func TestTeamCellFocusRenderedFrames(t *testing.T) {
	for _, width := range []int{24, 35, 60, 100, 120} {
		for _, picker := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/picker=%t", width, picker), func(t *testing.T) {
				m, _ := teamCellTestModel(t)
				m.width = width
				selectSubagentTestRow(t, m, "handle:guardian")
				if picker {
					m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
				}
				if !picker {
					for i := range m.subagentOverlay.status.Handles {
						item := &m.subagentOverlay.status.Handles[i]
						if item.Definition.Handle == agentbinding.HandleGuardianScreen {
							item.Binding = agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: "provider:jev", Effort: "none"}
						}
					}
				}
				frame := m.renderSubagentOverlay()
				plain := ansi.Strip(frame)
				for _, line := range strings.Split(plain, "\n") {
					if displayColumns(line) > width {
						t.Fatalf("overflow: %q", line)
					}
				}
				if strings.Contains(plain, "7/12") || strings.Contains(plain, "Choose Agent model") || strings.Contains(plain, "▽") {
					t.Fatal("obsolete menu text")
				}
				// Keep reviewable full frames for both menus at wide and narrow sizes.
				name := fmt.Sprintf("team-%d-picker-%t", width, picker)
				path := filepath.Join("testdata", "team", name+".golden")
				lines := strings.Split(plain, "\n")
				for i := range lines {
					lines[i] = strings.TrimRight(lines[i], " \t")
				}
				golden := strings.Join(lines, "\n") + "\n"
				if os.Getenv("CAELIS_TEAM_GOLDEN_UPDATE") != "" {
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(golden), 0644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(want) != golden {
					t.Fatalf("render differs from %s:\n%s", path, plain)
				}
				if dir := os.Getenv("CAELIS_TEAM_UI_REPORT"); dir != "" {
					if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(frame), 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestTeamRetiredMemoryHandlesAreAbsent(t *testing.T) {
	m, _ := teamCellTestModel(t)
	// Even an older Host projection must not recreate retired controls.
	for _, handle := range []agentbinding.Handle{"steward", "memory-verifier", "tool-search"} {
		m.subagentOverlay.status.Handles = append(m.subagentOverlay.status.Handles, agentbinding.HandleStatus{Definition: agentbinding.Definition{Handle: handle, Name: string(handle), Class: agentbinding.HandleClassSystem, Configurable: true}})
	}
	for _, row := range m.subagentMainRows() {
		if agentbinding.IsRetired(row.handle) {
			t.Fatalf("retired row: %#v", row)
		}
	}
	for _, width := range []int{24, 35, 60, 100, 120} {
		m.width = width
		frame := strings.ToLower(ansi.Strip(m.renderSubagentOverlay()))
		if strings.Contains(frame, "steward") || strings.Contains(frame, "verifier") || strings.Contains(frame, "zero-token") {
			t.Fatalf("retired entry at width %d:\n%s", width, frame)
		}
	}
}
