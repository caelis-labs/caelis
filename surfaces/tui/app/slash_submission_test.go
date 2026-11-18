package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSlashSubmissionEchoOmitsSensitiveArguments(t *testing.T) {
	for _, line := range []string{"/CONNECT openai test-model https://example.invalid secret-value", "/plugin marketplace add https://secret-value@example.invalid"} {
		t.Run(slashCommandName(line), func(t *testing.T) {
			var submitted string
			m := NewModel(Config{NoColor: true, NoAnimation: true, ExecuteLine: func(sub Submission) TaskResultMsg {
				submitted = sub.Text
				return TaskResultMsg{}
			}})
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			_, dispatch := m.submitInteractiveLine(line, line, nil)
			name := "/" + slashCommandName(line)
			if frame := ansi.Strip(m.View().Content); countUserNarrativeBlocksForTest(m, name) != 1 || strings.Contains(frame, "secret-value") {
				t.Fatalf("expected one command-only echo:\n%s", frame)
			}
			_, execute := m.Update(dispatch())
			m.Update(execute())
			if submitted != line {
				t.Fatalf("executed %q, want %q", submitted, line)
			}
		})
	}
}

func TestSlashPromptWaitsForControlEcho(t *testing.T) {
	for _, line := range []string{"/orbit inspect", "/review inspect", "/agent-handle inspect", "/status-like inspect"} {
		t.Run(slashCommandName(line), func(t *testing.T) {
			m := NewModel(Config{Commands: append(DefaultCommands(), "agent-handle"), NoAnimation: true, ExecuteLine: func(Submission) TaskResultMsg {
				return TaskResultMsg{}
			}})
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			_, cmd := m.submitInteractiveLine(line, line, nil)
			if cmd == nil || countUserNarrativeBlocksForTest(m, line) != 0 || strings.Contains(m.buildHintText(), "Running command") {
				t.Fatal("slash prompt must wait for Control before adding a user message")
			}
			const accepted = "inspect [image #1]"
			m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
				Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, SourceEventID: "accepted-input",
				NarrativeKind: TranscriptNarrativeUser, Text: accepted, Final: true,
			}}})
			if countUserNarrativeBlocksForTest(m, accepted) != 1 || countUserNarrativeBlocksForTest(m, line) != 0 {
				t.Fatal("expected only the accepted Control echo")
			}
		})
	}
}
