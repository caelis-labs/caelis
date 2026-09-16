package tuiapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

// TestProductBotSelectionAndInputRouteThroughRealHost binds the Bot TUI to a
// real Host and the production AppServerAdapter: no ExecuteLine stub, no fake
// Bot client. It proves that a staged Bot is published only after its Session
// attains, that input before an attached Bot never creates a workspace Session,
// and that a prompt routes to the selected Bot's conversation.
func TestProductBotSelectionAndInputRouteThroughRealHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"bot\",\"object\":\"chat.completion.chunk\",\"model\":\"observation\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"bot reply\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"bot\",\"object\":\"chat.completion.chunk\",\"model\":\"observation\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer provider.Close()

	workspace := t.TempDir()
	clients, closeHost, err := localclient.New(ctx, t.TempDir(), workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()

	first := createProductBot(t, ctx, clients.Bots, "Ada")
	second := createProductBot(t, ctx, clients.Bots, "Grace")
	if first.SessionID == second.SessionID {
		t.Fatal("Bots share one conversation")
	}

	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-bot", WorkspaceKey: "workspace", WorkspaceDir: workspace,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	messages := make(chan tea.Msg, 256)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	cfg := ConfigFromControlService(adapter, sender, Config{
		Context: ctx, NoColor: true, NoAnimation: true,
		Commands: BotCommands(), CommandDetails: BotCommandDetails(), Wizards: DefaultWizards(),
		PromptRouterFactory: controlprompt.New,
		Bot:                 &BotSurface{Client: clients.Bots},
	})
	model := NewModel(cfg)
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	nextMessage := func(desc string) tea.Msg {
		t.Helper()
		select {
		case msg := <-messages:
			model.Update(msg)
			model.drainPendingRenderEvents(time.Now())
			model.flushPendingViewportSync()
			return unwrapSessionViewMessage(msg)
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", desc, ctx.Err())
			return nil
		}
	}
	waitFor := func(desc string, ok func(tea.Msg) bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if ok(nextMessage(desc)) {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", desc)
	}

	// Before any Bot is attached, plain input must not reach the Host or create
	// an implicit workspace Session.
	updated, hintCmd := model.submitInteractiveLine("early input", "early input", nil)
	model = updated.(*Model)
	if hintCmd == nil {
		t.Fatal("pre-attach input did not surface a hint")
	}
	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("pre-attach input reached the model provider %d times", got)
	}
	assertOnlyBotSessions(t, ctx, clients.Sessions, first, second)

	// Bootstrap: two Bots open the picker; nothing is active yet.
	bootstrap := model.beginBotBootstrap()
	if bootstrap == nil {
		t.Fatal("beginBotBootstrap returned no command")
	}
	updated, load := model.Update(bootstrap())
	model = updated.(*Model)
	if load == nil || model.sessionPicker == nil || !model.sessionPicker.botPicker {
		t.Fatalf("expected the Bot picker: %#v", model.sessionPicker)
	}
	updated, _ = model.Update(load())
	model = updated.(*Model)
	if got := len(model.sessionPicker.rows); got != 2 {
		t.Fatalf("Bot picker rows = %d, want 2", got)
	}

	targetIndex := -1
	for i, row := range model.sessionPicker.rows {
		if row.SessionID == second.SessionID {
			targetIndex = i
		}
	}
	if targetIndex < 0 {
		t.Fatal("the second Bot is missing from the picker")
	}
	model.sessionPicker.index = targetIndex
	updated, selectCmd := model.Update(keyPress("enter"))
	model = updated.(*Model)
	if selectCmd == nil {
		t.Fatal("selection did not schedule the conversation attach")
	}
	if model.activeBotSelected() {
		t.Fatal("Bot became active before its conversation attached")
	}
	// Reopen the picker before the first attach runs. The second choice is
	// refused by the Session switch gate and must not replace the pending Bot.
	model = loadBotPickerForTest(t, model, model.openBotPicker())
	for i, row := range model.sessionPicker.rows {
		if row.SessionID == first.SessionID {
			model.sessionPicker.index = i
		}
	}
	if _, rejected := model.Update(keyPress("enter")); rejected != nil {
		t.Fatal("overlapping Bot selection was admitted")
	}
	_ = selectCmd()

	for {
		if _, ready := nextMessage("session history").(sessionHistoryReadyMsg); ready {
			break
		}
	}
	if name := model.botName(); name != second.Config.Name {
		t.Fatalf("active Bot name = %q, want %q", name, second.Config.Name)
	}
	if session := model.botActiveSessionID(); session != second.SessionID {
		t.Fatalf("active Bot conversation = %q, want %q", session, second.SessionID)
	}
	if current := model.currentSessionID; current != second.SessionID {
		t.Fatalf("command target Session = %q, want %q", current, second.SessionID)
	}

	// A prompt must reach the selected Bot's conversation and only that one.
	firstBefore := botRevision(t, ctx, clients.Bots, first.ID)
	secondBefore := botRevision(t, ctx, clients.Bots, second.ID)
	promptCmd := model.executeLineCmd(Submission{Text: "hello"})
	if promptCmd == nil {
		t.Fatal("prompt did not schedule dispatch")
	}
	_ = promptCmd()
	waitFor("agent output", func(msg tea.Msg) bool {
		envelope, ok := msg.(eventstream.Envelope)
		return ok && eventstream.UpdateType(envelope.Update) == eventstream.UpdateAgentMessage
	})
	waitForBotRevision(t, ctx, clients.Bots, second.ID, secondBefore)
	if after := botRevision(t, ctx, clients.Bots, first.ID); after != firstBefore {
		t.Fatalf("the unselected Bot conversation changed: %d -> %d", firstBefore, after)
	}
	if got := providerCalls.Load(); got == 0 {
		t.Fatal("the model provider never received the Bot prompt")
	}
	assertOnlyBotSessions(t, ctx, clients.Sessions, first, second)
}

// botTinyPNGBase64 is a valid 1x1 PNG, enough to build an image-only prompt.
const botTinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// TestProductBotAdapterRejectsImplicitSessionCreation proves the enforcement is
// in the adapter, not only the presentation: with no selected Session an
// image-only submission is rejected and no ordinary workspace Session is
// created.
func TestProductBotAdapterRejectsImplicitSessionCreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer provider.Close()

	clients, closeHost, err := localclient.New(ctx, t.TempDir(), t.TempDir(), provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()

	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-bot", WorkspaceKey: "workspace",
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
		RequireExistingSession: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	_, err = adapter.Submit(ctx, controlprompt.Submission{
		Attachments: []controlprompt.Attachment{{Name: "shot.png", MimeType: "image/png", Data: botTinyPNGBase64}},
	})
	if err == nil {
		t.Fatal("image-only submission without a Bot conversation was admitted")
	}
	listed, err := clients.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 0 {
		t.Fatalf("an implicit workspace Session was created: %#v", listed.Sessions)
	}
}

func createProductBot(t *testing.T, ctx context.Context, client appserver.BotClient, name string) bot.Bot {
	t.Helper()
	result, err := client.CreateBot(ctx, appserver.CreateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "product-bot-" + strings.ToLower(name)},
		Config:    bot.Config{Name: name},
	})
	if err != nil {
		t.Fatalf("CreateBot(%s): %v", name, err)
	}
	if result.Outcome != appserver.OutcomeCommitted || result.Resource == nil || strings.TrimSpace(result.Resource.Ref) == "" {
		t.Fatalf("CreateBot(%s) result = %#v", name, result)
	}
	created, err := client.GetBot(ctx, result.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(created.Config.Model) == "" {
		t.Fatalf("Bot %s did not snapshot a default model", name)
	}
	return created
}

func botRevision(t *testing.T, ctx context.Context, client appserver.BotClient, id string) uint64 {
	t.Helper()
	value, err := client.GetBot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return value.Revision
}

func waitForBotRevision(t *testing.T, ctx context.Context, client appserver.BotClient, id string, before uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		value, err := client.GetBot(ctx, id)
		if err == nil && value.Revision > before {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Bot revision: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("Bot %s revision never advanced past %d", id, before)
}

func assertOnlyBotSessions(t *testing.T, ctx context.Context, sessions appserver.SessionClient, bots ...bot.Bot) {
	t.Helper()
	allowed := map[string]bool{}
	for _, value := range bots {
		allowed[value.SessionID] = true
	}
	listed, err := sessions.ListSessions(ctx, appserver.ListSessionsRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range listed.Sessions {
		if !allowed[summary.SessionID] {
			t.Fatalf("unexpected workspace Session %q was created", summary.SessionID)
		}
	}
}
