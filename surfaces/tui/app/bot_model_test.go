package tuiapp

import (
	"strings"
	"testing"

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
