package tuiapp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestACPManualSendToAgentUsesNormalSubmissionAndPreservesDraft(t *testing.T) {
	for _, mode := range []string{"mouse", "keyboard", "narrow mouse"} {
		t.Run(mode, func(t *testing.T) {
			m, requests, _ := acpInstallationWizardFixture(t)
			connectPress(m, "down")
			connectPress(m, "enter")
			if mode == "narrow mouse" {
				m.width, m.height = 40, 18
			}
			prompt := m.wizardOverlay.runtimeSetup.InstallPrompt
			m.textarea.SetValue("unfinished draft")
			m.inputAttachments = []inputAttachment{{Name: "draft.png"}}
			draft := append([]inputAttachment(nil), m.inputAttachments...)
			var sent []Submission
			m.cfg.ExecuteLine = func(submission Submission) TaskResultMsg {
				sent = append(sent, submission)
				return TaskResultMsg{}
			}
			frame := ansi.Strip(m.renderWizardOverlay())
			if !strings.Contains(frame, "[Send to agent]") || !strings.Contains(frame, "[Check installation ↵]") {
				t.Fatalf("footer actions missing\n%s", frame)
			}
			for _, line := range strings.Split(frame, "\n") {
				if ansi.StringWidth(line) > m.width-4 {
					t.Fatalf("footer overflow\n%s", frame)
				}
			}
			t.Logf("%s:\n%s", mode, frame)
			g := m.wizardOverlay.geometry
			if mode == "keyboard" {
				connectPress(m, "tab")
				connectPress(m, "enter")
			} else {
				point := tea.Mouse{X: g.sendX + 1, Y: g.sendY, Button: tea.MouseLeft}
				connectMouse(m, tea.MouseClickMsg(point))
				connectMouse(m, tea.MouseReleaseMsg(point))
				connectMouse(m, tea.MouseReleaseMsg(point))
			}
			if m.wizardOverlay != nil || len(sent) != 1 || sent[0].Text != prompt || sent[0].DisplayText != prompt || sent[0].Mode != SubmissionModeDefault || len(sent[0].Attachments) != 0 {
				t.Fatalf("install prompt was not submitted once as normal input: %#v", sent)
			}
			if m.textarea.Value() != "unfinished draft" || !reflect.DeepEqual(m.inputAttachments, draft) || len(*requests) != 0 {
				t.Fatal("sending installation task consumed the draft or directly installed the runtime")
			}
		})
	}
}

func TestACPManualSendOnlyVisibleAndAcceptedWhenIdleAndConfigured(t *testing.T) {
	for _, test := range []struct {
		name  string
		block func(*Model)
	}{
		{"running", func(m *Model) { m.beginLiveTurn(SubmissionModeDefault, false, time.Now()) }},
		{"unknown model", func(m *Model) { m.statusView = StatusViewModel{} }},
		{"unconfigured model", func(m *Model) { m.statusView.Model = "not configured" }},
		{"unconfigured model hint", func(m *Model) { m.statusView.Model = "not configured (/connect)" }},
		{"missing key", func(m *Model) { m.statusView.MissingAPIKey = true }},
		{"switching", func(m *Model) { m.sessionSwitchPending = true }},
		{"recovering", func(m *Model) { m.sessionObservationRecovering = true }},
		{"history failure", func(m *Model) { m.sessionHistoryFailed = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, _, _ := acpInstallationWizardFixture(t)
			connectPress(m, "down")
			connectPress(m, "enter")
			m.View()
			g := m.wizardOverlay.geometry
			point := tea.Mouse{X: g.sendX + 1, Y: g.sendY, Button: tea.MouseLeft}
			connectMouse(m, tea.MouseClickMsg(point))
			// A state change between press and release must prevent submission.
			test.block(m)
			connectMouse(m, tea.MouseReleaseMsg(point))
			if m.sendACPInstallPrompt() != nil || m.wizardOverlay == nil || len(m.pendingQueue) != 0 {
				t.Fatal("ineligible task was sent or queued")
			}
			frame := ansi.Strip(m.renderWizardOverlay())
			if strings.Contains(frame, "Send to agent") || m.wizardOverlay.geometry.sendWidth != 0 {
				t.Fatalf("ineligible send action is visible\n%s", frame)
			}
		})
	}
}

func TestACPManualCheckClickDoesNotUseSendKeyboardFocus(t *testing.T) {
	m, requests, installed := acpInstallationWizardFixture(t)
	connectPress(m, "down")
	connectPress(m, "enter")
	connectPress(m, "tab")
	m.View()
	g := m.wizardOverlay.geometry
	*installed = true
	point := tea.Mouse{X: g.actionX + 1, Y: g.actionY, Button: tea.MouseLeft}
	connectMouse(m, tea.MouseClickMsg(point))
	connectMouse(m, tea.MouseReleaseMsg(point))
	if m.wizardStepKey() != "acp_model" || len(*requests) != 1 || (*requests)[0].Install != nil {
		t.Fatal("Check installation followed Send keyboard focus")
	}
}
