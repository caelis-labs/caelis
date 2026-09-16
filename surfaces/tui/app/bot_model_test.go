package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/bot"
)

// TestBotModelDisplayUsesControlSelectorPreservingDurableID proves Bot model
// chrome renders Control's public selector for a resolved durable model
// identity and never rewrites that durable identity.
func TestBotModelDisplayUsesControlSelectorPreservingDurableID(t *testing.T) {
	const (
		durable  = "openai@default/openai/gpt-5.4"
		selector = "openai/gpt-5.4"
	)
	value := bot.Bot{
		ID: "bot-1", SessionID: "chat-1", Revision: 4,
		Config: bot.Config{Name: "Ada", Model: durable}, ModelSelector: selector,
	}
	model := newBotTestModel(t, 80, 24, &fakeBotClient{bots: []bot.Bot{value}}, nil)
	model.bot.active = value
	model.bot.hasActive = true

	if got := model.botModelText(); got != selector {
		t.Fatalf("botModelText = %q, want the public selector %q", got, selector)
	}
	if identity := model.botIdentityText(); !strings.Contains(identity, selector) || strings.Contains(identity, durable) {
		t.Fatalf("footer identity = %q, want %q without the durable ID", identity, selector)
	}
	if active, _ := model.activeBot(); active.Config.Model != durable {
		t.Fatalf("display changed the durable model identity: %q", active.Config.Model)
	}
	if _, _, ok := model.showBotStatus(); !ok {
		t.Fatal("status was refused for an active Bot")
	}
	model.syncViewportContent()
	frame := ansi.Strip(model.View().Content)
	if !strings.Contains(frame, selector) || strings.Contains(frame, durable) {
		t.Fatalf("chrome and /status frame = %q, want %q without the durable ID", frame, selector)
	}
	rows := model.botPickerRows([]bot.Bot{value})
	if len(rows) != 1 || rows[0].Prompt != selector {
		t.Fatalf("picker rows = %+v, want Prompt %q", rows, selector)
	}
}

// TestBotModelDisplayFallsBackToDurableID proves a Bot read without a resolved
// selector shows the durable ID rather than an invented or parsed one.
func TestBotModelDisplayFallsBackToDurableID(t *testing.T) {
	const durable = "openai@default/openai/gpt-5.4"
	value := bot.Bot{Config: bot.Config{Name: "Ada", Model: durable}}
	if got := botModelDisplay(value); got != durable {
		t.Fatalf("botModelDisplay = %q, want the durable fallback %q", got, durable)
	}
	if got := botModelDisplay(bot.Bot{}); got != "" {
		t.Fatalf("botModelDisplay(empty) = %q, want the empty model", got)
	}
	model := newBotTestModel(t, 80, 24, &fakeBotClient{}, nil)
	model.bot.active = value
	model.bot.hasActive = true
	if got := model.botModelText(); got != durable {
		t.Fatalf("botModelText = %q, want %q", got, durable)
	}
}

// TestPromptBotModelDisplaysSelectorAndKeepsDurableValue proves the model prompt
// renders Control's public selector while the selected value and the keep-current
// fallback stay durable model identities.
func TestPromptBotModelDisplaysSelectorAndKeepsDurableValue(t *testing.T) {
	const (
		durable  = "openai@default/openai/gpt-5.4"
		otherID  = "openai@default/openai/gpt-5.3"
		selector = "openai/gpt-5.4"
	)
	candidates := []SlashArgCandidate{
		{Value: "openai/gpt-5.3", ModelConfigID: otherID, Display: "openai/gpt-5.3"},
		{Value: selector, ModelConfigID: durable, Display: selector},
	}
	current := bot.Bot{Config: bot.Config{Name: "Ada", Model: durable}, ModelSelector: selector}
	list := func(context.Context) ([]SlashArgCandidate, error) { return candidates, nil }

	var present PromptRequestMsg
	selected, ok := promptBotModel(t.Context(), func(msg tea.Msg) {
		present = msg.(PromptRequestMsg)
		present.Response <- PromptResponse{Line: durable}
	}, current, list)
	if !ok || present.Prompt != "Model (now: "+selector+")" || present.DefaultChoice != durable {
		t.Fatalf("prompt = %+v, ok = %v", present, ok)
	}
	if selected != durable {
		t.Fatalf("selected = %q, want the durable identity %q", selected, durable)
	}

	// A removed current model keeps a durable value with a selector label so the
	// user can still retain it without a catalog entry.
	var keep PromptRequestMsg
	if _, ok := promptBotModel(t.Context(), func(msg tea.Msg) {
		keep = msg.(PromptRequestMsg)
		keep.Response <- PromptResponse{Line: ""}
	}, current, func(context.Context) ([]SlashArgCandidate, error) { return candidates[:1], nil }); !ok {
		t.Fatal("removed current model aborted the prompt")
	}
	if len(keep.Choices) != 2 || keep.Choices[0].Label != selector || keep.Choices[0].Value != durable {
		t.Fatalf("keep-current choice = %+v, want label %q and value %q", keep.Choices, selector, durable)
	}
}

func TestBotModelSelectionUsesCanonicalIdentity(t *testing.T) {
	const currentID = "openai@default/openai/gpt-5.4"
	const otherID = "openai@default/openai/gpt-5.3"
	candidates := []SlashArgCandidate{
		{Value: "openai/gpt-5.3", ModelConfigID: otherID, Display: "openai/gpt-5.3"},
		{Value: "openai/gpt-5.4", ModelConfigID: currentID, Display: "openai/gpt-5.4"},
		{Value: "acp:external:default", Display: "External Agent"},
	}
	for _, tc := range []struct {
		name         string
		modelOnly    bool
		rename       bool
		changeModel  bool
		missingModel bool
	}{
		{name: "enter_keeps_current", modelOnly: true},
		{name: "rename_keeps_options", rename: true},
		{name: "different_model_resets_options", modelOnly: true, changeModel: true},
		{name: "missing_current_is_not_replaced", modelOnly: true, missingModel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := bot.Config{Name: "Ada", Model: currentID, Effort: "high", Fast: true}
			client := &fakeBotClient{bots: []bot.Bot{{ID: "bot-1", SessionID: "chat-1", Revision: 4, Config: original}}}
			model := newBotTestModel(t, 80, 24, client, nil)
			list := func(context.Context) ([]SlashArgCandidate, error) {
				if tc.missingModel {
					return candidates[:1], nil
				}
				return candidates, nil
			}
			prompts := 0
			runBotSettingsFlow(t.Context(), client, "bot-1", tc.modelOnly, list, func(msg tea.Msg) {
				req, ok := msg.(PromptRequestMsg)
				if !ok {
					return
				}
				prompts++
				if len(req.Choices) == 0 {
					answer := ""
					if tc.rename && prompts == 1 {
						answer = "Ada Lovelace"
					}
					req.Response <- PromptResponse{Line: answer}
					return
				}
				model.enqueuePrompt(req)
				choices := model.activePrompt.choices
				if len(choices) != 2 || choices[model.activePrompt.choiceIndex].value != currentID {
					t.Fatalf("current model not selected, or non-provider offered: %+v", model.activePrompt)
				}
				if tc.changeModel {
					model.Update(keyPress("up"))
				}
				model.Update(keyPress("enter"))
			})
			if !tc.rename && !tc.changeModel {
				if len(client.updated) != 0 {
					t.Fatalf("same model submitted an update: %+v", client.updated)
				}
				return
			}
			if len(client.updated) != 1 {
				t.Fatalf("updates = %d, want 1", len(client.updated))
			}
			want := original
			if tc.rename {
				want.Name = "Ada Lovelace"
			}
			if tc.changeModel {
				want.Model, want.Effort, want.Fast = otherID, "", false
			}
			if got := client.updated[0].Config; got != want {
				t.Fatalf("saved config = %+v, want %+v", got, want)
			}
		})
	}
}

func TestBotModelCatalogErrorDoesNotOfferUnresolvedInput(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{ID: "bot-1", Config: bot.Config{Name: "Ada", Model: "saved-id", Effort: "high", Fast: true}}}}
	var notices []string
	runBotSettingsFlow(t.Context(), client, "bot-1", true, func(context.Context) ([]SlashArgCandidate, error) {
		return nil, errors.New("catalog unavailable")
	}, func(msg tea.Msg) {
		switch msg := msg.(type) {
		case PromptRequestMsg:
			t.Fatal("failed catalog offered unresolvable model input")
		case SlashNoticeMsg:
			notices = append(notices, msg.Text)
		}
	})
	if len(client.updated) != 0 || !findBotNotice(notices, "catalog unavailable") {
		t.Fatalf("failed catalog updated settings or hid error: updates=%+v notices=%v", client.updated, notices)
	}
}

func TestBotSettingsWithoutModelsKeepsModelUnset(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{ID: "bot-1", Config: bot.Config{Name: "Ada"}}}}
	prompts := 0
	runBotSettingsFlow(t.Context(), client, "bot-1", false, func(context.Context) ([]SlashArgCandidate, error) {
		return nil, nil
	}, func(msg tea.Msg) {
		if req, ok := msg.(PromptRequestMsg); ok {
			prompts++
			req.Response <- PromptResponse{Line: "renamed"}
		}
	})
	if prompts != 2 || len(client.updated) != 1 || client.updated[0].Config.Model != "" {
		t.Fatalf("model-free edit: prompts=%d updates=%+v", prompts, client.updated)
	}
}

func TestBotModelPickerGoldenFrames(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			const currentID = "openai@default/openai/gpt-5.4"
			model := newBotTestModel(t, size[0], size[1], &fakeBotClient{}, nil)
			stageBotForGolden(model, bot.Bot{Config: bot.Config{Name: "Ada", Model: currentID}, ModelSelector: "openai/gpt-5.4"})
			list := func(context.Context) ([]SlashArgCandidate, error) {
				return []SlashArgCandidate{
					{Value: "openai/gpt-5.3", ModelConfigID: "openai@default/openai/gpt-5.3", Display: "openai/gpt-5.3"},
					{Value: "openai/gpt-5.4", ModelConfigID: currentID, Display: "openai/gpt-5.4"},
				}, nil
			}
			selected, ok := promptBotModel(t.Context(), func(msg tea.Msg) {
				req := msg.(PromptRequestMsg)
				model.enqueuePrompt(req)
				model.syncViewportContent()
				checkBotGolden(t, fmt.Sprintf("model_%dx%d", size[0], size[1]), size[0], size[1], model.View().Content)
				model.Update(keyPress("enter"))
			}, model.bot.active, list)
			if !ok || selected != currentID {
				t.Fatalf("Enter selected %q, want %q", selected, currentID)
			}
		})
	}
}
