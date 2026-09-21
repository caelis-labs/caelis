package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWizardLoadingAnimatesCatalogHandshakeAndSubmission(t *testing.T) {
	for _, width := range []int{40, 100} {
		for _, phase := range []string{"connect catalog", "disconnect catalog", "handshake", "connect submit", "disconnect submit"} {
			for _, outcome := range []string{"success", "failure", "no animation"} {
				t.Run(fmt.Sprintf("%d/%s/%s", width, phase, outcome), func(t *testing.T) {
					m, _ := wizardOverlayFixture(t)
					m.width, m.height = width, 30
					m.noAnimation = outcome == "no animation"
					complete := m.cfg.SlashArgComplete
					m.cfg.SlashArgComplete = func(ctx context.Context, command, query string, limit int) ([]SlashArgCandidate, error) {
						if command == "connect-acp-launcher:codex" {
							return []SlashArgCandidate{{Value: "hosted", Display: "Built in"}, {Value: "command", Display: "Custom command"}}, nil
						}
						if strings.HasPrefix(command, "disconnect") {
							if command == "disconnect" {
								return []SlashArgCandidate{{Value: "acp", Display: "ACP Agents"}}, nil
							}
							return []SlashArgCandidate{{Value: "antigravity", Display: "Antigravity"}}, nil
						}
						return complete(ctx, command, query, limit)
					}
					var cmd tea.Cmd
					label := "Loading"
					switch phase {
					case "connect catalog", "disconnect catalog":
						m.clearWizard()
						cmd = m.openSlashArgPicker(strings.Fields(phase)[0])
					case "handshake", "connect submit":
						connectPress(m, "down")
						connectPress(m, "enter")
						connectPress(m, "enter")
						_, cmd = m.Update(connectKey("enter"))
						label = "Preparing"
						if phase == "connect submit" {
							runConnectTestCmd(m, cmd)
							_, cmd = m.Update(connectKey("enter"))
							label = "Connecting"
						}
					case "disconnect submit":
						m.clearWizard()
						runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
						connectPress(m, "enter")
						_, cmd = m.Update(connectKey("enter"))
						label = "Disconnecting"
					}
					if cmd == nil || !m.wizardBusy() || m.spinnerTickScheduled == m.noAnimation {
						t.Fatal("operation did not schedule its busy animation")
					}
					frames := []string{m.View().Content}
					before := m.runningFrame()
					plain := ansi.Strip(m.renderWizardOverlay())
					if !strings.Contains(plain, before+" "+label) {
						t.Fatalf("missing loading indicator:\n%s", plain)
					}
					t.Logf("%s:\n%s", phase, plain)
					_, next := m.Update(m.spinner.Tick())
					if !m.noAnimation && (next == nil || m.runningFrame() == before) {
						t.Fatal("busy frame did not animate")
					}
					frames = append(frames, m.View().Content)
					if outcome == "failure" {
						err := errors.New("Connection unavailable")
						if m.wizardOverlay.pending {
							m.Update(TaskResultMsg{localID: m.wizardOverlay.submitID, Err: err})
						} else if m.slashArgLoadPending {
							m.Update(slashArgLoadResultMsg{seq: m.slashArgLoadSeq, command: m.slashArgLoadCommand, err: err})
						} else {
							m.Update(slashArgCompletionResultMsg{seq: m.slashArgRequestSeq, command: m.slashArgRequestCommand, query: m.slashArgRequestQuery, err: err})
						}
					} else {
						runConnectTestCmd(m, cmd)
					}
					if m.wizardOverlay != nil && m.wizardBusy() {
						t.Fatal("settled request remains busy")
					}
					if _, next := m.Update(m.spinner.Tick()); next != nil || m.spinnerTickScheduled {
						t.Fatal("settled request keeps animating")
					}
					frames = append(frames, m.View().Content)
					physical := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
					for i, frame := range frames {
						assertPhysicalFullscreenFrame(t, m.width, m.height, frame, physical[:i+1])
					}
				})
			}
		}
	}
}

func TestWizardLoadingCancelDiscardsLateHandshakeResult(t *testing.T) {
	m, _ := wizardOverlayFixture(t)
	complete := m.cfg.SlashArgComplete
	m.cfg.SlashArgComplete = func(ctx context.Context, command, query string, limit int) ([]SlashArgCandidate, error) {
		if command == "connect-acp-launcher:codex" {
			return []SlashArgCandidate{{Value: "hosted", Display: "Built in"}, {Value: "command", Display: "Custom command"}}, nil
		}
		return complete(ctx, command, query, limit)
	}
	connectPress(m, "down")
	connectPress(m, "enter")
	connectPress(m, "enter")
	_, pending := m.Update(connectKey("enter"))
	connectPress(m, "esc")
	if m.slashArgLoadPending || m.animationIndicatorActive() {
		t.Fatal("Back did not stop handshake loading")
	}
	step := m.wizardStepKey()
	runConnectTestCmd(m, pending)
	if m.wizardStepKey() != step || m.slashArgLoadPending {
		t.Fatal("late handshake reopened loading step")
	}
}
