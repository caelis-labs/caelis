package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/bot"
)

// botRenderSizes covers a minimal terminal, the common default, and a wide
// terminal. Every Bot frame must stay inside these bounds.
var botRenderSizes = []struct {
	name          string
	width, height int
}{
	{name: "minimal", width: 40, height: 12},
	{name: "common", width: 80, height: 24},
	{name: "wide", width: 120, height: 40},
}

func assertBotFrameBounds(t *testing.T, model *Model, frame string) {
	t.Helper()
	for i, line := range strings.Split(frame, "\n") {
		if width := displayColumns(ansi.Strip(line)); width > model.width {
			t.Fatalf("frame row %d width=%d exceeds terminal width=%d: %q", i, width, model.width, ansi.Strip(line))
		}
	}
}

func TestBotPickerRenderEvidence(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 3, Config: bot.Config{Name: "Grace"}},
		{ID: "bot-3", SessionID: "bot-chat-3", Revision: 4, Config: bot.Config{Name: "处理长名称的机器人", Model: "claude-4"}},
	}}
	for _, size := range botRenderSizes {
		t.Run(size.name, func(t *testing.T) {
			model := newBotTestModel(t, size.width, size.height, client, nil)
			updated, load := model.Update(model.beginBotBootstrap()())
			model = updated.(*Model)
			if load == nil {
				t.Fatal("Bot picker did not schedule a list load")
			}
			updated, _ = model.Update(load())
			model = updated.(*Model)

			frames := []string{model.View().Content}
			updated, _ = model.Update(keyPress("down"))
			model = updated.(*Model)
			frames = append(frames, model.View().Content)

			plain := ansi.Strip(frames[len(frames)-1])
			for _, want := range []string{"Bots", "Ada", "gpt-5", "Grace", "no model set", "处理长名称"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("Bot picker omitted %q:\n%s", want, plain)
				}
			}
			for _, frame := range frames {
				assertBotFrameBounds(t, model, frame)
			}
			updates := renderFullscreenFramesForTest(t, size.width, size.height, frames...)
			assertPhysicalFullscreenFrame(t, size.width, size.height, frames[len(frames)-1], updates)
		})
	}
}

func TestBotCreatePromptRenderEvidence(t *testing.T) {
	client := &fakeBotClient{}
	send := make(chan tea.Msg, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBotCreateFlow(context.Background(), client, func(msg tea.Msg) { send <- msg })
	}()
	req := nextBotPrompt(t, send)
	for _, step := range []struct{ name, hint string }{
		{"name", "Enter New Bot name"},
		{"description", "optional; Enter to skip"},
	} {
		if step.name == "description" {
			req.Response <- PromptResponse{Line: "Ada"}
			req = nextBotPrompt(t, send)
		}
		for _, size := range botRenderSizes {
			t.Run(step.name+"/"+size.name, func(t *testing.T) {
				model := newBotTestModel(t, size.width, size.height, client, nil)
				model.enqueuePrompt(req)
				model.syncViewportContent()
				frame := model.View().Content
				plain := ansi.Strip(frame)
				if !strings.Contains(strings.Join(strings.Fields(plain), " "), step.hint) {
					t.Fatalf("create prompt omitted %q:\n%s", step.hint, plain)
				}
				assertBotFrameBounds(t, model, frame)
				updates := renderFullscreenFramesForTest(t, size.width, size.height, frame)
				assertPhysicalFullscreenFrame(t, size.width, size.height, frame, updates)
			})
		}
	}

	req.Response <- PromptResponse{Err: errors.New(PromptErrInterrupt)}
	<-done
}

func TestBotChatChromeRenderEvidence(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1,
		Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	for _, size := range botRenderSizes {
		t.Run(size.name, func(t *testing.T) {
			model := newBotTestModel(t, size.width, size.height, client, nil)
			model.cfg.Workspace = "/Users/example/secret-project"
			model.statusView.Workspace = "/Users/example/secret-project (main)"
			model.commitInitialLine("Hello, how can I help?")
			updated, _ := model.Update(model.beginBotBootstrap()())
			model = updated.(*Model)
			model = publishBotForTest(t, model, "bot-chat-1")
			model.sessionSwitchPending = false
			model.syncViewportContent()

			frame := model.View().Content
			plain := ansi.Strip(frame)
			if strings.Contains(plain, "secret-project") || strings.Contains(plain, "bot-chat-1") {
				t.Fatalf("Bot chat chrome leaked workspace/Session identity:\n%s", plain)
			}
			for _, want := range []string{"Ada", "gpt-5"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("Bot chat chrome omitted %q:\n%s", want, plain)
				}
			}
			assertBotFrameBounds(t, model, frame)
			updates := renderFullscreenFramesForTest(t, size.width, size.height, frame)
			assertPhysicalFullscreenFrame(t, size.width, size.height, frame, updates)
		})
	}
}

func TestBotSettingsPromptRenderEvidence(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 2,
		Config: bot.Config{Name: "Ada", Description: "old", Model: "gpt-5"},
	}}}
	send := make(chan tea.Msg, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBotSettingsFlow(context.Background(), client, "bot-1", false, nil, func(msg tea.Msg) { send <- msg })
	}()
	nextBotPrompt(t, send).Response <- PromptResponse{Line: ""}
	req := nextBotPrompt(t, send)

	for _, size := range botRenderSizes {
		t.Run(size.name, func(t *testing.T) {
			model := newBotTestModel(t, size.width, size.height, client, nil)
			model.enqueuePrompt(req)
			model.syncViewportContent()
			frame := model.View().Content
			plain := ansi.Strip(frame)
			if !strings.Contains(plain, "Description") || !strings.Contains(plain, "old") {
				t.Fatalf("settings prompt omitted the current description at %dx%d:\n%s", size.width, size.height, plain)
			}
			assertBotFrameBounds(t, model, frame)
			updates := renderFullscreenFramesForTest(t, size.width, size.height, frame)
			assertPhysicalFullscreenFrame(t, size.width, size.height, frame, updates)
		})
	}

	req.Response <- PromptResponse{Err: errors.New(PromptErrInterrupt)}
	<-done
}
