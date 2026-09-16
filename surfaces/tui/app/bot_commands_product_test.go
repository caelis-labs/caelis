package tuiapp

// bot_commands_product_test.go locks the Bot slash-command surface against the
// real Host and the production AppServerAdapter: the Bot catalog must survive the
// Host's own SetCommandsMsg on attach and refresh, /team must read and save real
// Host configuration without a model call or a coding Session, and participant
// and handoff execution stays refused.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

type botCommandProductHarness struct {
	ctx           context.Context
	clients       appserver.AppServerClients
	providerCalls *atomic.Int64
	messages      chan tea.Msg
	model         *Model
	setCommands   []SetCommandsMsg
}

// newBotCommandProductHarness binds the Bot TUI to an isolated real Host through
// the production AppServerAdapter and ConfigFromControlService.
func newBotCommandProductHarness(t *testing.T) *botCommandProductHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	calls := &atomic.Int64{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"bot\",\"object\":\"chat.completion.chunk\",\"model\":\"observation\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"bot reply\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(provider.Close)

	workspace := t.TempDir()
	clients, closeHost, err := localclient.New(ctx, t.TempDir(), workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	})

	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-bot", WorkspaceKey: "workspace", WorkspaceDir: workspace, RequireExistingSession: true,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	messages := make(chan tea.Msg, 512)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	t.Cleanup(sender.Close)

	model := NewModel(ConfigFromControlService(adapter, sender, Config{
		Context: ctx, NoColor: true, NoAnimation: true,
		Commands: BotCommands(), CommandDetails: BotCommandDetails(), Wizards: DefaultWizards(),
		PromptRouterFactory: controlprompt.New,
		Bot:                 &BotSurface{Client: clients.Bots},
	}))
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	return &botCommandProductHarness{
		ctx: ctx, clients: clients, providerCalls: calls,
		messages: messages, model: model,
	}
}

func (h *botCommandProductHarness) apply(t *testing.T, msg tea.Msg) tea.Msg {
	t.Helper()
	unwrapped := unwrapSessionViewMessage(msg)
	if typed, ok := unwrapped.(SetCommandsMsg); ok {
		h.setCommands = append(h.setCommands, typed)
	}
	updated, _ := h.model.Update(msg)
	h.model = updated.(*Model)
	h.model.drainPendingRenderEvents(time.Now())
	h.model.flushPendingViewportSync()
	return unwrapped
}

// attach stages the Bot through selectBot, which owns the pending identity and
// publishes the active Bot only once the Session view attaches.
func (h *botCommandProductHarness) attach(t *testing.T, bot bot.Bot) {
	t.Helper()
	cmd := h.model.selectBot(bot)
	if cmd == nil {
		t.Fatal("selecting the Bot conversation scheduled no command")
	}
	if msg := cmd(); msg != nil {
		h.apply(t, msg)
	}
	// The Host refresh and the history marker are published concurrently, so
	// drain until both the refresh is observed and the view is committed.
	deadline := time.Now().Add(30 * time.Second)
	committed := false
	for time.Now().Before(deadline) {
		if committed && len(h.setCommands) > 0 {
			return
		}
		select {
		case msg := <-h.messages:
			if _, ready := h.apply(t, msg).(sessionHistoryReadyMsg); ready {
				committed = true
			}
		case <-h.ctx.Done():
			t.Fatalf("waiting for the Bot attach: %v", h.ctx.Err())
		}
	}
	t.Fatal("timed out waiting for the Bot attach")
}

func (h *botCommandProductHarness) sessionCount(t *testing.T) int {
	t.Helper()
	listed, err := h.clients.Sessions.ListSessions(h.ctx, appserver.ListSessionsRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(listed.Sessions)
}

// requireNoProviderOrExtraSession asserts the invariant every Bot command path
// preserves. Bot conversations are hidden from ordinary Session lists, so a
// non-zero count means a coding Session was created.
func (h *botCommandProductHarness) requireNoProviderOrExtraSession(t *testing.T) {
	t.Helper()
	if got := h.providerCalls.Load(); got != 0 {
		t.Fatalf("the model provider was invoked %d times; slash configuration must never reach a model", got)
	}
	if got := h.sessionCount(t); got != 0 {
		t.Fatalf("ordinary Session count = %d, want 0; no coding Session may be created", got)
	}
}

// assertBotCommandCatalog proves the command set, palette, slash menu, and
// details are the Bot catalog rather than the coding shell's.
func assertBotCommandCatalog(t *testing.T, model *Model) {
	t.Helper()
	want := BotCommands()
	if !slices.Equal(model.cfg.Commands, want) {
		t.Fatalf("command set = %#v, want the Bot catalog %#v", model.cfg.Commands, want)
	}

	items := model.palette.Items()
	gotPalette := make([]string, 0, len(items))
	for _, item := range items {
		typed, ok := item.(commandItem)
		if !ok {
			t.Fatalf("palette item %T is not a commandItem", item)
		}
		gotPalette = append(gotPalette, typed.name)
	}
	slices.Sort(gotPalette)
	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)
	if !slices.Equal(gotPalette, wantSorted) {
		t.Fatalf("palette = %#v, want the Bot catalog %#v", gotPalette, want)
	}

	assertBotCommandCandidates(t, model)
	gotMenu := make([]string, 0, len(model.slashCandidates))
	for _, candidate := range model.slashCandidates {
		gotMenu = append(gotMenu, strings.TrimPrefix(candidate, "/"))
	}
	for _, leaked := range []string{"compact", "plugin", "review", "breeze", "orbit", "zenith", "doctor", "resume"} {
		if slices.Contains(gotMenu, leaked) {
			t.Fatalf("slash menu = %#v, leaked coding command %q", model.slashCandidates, leaked)
		}
	}
	if got := model.commandCompletionDetail("new"); got != BotCommandDetails()["new"] {
		t.Fatalf("detail(/new) = %q, want the Bot detail %q", got, BotCommandDetails()["new"])
	}
	if got := model.commandCompletionDetail("team"); got != BotCommandDetails()["team"] {
		t.Fatalf("detail(/team) = %q, want the Bot detail %q", got, BotCommandDetails()["team"])
	}
}

// TestProductBotCommandCatalogSurvivesHostRefresh resumes a Bot conversation
// through a real Host. The attach makes the adapter publish a coding-shell
// SetCommandsMsg; the Bot catalog must survive both that refresh and a later
// agent/config refresh.
func TestProductBotCommandCatalogSurvivesHostRefresh(t *testing.T) {
	h := newBotCommandProductHarness(t)
	bot := createProductBot(t, h.ctx, h.clients.Bots, "Ada")

	h.attach(t, bot)
	if name := h.model.botName(); name != bot.Config.Name {
		t.Fatalf("active Bot = %q, want %q", name, bot.Config.Name)
	}
	if len(h.setCommands) == 0 {
		t.Fatal("the real attach published no SetCommandsMsg; the refresh path was not exercised")
	}
	refresh := h.setCommands[len(h.setCommands)-1]
	if !slices.Contains(refresh.Commands, "compact") || !slices.Contains(refresh.Commands, "plugin") {
		t.Fatalf("Host refresh = %#v, want the coding shell catalog", refresh.Commands)
	}
	assertBotCommandCatalog(t, h.model)
	if h.model.cfg.SkillComplete != nil {
		t.Fatal("Bot bridge exposes workspace skill completion")
	}

	refreshCmd := h.model.refreshAgentSlashCommandsCmd()
	if refreshCmd == nil {
		t.Fatal("agent command refresh scheduled no command")
	}
	h.apply(t, refreshCmd())
	assertBotCommandCatalog(t, h.model)

	h.requireNoProviderOrExtraSession(t)
}

// TestProductBotTeamOverlayReadsAndSavesHostConfiguration proves /team and its
// /subagent alias open the shared Host configuration overlay in Bot mode and
// that the overlay reads and persists real Host Agent-binding configuration.
func TestProductBotTeamOverlayReadsAndSavesHostConfiguration(t *testing.T) {
	h := newBotCommandProductHarness(t)
	bot := createProductBot(t, h.ctx, h.clients.Bots, "Grace")
	h.attach(t, bot)

	for _, command := range []string{"/team", "/subagent"} {
		t.Run(command, func(t *testing.T) {
			updated, cmd := h.model.submitInteractiveLine(command, command, nil)
			h.model = updated.(*Model)
			if cmd == nil || h.model.subagentOverlay == nil || !h.model.subagentOverlay.loading {
				t.Fatalf("%s did not open the Host configuration overlay: cmd=%v overlay=%#v", command, cmd != nil, h.model.subagentOverlay)
			}
			if h.model.wizard != nil || h.model.turnRunning() {
				t.Fatalf("%s opened the overlay with wizard=%v running=%v", command, h.model.wizard != nil, h.model.turnRunning())
			}

			updated, _ = h.model.Update(cmd())
			h.model = updated.(*Model)
			if h.model.subagentOverlay == nil || h.model.subagentOverlay.loading {
				t.Fatalf("%s overlay did not finish loading Host configuration", command)
			}
			if err := strings.TrimSpace(h.model.subagentOverlay.err); err != "" {
				t.Fatalf("%s overlay read error: %s", command, err)
			}
			if len(h.model.subagentOverlay.status.Handles) == 0 {
				t.Fatalf("%s overlay read no Host Agent configuration", command)
			}
			h.model.subagentOverlay = nil
		})
	}

	updated, openCmd := h.model.submitInteractiveLine("/team", "/team", nil)
	h.model = updated.(*Model)
	updated, _ = h.model.Update(openCmd())
	h.model = updated.(*Model)
	if h.model.subagentOverlay == nil || h.model.subagentOverlay.loading {
		t.Fatal("the configuration overlay did not load before saving")
	}

	_ = h.model.handleSubagentOverlayKey(tea.KeyPressMsg(tea.Key{Text: "s"}))
	h.model.renderSubagentOverlay()
	if h.model.subagentOverlay.page != subagentPageSaveSet {
		t.Fatalf("overlay page = %v, want the save-binding-set page", h.model.subagentOverlay.page)
	}
	_ = h.model.handleSubagentOverlayPaste(tea.PasteMsg{Content: "product-bot-team"})
	selectSubagentTestRow(t, h.model, "save")
	saveCmd := h.model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if saveCmd == nil {
		t.Fatal("saving a binding set returned no command")
	}
	updated, _ = h.model.Update(saveCmd())
	h.model = updated.(*Model)
	if h.model.subagentOverlay == nil {
		t.Fatal("the overlay closed after saving")
	}
	if err := strings.TrimSpace(h.model.subagentOverlay.err); err != "" {
		t.Fatalf("saving a binding set failed: %s", err)
	}

	status, err := h.clients.Agents.AgentBindingStatus(h.ctx, appserver.AgentRequest{Surface: "cli-bot"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, set := range status.Sets {
		if agentbinding.NormalizeSetName(set.Name) == "product-bot-team" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Host binding sets = %#v, want the saved %q", status.Sets, "product-bot-team")
	}

	// Host configuration never widens into participant/handoff execution. The
	// overlay is still open after saving; dismiss it before sending the command.
	h.model.subagentOverlay = nil
	updated, denied := h.model.submitInteractiveLine("/orbit go", "/orbit go", nil)
	h.model = updated.(*Model)
	if denied == nil || h.model.subagentOverlay != nil || h.model.turnRunning() || !strings.Contains(h.model.hint, "Unknown Bot command: /orbit") {
		t.Fatalf("/orbit was admitted in Bot mode: cmd=%v overlay=%v running=%v",
			denied != nil, h.model.subagentOverlay != nil, h.model.turnRunning())
	}

	h.requireNoProviderOrExtraSession(t)
}

// TestProductBotCreateWithDescription drives the real /new create flow against a
// real Host. The two prompt answers, including a multiline description, must
// persist on the created Bot with no model call and no coding Session.
func TestProductBotCreateWithDescription(t *testing.T) {
	h := newBotCommandProductHarness(t)

	answers := []string{"Ada", "Investigate flaky tests.\nReport only confirmed causes."}
	asked := 0
	var created *botFlowResultMsg
	send := func(msg tea.Msg) {
		switch typed := msg.(type) {
		case PromptRequestMsg:
			if asked >= len(answers) {
				t.Fatal("create flow asked for more input than a name and description")
			}
			answer := answers[asked]
			asked++
			typed.Response <- PromptResponse{Line: answer}
		case botFlowResultMsg:
			result := typed
			created = &result
		}
	}
	runBotCreateFlow(h.ctx, h.clients.Bots, send)
	if asked != len(answers) {
		t.Fatalf("create flow prompts = %d, want %d", asked, len(answers))
	}
	if created == nil || !created.created {
		t.Fatalf("create flow result = %#v", created)
	}

	got, err := h.clients.Bots.GetBot(h.ctx, created.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Name != answers[0] || got.Config.Description != answers[1] {
		t.Fatalf("created Bot config = %+v, want name %q description %q", got.Config, answers[0], answers[1])
	}

	state, err := h.clients.Sessions.InspectSession(h.ctx, appserver.StateRequest{SessionID: got.SessionID})
	if err != nil {
		t.Fatalf("inspecting the created conversation: %v", err)
	}
	if state.SessionID != got.SessionID || state.Run.Active {
		t.Fatalf("created conversation state = %#v", state)
	}

	h.requireNoProviderOrExtraSession(t)
}
