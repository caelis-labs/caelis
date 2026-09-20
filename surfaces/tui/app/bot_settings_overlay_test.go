package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/charmbracelet/x/ansi"
)

func botSettingsFixture(t *testing.T) (*Model, *fakeBotClient) {
	t.Helper()
	value := bot.Bot{ID: "bot-1", SessionID: "chat-1", Revision: 7, ModelSelector: "openai/sol", Config: bot.Config{Name: "Ada", Description: "old", Model: "durable-sol", Effort: "high", Fast: true}}
	client := &fakeBotClient{bots: []bot.Bot{value}}
	m := newBotTestModel(t, 100, 30, client, func(s Submission) TaskResultMsg {
		t.Fatalf("Bot settings reached work command: %s", s.Text)
		return TaskResultMsg{}
	})
	stageBotForGolden(m, value)
	m.cfg.SlashArgComplete = func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
		candidates := modelPickerCandidates()
		candidates[0].ModelConfigID, candidates[1].ModelConfigID = "durable-flash", "durable-sol"
		return append(candidates, SlashArgCandidate{Value: "acp:codex", Display: "Codex"}), nil
	}
	return m, client
}

func TestBotSettingsFormKeepsDraftAndSavesOnce(t *testing.T) {
	m, client := botSettingsFixture(t)
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	connectPress(m, "ctrl+u")
	connectPaste(m, "Ada Lovelace")
	connectPress(m, "tab")
	connectPress(m, "ctrl+u") // Empty clears the description directly.
	connectPress(m, "tab")
	connectPress(m, "enter")
	if len(m.slashArgCandidates) != 2 || m.slashArgIndex != 1 {
		t.Fatalf("provider/current selection: %+v", m.slashArgCandidates)
	}
	connectPress(m, "left")
	connectPress(m, "esc")
	if m.wizardOverlay.fields[0].value != "Ada Lovelace" || m.wizardOverlay.fields[1].value != "" {
		t.Fatal("model Back lost fields")
	}
	// The model edit was cancelled; editing metadata preserves its options.
	m.wizardOverlay.field = len(m.wizardOverlay.fields)
	connectPress(m, "enter")
	if len(client.updated) != 1 {
		t.Fatalf("updates: %+v", client.updated)
	}
	got := client.updated[0]
	if got.Config.Name != "Ada Lovelace" || got.Config.Description != "" || got.Config.Model != "durable-sol" || got.Config.Effort != "high" || !got.Config.Fast || *got.ExpectedRevision != 7 || got.SessionID != "chat-1" {
		t.Fatalf("saved: %+v", got)
	}
	if m.wizardOverlay != nil {
		t.Fatal("saved form remained open")
	}
}

func TestBotModelOptionsUseBotIdentity(t *testing.T) {
	m, client := botSettingsFixture(t)
	runConnectTestCmd(m, m.startBotSettingsFlow(true))
	connectPress(m, "left")
	connectPress(m, "tab")
	connectPress(m, "right")
	connectPaste(m, "Sol")
	connectPress(m, "enter")
	if len(client.updated) != 1 {
		t.Fatalf("updates: %+v", client.updated)
	}
	got := client.updated[0].Config
	if got.Model != "durable-sol" || got.Effort != "medium" || got.Fast || got.Name != "Ada" || got.Description != "old" {
		t.Fatalf("model options: %+v", got)
	}
}

func TestBotChangingModelDropsUnsupportedOptions(t *testing.T) {
	m, client := botSettingsFixture(t)
	runConnectTestCmd(m, m.startBotSettingsFlow(true))
	connectPress(m, "up")
	connectPress(m, "enter")
	if len(client.updated) != 1 {
		t.Fatalf("updates: %+v", client.updated)
	}
	config := client.updated[0].Config
	if config.Model != "durable-flash" || config.Effort != "none" || config.Fast {
		t.Fatalf("new model options: %+v", config)
	}
}

func TestBotModelSearchUsesFilteredCatalog(t *testing.T) {
	for _, modelOnly := range []bool{true, false} {
		t.Run(fmt.Sprint(modelOnly), func(t *testing.T) {
			m, client := botSettingsFixture(t)
			m.cfg.SlashArgComplete = func(_ context.Context, _ string, query string, _ int) ([]SlashArgCandidate, error) {
				candidates := modelPickerCandidates()
				candidates[0].Value, candidates[0].Display = "deepseek/deepseek-v4-flash", "DeepSeek V4 Flash"
				candidates[0].ModelConfigID, candidates[1].ModelConfigID = "durable-flash", "durable-sol"
				// Control returns only candidates matching the requested query.
				var filtered []SlashArgCandidate
				for _, c := range candidates {
					if strings.Contains(strings.ToLower(c.Value), query) || strings.Contains(strings.ToLower(c.Display), query) {
						filtered = append(filtered, c)
					}
				}
				return filtered, nil
			}
			runConnectTestCmd(m, m.startBotSettingsFlow(modelOnly))
			if !modelOnly {
				connectPress(m, "tab")
				connectPress(m, "tab")
				connectPress(m, "enter")
			}
			connectPaste(m, "durable-sol")
			if len(m.slashArgCandidates) != 0 {
				t.Fatalf("search fabricated an unavailable current model: %+v", m.slashArgCandidates)
			}
			connectPress(m, "ctrl+u")
			if len(m.slashArgCandidates) != 2 {
				t.Fatalf("clearing search did not restore the catalog: %+v", m.slashArgCandidates)
			}
			connectPaste(m, "v4")
			if len(m.slashArgCandidates) != 1 || m.slashArgCandidates[0].ModelConfigID != "durable-flash" {
				t.Fatalf("substring search lost matching model: %+v", m.slashArgCandidates)
			}
			if frame := ansi.Strip(m.renderWizardOverlay()); !strings.Contains(frame, "DeepSeek V4 Flash") {
				t.Fatalf("matching model missing from overlay:\n%s", frame)
			}
			connectPress(m, "enter")
			if !modelOnly {
				connectPress(m, "tab")
				connectPress(m, "tab")
				connectPress(m, "enter")
			}
			if len(client.updated) != 1 || client.updated[0].Config.Model != "durable-flash" || client.updated[0].Config.Effort != "none" || client.updated[0].Config.Fast {
				t.Fatalf("selected model not saved: %+v", client.updated)
			}
		})
	}
}

func TestBotCreateFormKeyboardAndCancel(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			client := &fakeBotClient{}
			m := newBotTestModel(t, 80, 24, client, nil)
			m.startBotCreateFlow()
			m.wizardOverlay.field = 3
			connectPress(m, "enter")
			if m.wizardOverlay.err == "" || len(client.created) != 0 {
				t.Fatal("blank name accepted")
			}
			connectPaste(m, "Ada")
			connectPress(m, "tab")
			connectPaste(m, "Be kind.\nKeep replies brief.")
			if cancel {
				connectPress(m, "esc")
				if len(client.created) != 0 {
					t.Fatal("cancel created Bot")
				}
				return
			}
			connectPress(m, "tab")
			connectPress(m, "tab")
			connectPress(m, "enter")
			if len(client.created) != 1 || client.created[0].Config != (bot.Config{Name: "Ada", Description: "Be kind.\nKeep replies brief."}) {
				t.Fatalf("creation: %+v", client.created)
			}
		})
	}
}

func TestBotModelUnavailableAndCatalogError(t *testing.T) {
	for _, catalogErr := range []bool{false, true} {
		t.Run(fmt.Sprint(catalogErr), func(t *testing.T) {
			m, client := botSettingsFixture(t)
			m.cfg.SlashArgComplete = func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
				if catalogErr {
					return nil, errors.New("catalog unavailable")
				}
				return nil, nil
			}
			runConnectTestCmd(m, m.startBotSettingsFlow(false))
			connectPress(m, "ctrl+u")
			connectPaste(m, "Renamed")
			connectPress(m, "tab")
			connectPress(m, "tab")
			connectPress(m, "enter")
			if catalogErr {
				connectPress(m, "enter")
				if len(client.updated) != 0 {
					t.Fatal("failed catalog saved")
				}
			} else if len(m.slashArgCandidates) != 1 || m.slashArgCandidates[0].ModelConfigID != "durable-sol" {
				t.Fatal("missing current silently rebound")
			}
			connectPress(m, "esc")
			m.wizardOverlay.field = len(m.wizardOverlay.fields)
			connectPress(m, "enter")
			if len(client.updated) != 1 || client.updated[0].Config.Model != "durable-sol" || !client.updated[0].Config.Fast {
				t.Fatalf("metadata edit: %+v", client.updated)
			}
		})
	}
}

func TestBotSettingsUnknownOutcomeCannotResubmit(t *testing.T) {
	m, client := botSettingsFixture(t)
	client.updateOutcome = appserver.OutcomeUnknown
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	connectPaste(m, " edited")
	m.wizardOverlay.field = len(m.wizardOverlay.fields)
	connectPress(m, "enter")
	if !m.wizardOverlay.blocked || len(client.updated) != 1 {
		t.Fatal("unknown result not blocked")
	}
	connectPress(m, "enter")
	if m.wizardOverlay != nil || len(client.updated) != 1 {
		t.Fatal("unknown result resubmitted")
	}
}

func TestBotSettingsStaleLoadDoesNotReopen(t *testing.T) {
	m, _ := botSettingsFixture(t)
	load := m.startBotSettingsFlow(false)
	connectPress(m, "esc")
	m.Update(load())
	if m.wizardOverlay != nil {
		t.Fatal("stale settings reopened")
	}
}

func TestBotModelPickerGoldenFrames(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 24}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := botSettingsFixture(t)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			runConnectTestCmd(m, m.startBotSettingsFlow(true))
			checkBotGolden(t, fmt.Sprintf("model_%dx%d", size[0], size[1]), size[0], size[1], m.View().Content)
		})
	}
}

func TestBotSlashCompletionOpensAndConsumesCommand(t *testing.T) {
	for _, command := range []string{"new", "settings", "model"} {
		t.Run(command, func(t *testing.T) {
			m, _ := botSettingsFixture(t)
			typeComposerThroughUpdate(t, m, "/"+command)
			connectPress(m, "enter")
			if m.wizardOverlay == nil || strings.TrimSpace(m.textarea.Value()) != "" {
				t.Fatal("completion did not open clean overlay")
			}
			connectPress(m, "esc")
			if m.wizardOverlay != nil || m.textarea.Value() != "" {
				t.Fatal("cancel restored slash")
			}
		})
	}
}
