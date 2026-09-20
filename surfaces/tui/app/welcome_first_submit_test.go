package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// welcomeFrameVisible reports whether a rendered frame still contains the
// Welcome launch card. The responsive wordmark and an action row are stable
// markers across terminal sizes.
func welcomeFrameVisible(content string) bool {
	plain := ansi.Strip(content)
	return strings.Contains(plain, welcomeWordmarkASCII[0]) ||
		strings.Contains(plain, welcomeActionMarker+welcomeActions[0].label)
}

// TestFirstPromptSubmissionDoesNotResurrectWelcome pins the Welcome launch
// card lifecycle across the first prompt. Submitting the prompt dismisses the
// card locally; the accepted turn is then promoted to a new Session, which
// resets the conversation view before the echoed transcript replays. That
// reset must not resurrect the launch card the submission already dismissed,
// or the card flickers away and back before the echo removes it again.
func TestFirstPromptSubmissionDoesNotResurrectWelcome(t *testing.T) {
	for _, eventBeforeReady := range []bool{false, true} {
		name := "event after history ready"
		if eventBeforeReady {
			name = "event before history ready"
		}
		t.Run(name, func(t *testing.T) {
			const width, height = 80, 24
			model := newWelcomeTestModel(t, width, height, Config{Version: "dev"})

			type step struct {
				name    string
				content string
				welcome bool
				users   int
			}
			var steps []step
			record := func(name string, welcome bool, users int) {
				if got := countUserNarrativeBlocksForTest(model, "inspect the repository"); got != users {
					t.Fatalf("%s user blocks = %d, want %d", name, got, users)
				}
				steps = append(steps, step{name: name, content: model.View().Content, welcome: welcome, users: users})
			}

			record("launch", true, 0)

			updated, _ := model.submitInteractiveLine("inspect the repository", "inspect the repository", nil)
			model = updated.(*Model)
			record("local submit", false, 0)

			// The first accepted prompt is promoted to a new Session; the view sees the
			// atomic Session start before the echoed transcript replays.
			model.Update(sessionViewStartMsg{
				generation: 1,
				state:      appserver.SessionState{SessionID: "s-first", Run: appserver.RunState{Active: true}},
				automatic:  true,
			})
			if !eventBeforeReady {
				model.Update(sessionHistoryReadyMsg{})
			}
			record("session attach", false, 0)

			event := TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
				ScopeID: "s-first", TurnID: "turn-1", SourceEventID: "user-1",
				NarrativeKind: TranscriptNarrativeUser, Text: "inspect the repository", Final: true}
			model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}, ReconnectReplay: eventBeforeReady})
			if eventBeforeReady {
				record("private history event", false, 0)
				model.Update(sessionHistoryReadyMsg{})
				record("history ready after event", false, 1)
			} else {
				record("gateway event", false, 1)
			}
			model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
			record("retransmitted event", false, 1)
			event.SourceEventID = "user-2"
			model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
			record("distinct identical text event", false, 2)

			if got := len(model.doc.FindByKind(BlockWelcome)); got != 0 {
				t.Fatalf("welcome blocks after first prompt lifecycle = %d, want 0", got)
			}

			terminal := vt.NewSafeEmulator(width, height)
			t.Cleanup(func() { _ = terminal.Close() })

			frames := make([]string, len(steps))
			for i, s := range steps {
				frames[i] = s.content
			}
			updates := renderFullscreenFramesForTest(t, width, height, frames...)
			for i, s := range steps {
				if _, err := terminal.Write([]byte(updates[i])); err != nil {
					t.Fatalf("write frame %q to physical terminal: %v", s.name, err)
				}
				if got := strings.Count(ansi.Strip(terminal.Render()), "inspect the repository"); got != s.users {
					t.Fatalf("physical frame %q user lines = %d, want %d\n%s", s.name, got, s.users, ansi.Strip(terminal.Render()))
				}
				got := welcomeFrameVisible(terminal.Render())
				if got != s.welcome {
					t.Fatalf("physical frame %q welcome visible = %v, want %v\n%s",
						s.name, got, s.welcome, ansi.Strip(terminal.Render()))
				}
			}
		})
	}
}

// TestSessionAttachClearsDeferredWelcome covers the unsized start: an
// unattached reset with no viewport width defers the Welcome card, and a
// Session attach must drop that deferral so a later resize cannot resurrect
// the launch screen.
func TestSessionAttachClearsDeferredWelcome(t *testing.T) {
	model := NewModel(Config{ShowWelcomeCard: true, NoColor: true, NoAnimation: true})
	model.viewport.SetWidth(0)
	model.resetConversationView()
	if !model.welcomeCardPending {
		t.Fatal("unattached unsized reset did not defer the Welcome card")
	}

	model.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "s-first"}})
	if model.welcomeCardPending {
		t.Fatal("Session attach retained deferred Welcome while loading history")
	}
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if welcomeFrameVisible(model.View().Content) {
		t.Fatal("resize resurrected Welcome while loading history")
	}
	model.Update(sessionHistoryReadyMsg{})
	if model.welcomeCardPending {
		t.Fatal("Session attach left a deferred Welcome card")
	}

	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := len(model.doc.FindByKind(BlockWelcome)); got != 0 {
		t.Fatalf("resize after Session attach resurrected %d Welcome blocks, want 0", got)
	}
}
