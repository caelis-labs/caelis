package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/charmbracelet/x/ansi"
)

func TestResumeLoadingDismissesWelcomeAndAnimatesUntilSettled(t *testing.T) {
	for _, width := range []int{35, 80} {
		for _, outcome := range []string{"ready", "history failure", "request failure", "no animation"} {
			t.Run(fmt.Sprintf("%d/%s", width, outcome), func(t *testing.T) {
				m := newWelcomeTestModel(t, width, 24, Config{
					ListSessions: func(context.Context) ([]ResumeCandidate, error) {
						return []ResumeCandidate{{SessionID: "restored", Title: "Earlier work"}}, nil
					},
					ExecuteLine: func(Submission) TaskResultMsg {
						return TaskResultMsg{Err: errors.New("Session unavailable")}
					},
				})
				m.noAnimation = outcome == "no animation"
				frames := []string{m.View().Content}
				if !welcomeFrameVisible(frames[0]) {
					t.Fatal("fixture did not render the launch card")
				}
				_, list := m.submitLine("/resume")
				m.Update(list())
				_, attach := m.Update(keyPress("enter"))
				if attach == nil || !m.sessionSwitchPending {
					t.Fatal("selection did not begin attachment")
				}
				assertLoading := func() {
					t.Helper()
					frame := m.View().Content
					if welcomeFrameVisible(frame) || m.welcomeCardPending {
						t.Fatal("Welcome remained visible during Session attachment")
					}
					if !strings.Contains(ansi.Strip(frame), m.runningFrame()+" "+sessionHistoryLoadingHint) {
						t.Fatalf("missing animated history hint:\n%s", ansi.Strip(frame))
					}
					frames = append(frames, frame)
				}
				// The launch card must leave before executing any remote work.
				assertLoading()
				if m.spinnerTickScheduled == m.noAnimation {
					t.Fatal("initial spinner scheduling ignored the animation preference")
				}
				if !m.noAnimation {
					before := m.runningFrame()
					_, next := m.Update(m.spinner.Tick())
					if next == nil || !m.spinnerTickScheduled || m.runningFrame() == before {
						t.Fatal("spinner did not advance while waiting for the Session")
					}
					assertLoading()
				}
				if outcome == "request failure" {
					applySessionSelectionCommandForTest(t, m, attach)
				} else {
					m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "restored"}})
					assertLoading()
					if !m.noAnimation {
						if _, next := m.Update(m.spinner.Tick()); next == nil {
							t.Fatal("spinner stopped before the history was ready")
						}
						assertLoading()
					}
					if outcome == "history failure" {
						m.Update(sessionObservationErrorMsg{err: errors.New("History unavailable")})
					} else {
						m.Update(sessionHistoryReadyMsg{})
					}
				}
				if m.sessionHistory != nil || m.sessionSwitchPending || strings.Contains(m.buildHintText(), sessionHistoryLoadingHint) {
					t.Fatal("settled attachment retained its loading state")
				}
				if _, next := m.Update(m.spinner.Tick()); next != nil || m.spinnerTickScheduled {
					t.Fatal("settled attachment kept scheduling spinner ticks")
				}
				frames = append(frames, m.View().Content)
				if welcomeFrameVisible(frames[len(frames)-1]) {
					t.Fatal("settled attachment resurrected Welcome")
				}
				updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
				for i, frame := range frames {
					assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates[:i+1])
				}
			})
		}
	}
}

// Drive the initial Bubble Tea batch, leaving subsequent animation ticks to
// the caller instead of running a recurring command loop in the test.
func applySessionSelectionCommandForTest(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing Session selection command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			applySessionSelectionCommandForTest(t, m, child)
		}
		return
	}
	m.Update(msg)
}
