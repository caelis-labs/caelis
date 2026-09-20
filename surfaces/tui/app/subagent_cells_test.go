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

func TestTeamCellFocusKeyboardCyclesFieldsAndSkipsUnsupported(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:orbit")
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	key := m.currentSubagentRow().key
	for _, field := range []subagentField{subagentFieldEffort, subagentFieldFast, subagentFieldModel} {
		m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
		if m.subagentOverlay.field != field || m.currentSubagentRow().key != key {
			t.Fatalf("Tab moved model or focused wrong field: %v %s", m.subagentOverlay.field, m.currentSubagentRow().key)
		}
		assertTeamSingleCellHighlight(t, m)
	}
	m.handleSubagentOverlayKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.subagentOverlay.field != subagentFieldFast {
		t.Fatal("Shift+Tab did not reverse field order")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	if m.currentSubagentRow().binding.ProfileID != "acp:claude:opus" || m.subagentOverlay.field != subagentFieldModel {
		t.Fatal("unsupported Fast focus did not fall back to Model")
	}
	for range 3 {
		m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	}
	if m.subagentOverlay.field != subagentFieldModel || m.currentSubagentRow().binding.ProfileID != "acp:claude:opus" {
		t.Fatal("fixed-effort model exposed a phantom Tab stop")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyUp))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyRight))
	m.handleSubagentOverlayKey(keyPress("f"))
	if m.currentSubagentRow().binding.Effort != "xhigh" || !m.currentSubagentRow().fastMode {
		t.Fatal("quick controls did not edit the draft")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEscape))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if m.currentSubagentRow().binding.Effort != "high" || m.currentSubagentRow().fastMode || service.bindRequest.ProfileID != "" {
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
		index := strings.Index(line, text)
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

func TestTeamCellFocusSingleClickEditsControlsAndAppliesModel(t *testing.T) {
	m, service := teamCellTestModel(t)
	clickTeamText(t, m, "orbit", "openai-codex")
	if m.subagentOverlay.page != subagentPageBinding {
		t.Fatal("single click did not open model picker")
	}
	clickTeamText(t, m, "openai-codex", "›")
	if m.currentSubagentRow().binding.Effort != "xhigh" || m.subagentOverlay.pending {
		t.Fatal("effort click applied or missed draft")
	}
	assertTeamSingleCellHighlight(t, m)
	clickTeamText(t, m, "openai-codex", "Fast off")
	if !m.currentSubagentRow().fastMode || m.subagentOverlay.pending {
		t.Fatal("Fast click applied or missed draft")
	}
	assertTeamSingleCellHighlight(t, m)
	cmd := clickTeamText(t, m, "openai-codex", "openai-codex")
	if cmd == nil {
		t.Fatal("single model click did not apply")
	}
	m.Update(cmd())
	if service.bindRequest.Handle != agentbinding.HandleOrbit || service.bindRequest.Speed != "fast" || service.bindRequest.Effort != "xhigh" {
		t.Fatalf("wrong typed binding: %+v", service.bindRequest)
	}
}

func TestTeamCellFocusMemoryVerifierSharesStewardAndOffersJev(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:steward")
	frame := ansi.Strip(m.renderSubagentOverlay())
	if strings.Contains(frame, "Memory Verifier") || !strings.Contains(frame, "Verifier Disabled") {
		t.Fatalf("Verifier is not paired:\n%s", frame)
	}
	clickTeamText(t, m, "Memory Steward", "Verifier")
	if m.subagentOverlay.bindingHandle != agentbinding.HandleMemoryVerifier {
		t.Fatal("Verifier opened Steward model picker")
	}
	frame = ansi.Strip(m.renderSubagentOverlay())
	if !strings.Contains(frame, "Jev") || strings.Contains(frame, "openai-codex") || strings.Contains(frame, "Claude") {
		t.Fatalf("incompatible verifier catalog:\n%s", frame)
	}
	cmd := clickTeamText(t, m, "Jev", "Jev")
	if cmd == nil {
		t.Fatal("Jev single click did not bind")
	}
	m.Update(cmd())
	if service.bindRequest.Handle != agentbinding.HandleMemoryVerifier || service.bindRequest.ProfileID != "provider:jev" || service.bindRequest.Effort != "none" {
		t.Fatalf("verifier binding: %+v", service.bindRequest)
	}
	if m.currentSubagentRow().handle != agentbinding.HandleSteward || m.subagentOverlay.field != subagentFieldAuxiliary {
		t.Fatal("save lost Steward auxiliary focus")
	}
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	cmd = clickTeamText(t, m, "Disabled", "Disabled")
	if cmd == nil {
		t.Fatal("missing disable command")
	}
	cmd()
	if service.reset != agentbinding.HandleMemoryVerifier {
		t.Fatal("disabling verifier changed Steward binding")
	}
}

func TestTeamCellFocusMainQuickControlsUseTypedBinding(t *testing.T) {
	m, service := teamCellTestModel(t)
	selectSubagentTestRow(t, m, "handle:orbit")
	cmd := m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyRight))
	if cmd == nil {
		t.Fatal("main effort shortcut did not save")
	}
	msg := cmd().(subagentOverlayResultMsg)
	for i := range msg.status.Handles {
		if msg.status.Handles[i].Definition.Handle == agentbinding.HandleOrbit {
			msg.status.Handles[i].Binding = service.bindRequest
		}
	}
	m.Update(msg)
	if service.bindRequest.Handle != agentbinding.HandleOrbit || service.bindRequest.Effort != "xhigh" || m.subagentOverlay.page != subagentPageMain {
		t.Fatal("wrong quick-edit binding or navigation")
	}
	// The stub's read refresh does not retain synthetic capabilities.
	for i := range m.subagentOverlay.status.Handles {
		if m.subagentOverlay.status.Handles[i].Definition.Handle == agentbinding.HandleOrbit {
			m.subagentOverlay.status.Handles[i].Profile.Speed = modelprofile.SpeedCapability{Choices: []modelprofile.SpeedChoice{{Canonical: "standard"}, {Canonical: "fast"}}}
		}
	}
	m.refreshSubagentRows("handle:orbit")
	cmd = m.handleSubagentOverlayKey(keyPress("f"))
	if cmd == nil {
		t.Fatal("main Fast shortcut did not save")
	}
	cmd()
	if service.bindRequest.Speed != "fast" || service.bindRequest.Effort != "xhigh" {
		t.Fatal("Fast changed effort")
	}
}

func assertTeamSingleCellHighlight(t *testing.T, m *Model) {
	t.Helper()
	frame := m.renderSubagentOverlay()
	g := m.subagentOverlay.geometry
	row := m.subagentOverlay.index
	screen := uv.NewScreenBuffer(g.width, g.height)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(frame).Draw(screen, screen.Bounds())
	var focused subagentCell
	for _, cell := range g.cells[row] {
		if cell.field == m.subagentOverlay.field {
			focused = cell
		}
	}
	if focused.width == 0 {
		t.Fatal("focused field has no visible cell")
	}
	for y := range g.height {
		for x := range g.width {
			cell := screen.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			selected := colorInSet(cell.Style.Bg, []color.Color{m.theme.SelectionBg})
			want := y+g.y == g.rows[row] && x+g.x >= focused.x && x+g.x < focused.x+focused.width
			if selected != want {
				t.Fatalf("focus leaked or missing at (%d,%d), field=%v want=%v selected=%v\n%s", x, y, m.subagentOverlay.field, want, selected, ansi.Strip(frame))
			}
		}
	}
}

func TestTeamCellFocusLightTheme(t *testing.T) {
	m, _ := teamCellTestModel(t)
	m.theme = tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	selectSubagentTestRow(t, m, "handle:guardian")
	assertTeamSingleCellHighlight(t, m)
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	assertTeamSingleCellHighlight(t, m)
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
	m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	for range 3 {
		assertTeamSingleCellHighlight(t, m)
		m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
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
				m.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyTab))
				assertTeamSingleCellHighlight(t, m)
				frame := m.renderSubagentOverlay()
				plain := ansi.Strip(frame)
				for _, line := range strings.Split(plain, "\n") {
					if displayColumns(line) > width {
						t.Fatalf("overflow: %q", line)
					}
				}
				if strings.Contains(plain, "7/12") || strings.Contains(plain, "Choose Agent model") {
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
