package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

func TestProductBotObservesOtherWindowConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clients, closeHost, workspace := newAddressProductHost(t, ctx)
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()
	original := createProductBot(t, ctx, clients.Bots, "Grace")
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-bot", WorkspaceKey: "workspace", WorkspaceDir: workspace, RequireExistingSession: true,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	sender := &ProgramSender{}
	defer sender.Close()
	model := NewModel(ConfigFromControlService(adapter, sender, Config{
		Context: ctx, NoColor: true, NoAnimation: true,
		Bot: &BotSurface{Client: clients.Bots}, PromptRouterFactory: controlprompt.New,
	}))
	type snapshot struct {
		value        bot.Bot
		title, frame string
		attached     bool
		userEvents   int
	}
	type inspect chan snapshot
	userEvents := 0
	program := tea.NewProgram(model, tea.WithInput(nil), tea.WithoutRenderer(), tea.WithWindowSize(120, 40), tea.WithoutSignalHandler(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if reply, ok := msg.(inspect); ok {
			value, _ := model.activeBot()
			reply <- snapshot{value: value, title: model.windowTitle(), frame: ansi.Strip(model.View().Content),
				attached: model.currentSessionID == original.SessionID && model.sessionHistory == nil, userEvents: userEvents}
			return nil
		}
		if env, ok := unwrapSessionViewMessage(msg).(eventstream.Envelope); ok && eventstream.UpdateType(env.Update) == eventstream.UpdateUserMessage {
			userEvents++
		}
		return msg
	}))
	sender.Send = program.Send
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	defer func() {
		program.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			program.Kill()
			t.Error("Bot observation program did not stop")
		}
	}()
	wait := func(label string, predicate func(snapshot) bool) snapshot {
		t.Helper()
		var last snapshot
		for {
			reply := make(inspect, 1)
			program.Send(reply)
			select {
			case got := <-reply:
				last = got
				if predicate(got) {
					return got
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v; snapshot=%+v", label, ctx.Err(), last)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v; snapshot=%+v", label, ctx.Err(), last)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	before := wait("initial Bot attach", func(got snapshot) bool { return got.attached && got.value.ID == original.ID })
	config := original.Config
	config.Name, config.Description = "Renamed in second window", "Description from another window"
	result, err := clients.Bots.UpdateBot(ctx, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "remote-text-edit", SessionID: original.SessionID, ExpectedRevision: &original.Revision},
		BotID:     original.ID, Config: config,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("remote save: %+v, %v", result, err)
	}
	wait("canonical settings event", func(got snapshot) bool { return got.userEvents > before.userEvents })
	program.Send(statusRefreshRequestMsg{})
	updated := wait("refreshed Bot chrome", func(got snapshot) bool { return got.value.Config == config && got.title == config.Name })
	program.Send(tea.PasteMsg{Content: "/status"})
	program.Send(keyPress("enter"))
	wait("refreshed /status", func(got snapshot) bool { return strings.Contains(got.frame, config.Description) })

	// Model-only edits append no configuration user message. The ordinary
	// status tick must still refresh the focused Bot read, not parse transcript.
	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{WorkspaceKey: "workspace", CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	result, err = clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "remote-connect", ExpectedRevision: &status.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "ollama", Model: "llama3"},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect: %+v, %v", result, err)
	}
	config.Model, config.Effort, config.Fast = "ollama/llama3", "", false
	result, err = clients.Bots.UpdateBot(ctx, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "remote-model-edit", SessionID: original.SessionID, ExpectedRevision: &updated.value.Revision},
		BotID:     original.ID, Config: config,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("remote model save: %+v, %v", result, err)
	}
	want, err := clients.Bots.GetBot(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterModel := wait("model-only update through status tick", func(got snapshot) bool {
		return got.value.Config == want.Config && got.value.ModelSelector == want.ModelSelector && strings.Contains(got.frame, want.ModelSelector)
	})
	if afterModel.userEvents != updated.userEvents {
		t.Fatal("model-only settings changed canonical user messages")
	}
	assertOnlyBotSessions(t, ctx, clients.Sessions, original)
}
