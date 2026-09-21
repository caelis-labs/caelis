package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func connectMouse(m *Model, msg tea.MouseMsg) {
	_, cmd := m.Update(msg)
	runConnectTestCmd(m, cmd)
}

func wizardTextPointForTest(t *testing.T, m *Model, text string, end bool) tea.Mouse {
	t.Helper()
	s, g := &m.wizardOverlay.text, m.wizardOverlay.geometry
	for line, value := range s.lines {
		index := strings.Index(value, text)
		if index < 0 {
			continue
		}
		column := displayColumns(value[:index])
		if end {
			column += displayColumns(text)
		}
		for _, row := range s.rows {
			if row.line == line && column >= row.from && column <= row.from+row.width {
				return tea.Mouse{X: g.contentX + column - row.from, Y: g.contentY + row.y, Button: tea.MouseLeft}
			}
		}
	}
	t.Fatalf("text endpoint is not visible: %q end=%v", text, end)
	return tea.Mouse{}
}

func TestWizardManualDragCopiesLogicalText(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		m, requests, _ := acpInstallationWizardFixture(t)
		connectPress(m, "down")
		connectPress(m, "enter")
		m.width, m.height = 48, 44
		m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
		command := "cd '/Users/测试/é/tools/a long runtime directory/antigravity-acp'"
		m.wizardOverlay.runtimeSetup.ManualSteps = []string{command, "chmod +x 'agy_acp_server.par'"}
		var copied string
		m.cfg.WriteClipboardText = func(text string) error { copied = text; return nil }
		before := m.View().Content
		if strings.Contains(before, "Copy instructions") || strings.Contains(before, "[c]") {
			t.Fatal("obsolete copy control is still displayed")
		}
		connectPress(m, "c")
		if copied != "" {
			t.Fatal("obsolete shortcut copied text")
		}
		for _, value := range []string{command, m.wizardOverlay.runtimeSetup.ArchiveURL, "agy_acp_server.par"} {
			start := wizardTextPointForTest(t, m, value, false)
			end := wizardTextPointForTest(t, m, value, true)
			if reverse {
				start, end = end, start
			}
			connectMouse(m, tea.MouseClickMsg(start))
			m.Update(tea.MouseMotionMsg(end))
			highlighted := m.View().Content
			if highlighted == before || !m.wizardOverlay.text.selecting {
				t.Fatal("drag did not render a text selection")
			}
			connectMouse(m, tea.MouseReleaseMsg(end))
			if copied != value || strings.Contains(copied, "\x1b") || len(*requests) != 0 {
				t.Fatalf("drag copied %q, want %q; requests=%d", copied, value, len(*requests))
			}
			if m.wizardOverlay.text.selecting {
				t.Fatal("release retained selection")
			}
			m.View()
		}
	}
}

func TestWizardManualDragScrollAndReleaseOverActionOnlyCopies(t *testing.T) {
	m, requests, _ := acpInstallationWizardFixture(t)
	connectPress(m, "down")
	connectPress(m, "enter")
	m.width, m.height = 40, 18
	m.View()
	want := strings.Join(m.wizardOverlay.text.lines, "\n")
	var copied string
	m.cfg.WriteClipboardText = func(text string) error { copied = text; return nil }
	start := wizardTextPointForTest(t, m, "Download ZIP:", false)
	connectMouse(m, tea.MouseClickMsg(start))
	for range 120 {
		g := m.wizardOverlay.geometry
		mouse := tea.Mouse{X: g.contentX + g.contentWidth - 1, Y: g.actionY, Button: tea.MouseLeft}
		m.Update(tea.MouseMotionMsg(mouse))
		previous := m.wizardOverlay.window
		m.advanceSelectionAutoScroll(m.selectionAutoScroll.scheduledToken)
		if previous == m.wizardOverlay.window {
			connectMouse(m, tea.MouseReleaseMsg(mouse))
			break
		}
	}
	if copied != want || len(*requests) != 0 || !m.isACPManualSetup() {
		t.Fatalf("scrolling selection lost text or activated action: copied=%q want=%q requests=%d", copied, want, len(*requests))
	}
	if m.selectionAutoScroll.active || m.wizardOverlay.text.selecting {
		t.Fatal("selection scrolling remained active after release")
	}
}

func TestWizardHoverAndButtonsAcrossConnectSteps(t *testing.T) {
	for _, page := range []string{"source", "provider", "connection", "models", "custom model", "ACP agent", "ACP models", "runtime", "install", "manual", "retry"} {
		t.Run(page, func(t *testing.T) {
			var m *Model
			if page == "runtime" || page == "install" || page == "manual" || page == "retry" {
				m, _, _ = acpInstallationWizardFixture(t)
				if page == "manual" {
					connectPress(m, "down")
				}
				if page == "install" || page == "manual" {
					connectPress(m, "enter")
				}
				if page == "retry" {
					m.wizardOverlay.err = "Could not connect. Try again."
					m.slashArgCandidates = nil
				}
			} else {
				m, _ = wizardOverlayFixture(t)
				switch page {
				case "provider":
					connectPress(m, "enter")
				case "connection", "models", "custom model":
					connectPress(m, "enter")
					connectPress(m, "enter")
					if page != "connection" {
						connectPress(m, "tab")
						connectPaste(m, "fixture-secret")
						connectPress(m, "enter")
					}
					if page == "custom model" {
						m.slashArgIndex = len(m.slashArgCandidates)
						connectPress(m, "enter")
					}
				case "ACP agent", "ACP models":
					connectPress(m, "down")
					connectPress(m, "enter")
					if page == "ACP models" {
						connectPress(m, "enter")
					}
				}
			}
			m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
			baseline := m.View()
			t.Logf("%s:\n%s", page, ansi.Strip(m.renderWizardOverlay()))
			if baseline.MouseMode != tea.MouseModeAllMotion {
				t.Fatal("wizard did not request mouse hover events")
			}
			g := m.wizardOverlay.geometry
			targets := []struct {
				name        string
				x, y, width int
			}{{"action", g.actionX, g.actionY, g.actionWidth}, {"back", g.backX, g.backY, g.backWidth}, {"close", g.closeX, g.closeY, 3}, {"send", g.sendX, g.sendY, g.sendWidth}}
			for _, target := range targets {
				if target.name == "send" && page != "manual" {
					continue
				}
				if target.width == 0 {
					t.Fatalf("missing %s button", target.name)
				}
				m.Update(tea.MouseMotionMsg{X: target.x, Y: target.y})
				hovered := m.View().Content
				if m.wizardOverlay.hovered != target.name || hovered == baseline.Content {
					t.Fatalf("%s has no hover feedback", target.name)
				}
				terminal := vt.NewSafeEmulator(m.width, m.height)
				for _, output := range renderFullscreenFramesForTest(t, m.width, m.height, baseline.Content, hovered) {
					if _, err := terminal.Write([]byte(output)); err != nil {
						t.Fatal(err)
					}
				}
				want := colorprofile.ANSI256.Convert(m.theme.CommandActiveStyle().GetBackground())
				wr, wg, wb, _ := want.RGBA()
				for x := target.x; x < target.x+target.width; x++ {
					cell := terminal.CellAt(x, target.y)
					if cell == nil || cell.Style.Bg == nil {
						t.Fatalf("%s missing solid hover background at %d,%d: %#v", target.name, x, target.y, cell)
					}
					r, green, b, _ := cell.Style.Bg.RGBA()
					if r != wr || green != wg || b != wb {
						t.Fatalf("%s hover background at %d,%d = %v, want %v", target.name, x, target.y, cell.Style.Bg, want)
					}
				}
				terminal.Close()
				m.Update(tea.MouseMotionMsg{X: 0, Y: 0})
				if m.wizardOverlay.hovered != "" {
					t.Fatal("leaving overlay retained hover")
				}
			}
			m.clearWizard()
			if m.View().MouseMode != tea.MouseModeCellMotion {
				t.Fatal("closing wizard retained all-motion mode")
			}
		})
	}
}

func TestWizardRowsFollowHoverWithoutMovingOrEditing(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
	m.View()
	g := m.wizardOverlay.geometry
	m.Update(tea.MouseMotionMsg{X: g.contentX + 1, Y: g.rows[1]})
	frame := m.View().Content
	if m.slashArgIndex != 1 || m.wizardStepKey() != "source" || len(*submissions) != 0 || !strings.Contains(ansi.Strip(frame), "› Local ACP Agent") {
		t.Fatal("hover did not select the ACP row without activation")
	}
	if m.wizardOverlay.geometry.rows[1] != g.rows[1] {
		t.Fatal("hover moved the target row")
	}
	connectPress(m, "up")
	connectPress(m, "enter")
	connectPress(m, "enter")
	m.View()
	g = m.wizardOverlay.geometry
	field, cursor := m.wizardOverlay.field, m.wizardOverlay.cursor
	before := m.View().Content
	m.Update(tea.MouseMotionMsg{X: g.contentX + 2, Y: g.rows[1]})
	if m.View().Content == before || m.wizardOverlay.field != field || m.wizardOverlay.cursor != cursor {
		t.Fatal("field hover stole editing focus or omitted feedback")
	}
	point := tea.Mouse{X: g.contentX + 2, Y: g.rows[1], Button: tea.MouseLeft}
	connectMouse(m, tea.MouseClickMsg(point))
	connectMouse(m, tea.MouseReleaseMsg(point))
	connectPaste(m, "fixture-secret")
	m.View()
	if m.wizardOverlay.field != 1 || strings.Contains(strings.Join(m.wizardOverlay.text.lines, "\n"), "fixture-secret") || strings.Contains(strings.Join(m.wizardOverlay.text.lines, "\n"), "••") {
		t.Fatal("field click failed or credential field entered text selection")
	}
}

func TestWizardButtonsRequireMatchingLeftRelease(t *testing.T) {
	for _, cancel := range []string{"leave", "right release", "resize", "loading", "catalog refresh"} {
		t.Run(cancel, func(t *testing.T) {
			m, _ := wizardOverlayFixture(t)
			m.View()
			g := m.wizardOverlay.geometry
			point := tea.Mouse{X: g.actionX + 1, Y: g.actionY, Button: tea.MouseLeft}
			connectMouse(m, tea.MouseClickMsg(point))
			switch cancel {
			case "leave":
				m.Update(tea.MouseMotionMsg{X: 0, Y: 0, Button: tea.MouseLeft})
				m.Update(tea.MouseMotionMsg(point))
			case "right release":
				point.Button = tea.MouseRight
			case "resize":
				m.Update(tea.WindowSizeMsg{Width: 101, Height: 30})
			case "loading":
				m.slashArgLoadPending = true
			case "catalog refresh":
				m.wizardCatalogReady(nil)
			}
			connectMouse(m, tea.MouseReleaseMsg(point))
			if m.wizardStepKey() != "source" {
				t.Fatal("cancelled press activated a control")
			}
		})
	}
	m, _ := wizardOverlayFixture(t)
	connectPress(m, "enter")
	m.View()
	g := m.wizardOverlay.geometry
	point := tea.Mouse{X: g.backX, Y: g.backY, Button: tea.MouseLeft}
	connectMouse(m, tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	connectMouse(m, tea.MouseReleaseMsg(point))
	if m.wizardStepKey() != "source" {
		t.Fatal("back button failed to navigate with terminal MouseNone release")
	}
}

func TestWizardAuthenticationUsesSharedMouseControlsAndOriginalResponse(t *testing.T) {
	for _, action := range []string{"row", "continue", "back", "keyboard"} {
		t.Run(action, func(t *testing.T) {
			m, _ := wizardOverlayFixture(t)
			connectPress(m, "down")
			connectPress(m, "enter")
			connectPress(m, "enter")
			m.slashArgLoadPending = true
			responses := make(chan PromptResponse, 1)
			m.handleACPAuthSelectionRequest(acpAuthSelectionRequestMsg{
				seq: m.slashArgLoadSeq,
				request: agents.AuthenticationSelectionRequest{AgentID: "antigravity", Methods: []agents.AuthenticationMethod{
					{ID: "google-login", Name: "Google personal account", Type: agents.AuthenticationAgent},
					{ID: "api-key", Name: "API key", Type: agents.AuthenticationAgent},
				}}, response: responses,
			})
			m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
			frame := m.View()
			if !m.isWizardAuthChoice() || frame.MouseMode != tea.MouseModeAllMotion || !strings.Contains(ansi.Strip(frame.Content), "[Continue ↵]") || strings.Count(ansi.Strip(frame.Content), "Choose an authentication method") != 1 {
				t.Fatalf("authentication escaped the shared wizard:\n%s", ansi.Strip(frame.Content))
			}
			g := m.wizardOverlay.geometry
			point := tea.Mouse{X: g.contentX + 2, Y: g.rows[1], Button: tea.MouseLeft}
			m.Update(tea.MouseMotionMsg(point))
			if m.activePrompt.choiceIndex != 1 || len(responses) != 0 {
				t.Fatal("authentication hover did not select without responding")
			}
			switch action {
			case "continue":
				point.X, point.Y = g.actionX, g.actionY
			case "back":
				point.X, point.Y = g.backX, g.backY
			case "keyboard":
				connectPress(m, "enter")
			}
			if action != "keyboard" {
				connectMouse(m, tea.MouseClickMsg(point))
				connectMouse(m, tea.MouseReleaseMsg(point))
			}
			select {
			case response := <-responses:
				if action == "back" {
					if response.Err == nil || response.Line != "" {
						t.Fatalf("back did not cancel authentication: %+v", response)
					}
				} else if response.Err != nil || response.Line != "api-key" {
					t.Fatalf("wrong authentication response: %+v", response)
				}
			default:
				t.Fatal("authentication control did not respond")
			}
			if m.activePrompt != nil || m.wizardOverlay == nil || m.wizardOverlay.pressed != "" || len(responses) != 0 {
				t.Fatal("authentication did not resume connection preparation exactly once")
			}
			if strings.Contains(ansi.Strip(m.View().Content), "Google personal account") {
				t.Fatal("completed authentication prompt remained visible")
			}
		})
	}
}
