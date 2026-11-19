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

func TestWizardHoverAndButtonsAcrossConnectSteps(t *testing.T) {
	for _, page := range []string{"source", "provider", "connection", "models", "custom model", "ACP agent", "ACP models"} {
		t.Run(page, func(t *testing.T) {
			m, _ := wizardOverlayFixture(t)
			switch page {
			case "provider":
				connectPress(m, "enter")
			case "connection", "models", "custom model":
				connectPress(m, "enter")
				connectPress(m, "enter")
				connectPress(m, "enter") // endpoint
				if page != "connection" {
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
			}{{"action", g.actionX, g.actionY, g.actionWidth}, {"back", g.backX, g.backY, g.backWidth}, {"close", g.closeX, g.closeY, 3}}
			for _, target := range targets {
				if target.width == 0 {
					t.Fatalf("missing %s button", target.name)
				}
				m.Update(tea.MouseMotionMsg{X: target.x, Y: target.y})
				hovered := m.View().Content
				if (target.name == "close" && !m.wizardOverlay.closeButton.hovered || target.name != "close" && m.wizardOverlay.hovered != target.name) || hovered == baseline.Content {
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
	if m.slashArgIndex != 1 || m.wizardStepKey() != "source" || len(*submissions) != 0 || !strings.Contains(ansi.Strip(frame), "› local acp agent") {
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
	m.Update(tea.MouseMotionMsg{X: g.backX, Y: g.backY})
	if m.View().Content == before || m.wizardOverlay.field != field || m.wizardOverlay.cursor != cursor {
		t.Fatal("hover stole endpoint editing focus or omitted feedback")
	}
	connectPress(m, "enter") // endpoint
	connectPress(m, "tab")   // move focus away from the credential field
	m.View()
	g = m.wizardOverlay.geometry
	point := tea.Mouse{X: g.contentX + 2, Y: g.rows[0], Button: tea.MouseLeft}
	connectMouse(m, tea.MouseClickMsg(point))
	connectMouse(m, tea.MouseReleaseMsg(point))
	connectPaste(m, "fixture-secret")
	m.View()
	if m.wizardOverlay.field != 0 || strings.Contains(strings.Join(m.wizardOverlay.text.lines, "\n"), "fixture-secret") || strings.Contains(strings.Join(m.wizardOverlay.text.lines, "\n"), "••") {
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
				request: agents.AuthenticationSelectionRequest{AgentID: "codex", Methods: []agents.AuthenticationMethod{
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
