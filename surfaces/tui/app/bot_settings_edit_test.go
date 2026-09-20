package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/charmbracelet/x/ansi"
)

func TestBotSettingsLongDescriptionNavigationPreservesValue(t *testing.T) {
	for _, description := range []string{strings.Repeat("x", 9000), strings.Repeat("一\n二", 4000)} {
		for _, code := range []rune{tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd} {
			t.Run(fmt.Sprintf("%d/%d", len(description), code), func(t *testing.T) {
				m, client := botSettingsFixture(t)
				client.bots[0].Config.Description = description
				if _, err := bot.Normalize(client.bots[0].Config); err != nil {
					t.Fatal(err)
				}
				for _, rename := range []bool{false, true} {
					runConnectTestCmd(m, m.startBotSettingsFlow(false))
					if rename {
						connectPaste(m, " renamed")
					}
					connectPress(m, "tab")
					m.Update(tea.KeyPressMsg(tea.Key{Code: code}))
					if got := m.wizardOverlay.fields[1].value; got != description {
						t.Fatalf("navigation changed description: before=%d after=%d bytes", len(description), len(got))
					}
					connectPress(m, "tab")
					connectPress(m, "tab")
					connectPress(m, "tab")
					connectPress(m, "enter")
					if m.wizardOverlay != nil {
						t.Fatal("save did not close settings")
					}
					if !rename && len(client.updated) != 0 {
						t.Fatal("navigation alone saved a configuration change")
					}
					if rename && (len(client.updated) != 1 || client.updated[0].Config.Description != description) {
						t.Fatal("rename failed to preserve the complete description")
					}
				}
			})
		}
	}
}

func TestBotDescriptionByteLimitRejectsEditsWithoutTruncation(t *testing.T) {
	for _, description := range []string{strings.Repeat("x", 64*1024-1), strings.Repeat("界", (64*1024-1)/3)} {
		t.Run(fmt.Sprint(utf8.RuneCountInString(description)), func(t *testing.T) {
			m, client := botSettingsFixture(t)
			m.startBotCreateFlow()
			connectPaste(m, "New Bot")
			connectPress(m, "tab")
			connectPaste(m, description)
			m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
			want := description + "a"
			if got := m.wizardOverlay.fields[1].value; got != want {
				t.Fatalf("valid description truncated: want=%d got=%d bytes", len(want), len(got))
			}
			if _, err := bot.Normalize(bot.Config{Name: "New Bot", Description: want}); err != nil {
				t.Fatal(err)
			}
			cursor := m.wizardOverlay.cursor
			m.Update(tea.KeyPressMsg(tea.Key{Code: '界', Text: "界"}))
			if m.wizardOverlay.fields[1].value != want || m.wizardOverlay.cursor != cursor || m.wizardOverlay.err == "" {
				t.Fatal("over-limit typing must reject the edit and preserve value and cursor")
			}
			m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
			connectPaste(m, "over-limit paste")
			if m.wizardOverlay.fields[1].value != want || m.wizardOverlay.cursor != 0 || m.wizardOverlay.err == "" {
				t.Fatal("over-limit paste must reject the edit and preserve value and cursor")
			}
			if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "64 KiB") {
				t.Fatalf("limit error missing from rendered form:\n%s", frame)
			}
			m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
			m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
			connectPaste(m, "b")
			if m.wizardOverlay.err != "" {
				t.Fatal("valid correction did not clear the error")
			}
			connectPress(m, "tab")
			connectPress(m, "tab")
			connectPress(m, "enter")
			if len(client.created) != 1 || client.created[0].Config.Description != description+"b" {
				t.Fatal("save did not retain the full corrected description")
			}
		})
	}
}

func TestBotMultilineDescriptionCursorMatchesEditing(t *testing.T) {
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, client := botSettingsFixture(t)
			m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			client.bots[0].Config.Description = "a\n界b\nc"
			runConnectTestCmd(m, m.startBotSettingsFlow(false))
			connectPress(m, "tab")
			steps := []struct {
				key           tea.Key
				value, visual string
			}{
				{tea.Key{}, "a\n界b\nc", "a ↵ 界b ↵ c▏"},
				{tea.Key{Code: tea.KeyLeft}, "a\n界b\nc", "a ↵ 界b ↵ ▏c"},
				{tea.Key{Code: tea.KeyLeft}, "a\n界b\nc", "a ↵ 界b▏ ↵ c"},
				{tea.Key{Code: tea.KeyLeft}, "a\n界b\nc", "a ↵ 界▏b ↵ c"},
				{tea.Key{Code: 'X', Text: "X"}, "a\n界Xb\nc", "a ↵ 界X▏b ↵ c"},
				{tea.Key{Code: tea.KeyBackspace}, "a\n界b\nc", "a ↵ 界▏b ↵ c"},
				{tea.Key{Code: tea.KeyHome}, "a\n界b\nc", "▏a ↵ 界b ↵ c"},
				{tea.Key{Code: tea.KeyRight}, "a\n界b\nc", "a▏ ↵ 界b ↵ c"},
				{tea.Key{Code: tea.KeyRight}, "a\n界b\nc", "a ↵ ▏界b ↵ c"},
				{tea.Key{Code: tea.KeyBackspace}, "a界b\nc", "a▏界b ↵ c"},
				{tea.Key{Code: tea.KeyDelete}, "ab\nc", "a▏b ↵ c"},
				{tea.Key{Code: tea.KeyEnd}, "ab\nc", "ab ↵ c▏"},
			}
			for i, step := range steps {
				if i > 0 {
					m.Update(tea.KeyPressMsg(step.key))
				}
				if got := m.wizardOverlay.fields[1].value; got != step.value {
					t.Fatalf("step %d: value=%q want=%q", i, got, step.value)
				}
				if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, step.visual) {
					t.Fatalf("step %d: missing %q in rendered form:\n%s", i, step.visual, frame)
				}
			}
			connectPress(m, "tab")
			connectPress(m, "tab")
			connectPress(m, "tab")
			connectPress(m, "enter")
			if len(client.updated) != 1 || client.updated[0].Config.Description != "ab\nc" {
				t.Fatal("save did not retain multiline edits")
			}
		})
	}
}
