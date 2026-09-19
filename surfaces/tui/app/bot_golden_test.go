package tuiapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/bot"
)

// botGoldenUpdateEnv regenerates the reviewable full-frame goldens under
// testdata/bot. Set it to any non-empty value to rewrite the files.
const botGoldenUpdateEnv = "CAELIS_BOT_GOLDEN_UPDATE"

// botGoldenFrame strips style escapes and trailing whitespace. Geometry is
// checked against the complete frame before empty bottom rows are omitted.
func botGoldenFrame(content string) string {
	lines := strings.Split(ansi.Strip(content), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func checkBotGolden(t *testing.T, name string, width, height int, frame string) {
	t.Helper()
	path := filepath.Join("testdata", "bot", name+".golden")
	lines := strings.Split(ansi.Strip(frame), "\n")
	if len(lines) > height {
		t.Fatalf("Bot golden %s has %d rows, terminal height %d", name, len(lines), height)
	}
	for i, line := range lines {
		if got := displayColumns(line); got > width {
			t.Fatalf("Bot golden %s row %d width=%d exceeds terminal width=%d: %q", name, i, got, width, line)
		}
	}
	frame = botGoldenFrame(frame)
	if os.Getenv(botGoldenUpdateEnv) != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(frame), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with %s=1)", path, err, botGoldenUpdateEnv)
	}
	if frame != string(want) {
		t.Fatalf("Bot frame %s differs from golden:\n--- golden ---\n%s--- rendered ---\n%s", path, want, frame)
	}
}

// stageBotForGolden publishes a Bot directly so a rendering golden exercises
// chrome and transcript state without the attach flow, which other tests cover.
func stageBotForGolden(model *Model, value bot.Bot) {
	model.bot.active = value
	model.bot.hasActive = true
	model.bot.pending = bot.Bot{}
	model.bot.hasPending = false
}

func botGoldenBots() []bot.Bot {
	return []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 7, Config: bot.Config{Name: "Grace"}},
		{ID: "bot-3", SessionID: "bot-chat-3", Revision: 2, Config: bot.Config{Name: "处理长名称的机器人", Model: "claude-4"}},
	}
}

func botGoldenModel(t *testing.T, width, height int, bots []bot.Bot) (*Model, *fakeBotClient) {
	t.Helper()
	client := &fakeBotClient{bots: bots}
	model := newBotTestModel(t, width, height, client, nil)
	model.cfg.Workspace = "/Users/example/secret-project"
	model.statusView.Workspace = "/Users/example/secret-project (main)"
	model.sessionSwitchPending = false
	return model, client
}

func botGoldenPickerFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, _ := botGoldenModel(t, width, height, botGoldenBots())
	updated, load := model.Update(botListMsg{bots: botGoldenBots()})
	model = updated.(*Model)
	model = loadBotPickerForTest(t, model, load)
	model.sessionPicker.index = 2
	model.syncViewportContent()
	return model.View().Content
}

func botGoldenCreateFrame(t *testing.T, width, height int) string {
	t.Helper()
	return botGoldenCreateStepFrame(t, width, height, false)
}

func botGoldenDescriptionFrame(t *testing.T, width, height int) string {
	t.Helper()
	return botGoldenCreateStepFrame(t, width, height, true)
}

func botGoldenCreateStepFrame(t *testing.T, width, height int, description bool) string {
	t.Helper()
	model, _ := botGoldenModel(t, width, height, nil)
	model.startBotCreateFlow()
	if description {
		model.Update(tea.PasteMsg{Content: "Ada"})
		model.Update(connectKey("tab"))
	}
	model.syncViewportContent()
	return model.View().Content
}

func botGoldenCommandsFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, client := botGoldenModel(t, width, height, botGoldenBots()[:1])
	stageBotForGolden(model, client.bots[0])
	model.Update(SetCommandsMsg{Commands: append(DefaultCommands(), "custom-agent")})
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	model.syncViewportContent()
	return model.View().Content
}

func botGoldenTeamCompletionFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, client := botGoldenModel(t, width, height, botGoldenBots()[:1])
	stageBotForGolden(model, client.bots[0])
	model.Update(SetCommandsMsg{Commands: append(DefaultCommands(), "custom-agent")})
	model.setInputText("/sub")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	model.syncViewportContent()
	return model.View().Content
}

func botGoldenSettingsFrame(t *testing.T, width, height int) string {
	t.Helper()
	value := bot.Bot{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Description: "kind and brief", Model: "gpt-5", Effort: "high"}}
	model, _ := botGoldenModel(t, width, height, []bot.Bot{value})
	stageBotForGolden(model, value)
	model.Update(model.startBotSettingsFlow(false)())
	model.syncViewportContent()
	return model.View().Content
}

// applyBotGoldenEnvelope feeds one ScopeMain envelope through the real
// transcript path so committed and streaming content share production shapes.
func applyBotGoldenEnvelope(t *testing.T, model *Model, env eventstream.Envelope) *Model {
	t.Helper()
	next, _ := model.handleACPEventEnvelope(env)
	typed, ok := next.(*Model)
	if !ok {
		t.Fatalf("model = %T, want *Model", next)
	}
	typed.syncViewportContent()
	return typed
}

func botGoldenUserEnvelope(text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "bot-chat-1", Scope: eventstream.ScopeMain,
		Final: true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: text},
		},
	}
}

func botGoldenAgentEnvelope(text string, final bool) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "bot-chat-1", Scope: eventstream.ScopeMain,
		Final: final,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: text},
		},
	}
}

func botGoldenChatFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, client := botGoldenModel(t, width, height, botGoldenBots()[:1])
	stageBotForGolden(model, client.bots[0])
	model = applyBotGoldenEnvelope(t, model, botGoldenUserEnvelope("What can you do?"))
	model = applyBotGoldenEnvelope(t, model, botGoldenAgentEnvelope("I can draft replies, discuss ideas, and summarize text.", true))
	model.setInputText("Help me phrase a reply")
	model.syncTextareaFromInput()
	model.syncViewportContent()
	_ = client
	return model.View().Content
}

func botGoldenStreamingFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, _ := botGoldenModel(t, width, height, botGoldenBots()[:1])
	stageBotForGolden(model, botGoldenBots()[0])
	model = applyBotGoldenEnvelope(t, model, botGoldenUserEnvelope("Explain streaming"))
	long := strings.Join([]string{
		"Streaming keeps long answers readable while they are still being written.",
		"Each chunk is buffered and painted in order so the tail stays stable,",
		"the viewport follows the newest text, and scrolling never fights the",
		"writer. Wrapped continuations keep their alignment, and the composer",
		"never loses a draft just because output is still arriving.",
	}, "\n")
	model = applyBotGoldenEnvelope(t, model, botGoldenAgentEnvelope(long, false))
	model.syncViewportContent()
	return model.View().Content
}

func botGoldenScrollingFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, _ := botGoldenModel(t, width, height, botGoldenBots()[:1])
	stageBotForGolden(model, botGoldenBots()[0])
	model = applyBotGoldenEnvelope(t, model, botGoldenUserEnvelope("Write a long list"))
	lines := make([]string, 0, 24)
	for i := 0; i < 24; i++ {
		lines = append(lines, "Line "+string(rune('A'+i%26))+" of the long Bot answer that must be scrolled.")
	}
	model = applyBotGoldenEnvelope(t, model, botGoldenAgentEnvelope(strings.Join(lines, "\n"), true))
	model.setViewportFollowState(viewportPinnedHistory)
	model.viewportScrollbarVisibleUntil = time.Unix(1<<40, 0)
	model.viewport.GotoTop()
	model.viewport.SetYOffset(3)
	model.syncViewportContent()
	return model.View().Content
}

func botGoldenDraftsFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, _ := botGoldenModel(t, width, height, botGoldenBots()[:2])
	// Ada's conversation holds a draft; switching away and back must restore it.
	model.applySessionReconnectState(appserver.SessionState{SessionID: "bot-chat-1"})
	model.setInputText("unfinished note for Ada")
	model.syncTextareaFromInput()
	model.applySessionReconnectState(appserver.SessionState{SessionID: "bot-chat-2"})
	if got := strings.TrimSpace(model.textarea.Value()); got != "" {
		t.Fatalf("switching Bots leaked a draft: %q", got)
	}
	model.applySessionReconnectState(appserver.SessionState{SessionID: "bot-chat-1"})
	stageBotForGolden(model, botGoldenBots()[0])
	model.syncViewportContent()
	if got := strings.TrimSpace(model.textarea.Value()); got != "unfinished note for Ada" {
		t.Fatalf("draft after switching back = %q", got)
	}
	return model.View().Content
}

func botGoldenUnknownOutcomeFrame(t *testing.T, width, height int) string {
	t.Helper()
	model, _ := botGoldenModel(t, width, height, botGoldenBots()[:1])
	stageBotForGolden(model, botGoldenBots()[0])
	model.applySessionReconnectState(appserver.SessionState{
		SessionID: "bot-chat-1",
		Run:       appserver.RunState{Status: eventstream.LifecycleStateUnknown},
	})
	model = applyBotGoldenEnvelope(t, model, botGoldenUserEnvelope("Did the last reply finish?"))
	model = applyBotGoldenEnvelope(t, model, botGoldenAgentEnvelope("I was interrupted before I could confirm.", true))
	model.syncViewportContent()
	return model.View().Content
}

func TestBotGoldenFrames(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		height int
		build  func(*testing.T, int, int) string
	}{
		{name: "picker_40x12", width: 40, height: 12, build: botGoldenPickerFrame},
		{name: "picker_80x24", width: 80, height: 24, build: botGoldenPickerFrame},
		{name: "create_40x12", width: 40, height: 12, build: botGoldenCreateFrame},
		{name: "create_80x24", width: 80, height: 24, build: botGoldenCreateFrame},
		{name: "settings_40x12", width: 40, height: 12, build: botGoldenSettingsFrame},
		{name: "settings_80x24", width: 80, height: 24, build: botGoldenSettingsFrame},
		{name: "chat_40x12", width: 40, height: 12, build: botGoldenChatFrame},
		{name: "description_40x12", width: 40, height: 12, build: botGoldenDescriptionFrame},
		{name: "description_80x24", width: 80, height: 24, build: botGoldenDescriptionFrame},
		{name: "commands_40x12", width: 40, height: 12, build: botGoldenCommandsFrame},
		{name: "commands_80x24", width: 80, height: 24, build: botGoldenCommandsFrame},
		{name: "team_completion_80x24", width: 80, height: 24, build: botGoldenTeamCompletionFrame},
		{name: "chat_80x24", width: 80, height: 24, build: botGoldenChatFrame},
		{name: "chat_120x40", width: 120, height: 40, build: botGoldenChatFrame},
		{name: "streaming_80x24", width: 80, height: 24, build: botGoldenStreamingFrame},
		{name: "streaming_120x40", width: 120, height: 40, build: botGoldenStreamingFrame},
		{name: "scrolling_80x24", width: 80, height: 24, build: botGoldenScrollingFrame},
		{name: "drafts_80x24", width: 80, height: 24, build: botGoldenDraftsFrame},
		{name: "unknown_80x24", width: 80, height: 24, build: botGoldenUnknownOutcomeFrame},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			frame := testCase.build(t, testCase.width, testCase.height)
			checkBotGolden(t, testCase.name, testCase.width, testCase.height, frame)
		})
	}
}
