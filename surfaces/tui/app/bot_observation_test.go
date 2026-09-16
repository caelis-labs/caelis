package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

type botReadProbe struct {
	appserver.BotClient
	get func(context.Context, string) (bot.Bot, error)
}

func (p botReadProbe) GetBot(ctx context.Context, id string) (bot.Bot, error) {
	return p.get(ctx, id)
}

func TestBotConfigurationRefreshUsesFocusedRead(t *testing.T) {
	old := bot.Bot{ID: "bot-1", SessionID: "chat-1", Revision: 1, Config: bot.Config{Name: "Old"}}
	latest := old
	latest.Revision++
	latest.Config = bot.Config{Name: "Renamed", Description: "New description", Model: "deepseek@default/deepseek/deepseek-flash"}
	latest.ModelSelector = "deepseek/deepseek-flash"
	calls := 0
	client := botReadProbe{get: func(_ context.Context, id string) (bot.Bot, error) {
		calls++
		if id != old.ID {
			t.Fatalf("GetBot(%q), want %q", id, old.ID)
		}
		return latest, nil
	}}
	model := newBotTestModel(t, 100, 30, client, nil)
	stageBotForGolden(model, old)
	model.viewGeneration = 3
	model.currentSessionID = old.SessionID
	model.setInputText("Unsent draft")
	model.syncTextareaFromInput()
	refresh := model.beginStatusRefreshCmd()
	if calls != 0 || refresh == nil {
		t.Fatal("Bot read did not use the background status refresh")
	}
	if duplicate := model.beginStatusRefreshCmd(); duplicate != nil {
		t.Fatal("a second status read was admitted while the first was in flight")
	}
	model.Update(refresh())
	if calls != 1 || model.bot.active != latest || model.statusRefreshInFlight {
		t.Fatalf("refresh: calls=%d active=%+v inFlight=%v", calls, model.bot.active, model.statusRefreshInFlight)
	}
	if model.windowTitle() != latest.Config.Name || model.botModelText() != latest.ModelSelector {
		t.Fatalf("stale chrome: title=%q model=%q", model.windowTitle(), model.botModelText())
	}
	if model.textarea.Value() != "Unsent draft" || model.currentSessionID != old.SessionID || model.doc.Len() != 0 {
		t.Fatal("configuration observation changed the draft, target, or conversation")
	}
	model.showBotStatus()
	model.syncViewportContent()
	frame := ansi.Strip(model.View().Content)
	for _, want := range []string{latest.Config.Name, latest.Config.Description, latest.ModelSelector} {
		if !strings.Contains(frame, want) {
			t.Fatalf("/status omitted %q:\n%s", want, frame)
		}
	}
}

func TestBotConfigurationRefreshRejectsStaleSnapshots(t *testing.T) {
	original := bot.Bot{ID: "bot-1", SessionID: "chat-1", Revision: 3, Config: bot.Config{Name: "Original"}}
	read := original
	read.Revision++
	read.Config.Name = "Snapshot"
	for _, scenario := range []string{"new view", "other Bot", "other Session", "newer save", "failed read"} {
		t.Run(scenario, func(t *testing.T) {
			client := botReadProbe{get: func(context.Context, string) (bot.Bot, error) {
				if scenario == "failed read" {
					return bot.Bot{}, errors.New("offline")
				}
				return read, nil
			}}
			model := newBotTestModel(t, 80, 24, client, nil)
			stageBotForGolden(model, original)
			model.viewGeneration = 5
			refresh := model.beginStatusRefreshCmd()
			result := refresh()
			switch scenario {
			case "new view":
				model.viewGeneration++
				// A new view may already have its own read in flight.
			case "other Bot":
				model.bot.active.ID = "bot-2"
			case "other Session":
				model.bot.active.SessionID = "chat-2"
			case "newer save":
				saved := original
				saved.Config.Name = "Saved after the read"
				saved.Revision = read.Revision + 1
				model.handleBotFlowResult(botFlowResultMsg{bot: saved})
			}
			want := model.bot.active
			model.Update(result)
			if model.bot.active != want {
				t.Fatalf("stale result changed active Bot: got %+v, want %+v", model.bot.active, want)
			}
			if scenario == "new view" && !model.statusRefreshInFlight {
				t.Fatal("old result cleared the new view's in-flight read")
			}
		})
	}
}

func TestBotLocalSaveResultCannotRollBackRefreshedConfiguration(t *testing.T) {
	latest := bot.Bot{ID: "bot-1", SessionID: "chat-1", Revision: 10, Config: bot.Config{Name: "Latest"}}
	model := newBotTestModel(t, 80, 24, &fakeBotClient{}, nil)
	stageBotForGolden(model, latest)
	model.bot.bots = []bot.Bot{latest}
	older := latest
	older.Revision--
	older.Config.Name = "Older save"
	model.handleBotFlowResult(botFlowResultMsg{bot: older})
	if model.bot.active != latest || model.bot.bots[0] != latest {
		t.Fatalf("late local save rolled back configuration: active=%+v list=%+v", model.bot.active, model.bot.bots)
	}
}
