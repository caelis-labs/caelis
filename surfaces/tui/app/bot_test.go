package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/bot"
)

// fakeBotClient is an in-memory appserver.BotClient. UpdateBot replaces the
// stored Bot so GetBot observes the saved configuration, matching the Host.
// Outcome fields drive the client-visible recovery state under test.
type fakeBotClient struct {
	mu        sync.Mutex
	bots      []bot.Bot
	nextID    int
	created   []appserver.CreateBotRequest
	updated   []appserver.UpdateBotRequest
	createErr error
	updateErr error
	// createOutcome, when set to a non-committed outcome, makes CreateBot report
	// that outcome with createResult. createErr forces a transport-style
	// failure that may still carry a Resource identity.
	createOutcome appserver.Outcome
	createResult  appserver.CommandResult
	updateOutcome appserver.Outcome
	updateResult  appserver.CommandResult
}

func (f *fakeBotClient) ListBots(context.Context) ([]bot.Bot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bot.Bot(nil), f.bots...), nil
}

func (f *fakeBotClient) GetBot(_ context.Context, id string) (bot.Bot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, candidate := range f.bots {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return bot.Bot{}, fmt.Errorf("bot %q not found", id)
}

func (f *fakeBotClient) CreateBot(_ context.Context, req appserver.CreateBotRequest) (appserver.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req)
	if f.createErr != nil {
		result := f.createResult
		if result.Outcome == "" {
			result.Outcome = f.createOutcome
		}
		return result, f.createErr
	}
	if f.createOutcome != "" && f.createOutcome != appserver.OutcomeCommitted {
		result := f.createResult
		result.Outcome = f.createOutcome
		result.OperationID = req.OperationID
		return result, appserver.NewOutcomeError(f.createOutcome, errors.New("test outcome"))
	}
	f.nextID++
	id := fmt.Sprintf("bot-%032d", f.nextID)
	f.bots = append(f.bots, bot.Bot{
		ID:        id,
		SessionID: "bot-chat-" + id,
		Revision:  1,
		Config:    req.Config,
		// New Bots are notebook-enabled by Control; existing Bots stay legacy
		// until an explicit opt-in.
		NotebookEnabled: true,
	})
	return appserver.CommandResult{
		OperationID: req.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		SessionID:   "bot-chat-" + id,
		Resource:    &appserver.CommandResource{Kind: appserver.CommandResourceBot, Ref: id},
	}, nil
}

func (f *fakeBotClient) UpdateBot(_ context.Context, req appserver.UpdateBotRequest) (appserver.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, req)
	if f.updateErr != nil {
		return f.updateResult, f.updateErr
	}
	if f.updateOutcome != "" && f.updateOutcome != appserver.OutcomeCommitted {
		result := f.updateResult
		result.Outcome = f.updateOutcome
		result.OperationID = req.OperationID
		return result, appserver.NewOutcomeError(f.updateOutcome, errors.New("test outcome"))
	}
	for i, candidate := range f.bots {
		if candidate.ID == req.BotID {
			f.bots[i].Config = req.Config
			// A notebook enable is one-way: it is never cleared by a later save.
			if req.EnableNotebook {
				f.bots[i].NotebookEnabled = true
			}
			f.bots[i].Revision++
			return appserver.CommandResult{
				OperationID: req.OperationID, Outcome: appserver.OutcomeCommitted,
				SessionID: candidate.SessionID, Revision: f.bots[i].Revision,
			}, nil
		}
	}
	return appserver.CommandResult{}, errors.New("unknown bot")
}

func newBotTestModel(t *testing.T, width, height int, client appserver.BotClient, execute func(Submission) TaskResultMsg) *Model {
	t.Helper()
	model := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		ExecuteLine: execute,
		Commands:    BotCommands(),
		Bot:         &BotSurface{Client: client},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)
	// ConfigFromControlService forces local queueing for Bot mode, since the
	// Host rejects steering a running Bot reply.
	model.cfg.CanSubmitRunningPrompt = func() bool { return false }
	return model
}

func TestBotBootstrapSingleBotEntersDirectly(t *testing.T) {
	var submitted []Submission
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1,
		Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{ContinueRunning: true}
	})

	bootstrap := model.beginBotBootstrap()
	if bootstrap == nil {
		t.Fatal("beginBotBootstrap returned no command")
	}
	updated, resume := model.Update(bootstrap())
	model = updated.(*Model)
	if resume == nil {
		t.Fatal("entering the single Bot did not open its conversation")
	}
	_ = resume()
	if len(submitted) != 1 || submitted[0].Text != "/resume bot-chat-1" {
		t.Fatalf("submissions = %#v, want /resume bot-chat-1", submitted)
	}
	// The active Bot is published only once the Session view attaches.
	if model.activeBotSelected() {
		t.Fatal("Bot became active before its conversation attached")
	}
	model = publishBotForTest(t, model, "bot-chat-1")
	if !model.activeBotSelected() {
		t.Fatal("attached Bot did not become active")
	}
}

func TestBotBootstrapMultipleBotsOpensPicker(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 3, Config: bot.Config{Name: "Grace"}},
	}}
	model := newBotTestModel(t, 80, 24, client, nil)

	updated, load := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	if load == nil {
		t.Fatal("Bot picker did not schedule a list load")
	}
	updated, _ = model.Update(load())
	model = updated.(*Model)
	if model.sessionPicker == nil || !model.sessionPicker.botPicker {
		t.Fatalf("multiple Bots did not open the Bot picker: %+v", model.sessionPicker)
	}
	if len(model.sessionPicker.rows) != 2 {
		t.Fatalf("picker rows = %d, want 2", len(model.sessionPicker.rows))
	}
	frame := ansi.Strip(model.renderBotPicker())
	for _, want := range []string{"Bots", "Ada", "gpt-5", "Grace", "no model set"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("Bot picker omitted %q:\n%s", want, frame)
		}
	}
}

func TestBotPickerSelectionOpensConversation(t *testing.T) {
	var submitted []Submission
	client := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 3, Config: bot.Config{Name: "Grace"}},
	}}
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{ContinueRunning: true}
	})
	updated, load := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	if load == nil {
		t.Fatal("Bot picker did not schedule a list load")
	}
	updated, _ = model.Update(load())
	model = updated.(*Model)

	updated, _ = model.Update(keyPress("down"))
	model = updated.(*Model)
	updated, selectCmd := model.Update(keyPress("enter"))
	model = updated.(*Model)
	if model.sessionPicker != nil {
		t.Fatal("Enter did not close the Bot picker")
	}
	if selectCmd == nil {
		t.Fatal("Enter did not schedule the Bot conversation")
	}
	_ = selectCmd()
	if len(submitted) != 1 || submitted[0].Text != "/resume bot-chat-2" {
		t.Fatalf("submissions = %#v, want /resume bot-chat-2", submitted)
	}
	// Identity switches only after the selected conversation attaches.
	model = publishBotForTest(t, model, "bot-chat-2")
	if name := model.botName(); name != "Grace" {
		t.Fatalf("active Bot name = %q, want Grace", name)
	}
}

func TestBotChromeHidesWorkspaceAndShowsBotIdentity(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1,
		Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 100, 30, client, nil)
	model.cfg.Workspace = "/Users/example/secret-project"
	model.statusView.Workspace = "/Users/example/secret-project (main)"
	model.setWorkspaceDisplay("/Users/example/secret-project")

	updated, _ := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	model = publishBotForTest(t, model, "bot-chat-1")

	if got := model.headerWorkspaceText(); got != "" {
		t.Fatalf("header workspace text = %q, want hidden", got)
	}
	if got := model.botIdentityText(); got != "Ada · gpt-5" {
		t.Fatalf("Bot identity = %q, want Ada · gpt-5", got)
	}
	if got := model.windowTitle(); got != "Ada" {
		t.Fatalf("window title = %q, want Ada", got)
	}
	frame := ansi.Strip(model.View().Content)
	if strings.Contains(frame, "secret-project") {
		t.Fatalf("Bot frame leaked the workspace path:\n%s", frame)
	}
	if !strings.Contains(frame, "Ada") {
		t.Fatalf("Bot frame omitted the Bot name:\n%s", frame)
	}
}

func TestBotUnconfiguredModelIsVisible(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"},
	}}}
	model := newBotTestModel(t, 80, 24, client, nil)
	updated, _ := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	model = publishBotForTest(t, model, "bot-chat-1")
	if got := model.botIdentityText(); got != "Ada · no model set" {
		t.Fatalf("identity = %q, want Ada · no model set", got)
	}
}

func TestBotHelpListsOnlyBotCommands(t *testing.T) {
	client := &fakeBotClient{}
	model := newBotTestModel(t, 100, 30, client, nil)
	next, cmd, handled := model.submitBotLine("/help", "/help", nil)
	if !handled {
		t.Fatal("/help was not handled by the Bot surface")
	}
	_ = cmd
	model = next.(*Model)
	model.syncViewportContent()
	rendered := ansi.Strip(strings.Join(viewFrameLines(model), "\n"))
	for _, want := range []string{"/bots", "/settings", "/model", "/new", "/connect", "/disconnect", "/team", "/status", "/quit"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Bot help omitted %q:\n%s", want, rendered)
		}
	}
	for _, forbidden := range []string{"/plugin", "/review", "/breeze", "/compact"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("Bot help leaked coding command %q:\n%s", forbidden, rendered)
		}
	}
}

func TestBotCommandInterception(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"},
	}}}
	for _, line := range []string{"/new", "/bots", "/settings", "/model", "/resume"} {
		t.Run(line, func(t *testing.T) {
			model := newBotTestModel(t, 80, 24, client, nil)
			updated, _ := model.Update(model.beginBotBootstrap()())
			model = updated.(*Model)
			if _, _, handled := model.submitBotLine(line, line, nil); !handled {
				t.Fatalf("%s was not handled by the Bot surface", line)
			}
		})
	}
}

func TestBotChatIsQueuedWhileRunningInsteadOfSteering(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	var submitted []Submission
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{ContinueRunning: true}
	})
	// Bot mode never steers a running reply.
	updated, _ := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	model = publishBotForTest(t, model, "bot-chat-1")
	// The pending Session switch clears only when its result is delivered.
	model.sessionSwitchPending = false
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())

	_, cmd := model.submitInteractiveLine("keep working", "keep working", nil)
	if cmd != nil {
		t.Fatal("running Bot input dispatched immediately instead of queueing")
	}
	if len(submitted) != 0 {
		t.Fatalf("running Bot input was admitted: %#v", submitted)
	}
	if len(model.pendingQueue) == 0 {
		t.Fatal("running Bot input was not queued locally")
	}
}

func nextBotFlowResult(t *testing.T, send <-chan tea.Msg) botFlowResultMsg {
	t.Helper()
	for {
		select {
		case msg := <-send:
			if result, ok := msg.(botFlowResultMsg); ok {
				return result
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a Bot flow result")
		}
	}
}

func TestBotCommandSetExcludesWorkerMemoryAndCoding(t *testing.T) {
	allowed := map[string]bool{}
	for _, name := range BotCommands() {
		allowed[name] = true
	}
	for _, want := range []string{"help", "new", "bots", "settings", "model", "connect", "quit"} {
		if !allowed[want] {
			t.Fatalf("Bot command set is missing %q", want)
		}
	}
	for _, forbidden := range []string{"plugin", "review", "breeze", "orbit", "zenith", "compact"} {
		if allowed[forbidden] {
			t.Fatalf("Bot command set leaks coding command %q", forbidden)
		}
	}
}

func TestBotSettingsRefusedWhileReplyRuns(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 80, 24, client, nil)
	updated, _ := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	model.sessionSwitchPending = false
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())

	if cmd := model.startBotSettingsFlow(false); cmd == nil {
		t.Fatal("running Bot save did not surface a notice")
	}
	if model.activePrompt != nil {
		t.Fatal("running Bot save opened the settings prompt instead of refusing")
	}
}

func TestBotSettingsWithoutActiveBotShowsHint(t *testing.T) {
	model := newBotTestModel(t, 80, 24, &fakeBotClient{}, nil)
	if cmd := model.startBotSettingsFlow(false); cmd == nil {
		t.Fatal("settings without an active Bot did not surface a hint")
	}
}

func TestBotListErrorSurfacesHint(t *testing.T) {
	model := newBotTestModel(t, 80, 24, &fakeBotClient{}, nil)
	updated, cmd := model.Update(botListMsg{err: errors.New("host unavailable")})
	model = updated.(*Model)
	if cmd == nil {
		t.Fatal("Bot list error did not surface a hint")
	}
	if model.activeBotSelected() {
		t.Fatal("Bot list error selected a Bot")
	}
}

func viewFrameLines(model *Model) []string {
	return strings.Split(model.View().Content, "\n")
}

// publishBotForTest delivers a Session view start for sessionID, the signal the
// Model uses to commit a staged Bot selection.
func publishBotForTest(t *testing.T, model *Model, sessionID string) *Model {
	t.Helper()
	updated, _ := model.Update(sessionViewStartMsg{
		generation: model.viewGeneration + 1,
		state:      appserver.SessionState{SessionID: sessionID},
	})
	next := updated.(*Model)
	// A completed attach also commits its history, which clears the pending switch.
	if value, ok := next.activeBot(); ok && strings.TrimSpace(value.SessionID) == sessionID {
		next.sessionSwitchPending = false
	}
	return next
}

// barrierBotClient blocks UpdateBot until released so a settings save can be
// held in flight while the user switches Bots. No sleeps are used.
type barrierBotClient struct {
	*fakeBotClient
	entered chan struct{}
	release chan struct{}
}

func (b *barrierBotClient) UpdateBot(ctx context.Context, req appserver.UpdateBotRequest) (appserver.CommandResult, error) {
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return b.fakeBotClient.UpdateBot(ctx, req)
}

func TestBotImageOnlyInputBlockedBeforeBootstrap(t *testing.T) {
	var submitted []Submission
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"},
	}}}
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{}
	})
	_, cmd := model.submitInteractiveLine("", "shot.png", []Attachment{{Name: "shot.png"}})
	if cmd == nil {
		t.Fatal("image-only input before bootstrap did not surface a hint")
	}
	if len(submitted) != 0 {
		t.Fatalf("image-only input reached the router: %#v", submitted)
	}
}

func TestBotSettingsResultDoesNotOverrideSwitchedBot(t *testing.T) {
	base := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 1, Config: bot.Config{Name: "Grace", Model: "gpt-5"}},
	}}
	client := &barrierBotClient{fakeBotClient: base, entered: make(chan struct{}), release: make(chan struct{})}
	send := make(chan tea.Msg, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBotSettingsFlow(context.Background(), client, base.bots[0], bot.Config{Name: "Ada", Model: "claude-4"}, false, func(msg tea.Msg) { send <- msg })
	}()
	<-client.entered // Ada's save is now in flight inside UpdateBot

	model := newBotTestModel(t, 80, 24, client, nil)
	_ = model.selectBot(base.bots[0])
	model = publishBotForTest(t, model, "bot-chat-1")
	if name := model.botName(); name != "Ada" {
		t.Fatalf("precondition active = %q, want Ada", name)
	}
	if cmd := model.selectBot(base.bots[1]); cmd == nil {
		t.Fatal("switching Bots did not schedule an attach")
	}
	model = publishBotForTest(t, model, "bot-chat-2")
	if name := model.botName(); name != "Grace" {
		t.Fatalf("precondition switch active = %q, want Grace", name)
	}

	close(client.release)
	result := nextBotFlowResult(t, send)
	<-done
	updated, _ := model.Update(result)
	model = updated.(*Model)

	if name := model.botName(); name != "Grace" {
		t.Fatalf("in-flight save for Ada overrode the selected Bot: %q", name)
	}
	if session := model.botActiveSessionID(); session != "bot-chat-2" {
		t.Fatalf("selected conversation changed to %q", session)
	}
	refreshed := false
	for _, candidate := range model.bot.bots {
		if candidate.ID == "bot-1" && candidate.Config.Model == "claude-4" {
			refreshed = true
		}
	}
	if !refreshed {
		t.Fatalf("refreshed list entry for Ada is missing: %#v", model.bot.bots)
	}
}

func TestBotSettingsResultRefreshesStillActiveBot(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 4,
		Config: bot.Config{Name: "Ada", Model: "gpt-5", Effort: "high", Fast: true},
	}}}
	notices, result := runBotUpdateFlowResult(t, client, "claude-4")
	if result == nil {
		t.Fatal("committed update did not report a refreshed Bot")
	}
	model := newBotTestModel(t, 80, 24, client, nil)
	_ = model.selectBot(client.bots[0])
	model = publishBotForTest(t, model, "bot-chat-1")
	updated, cmd := model.Update(*result)
	model = updated.(*Model)
	if cmd == nil {
		t.Fatal("refresh of the active Bot did not surface a notice")
	}
	if model.botModelText() != "claude-4" {
		t.Fatalf("active Bot model = %q, want claude-4", model.botModelText())
	}
	if !strings.Contains(model.hint, "Bot settings saved") {
		t.Fatalf("save notice missing: hint=%q flow=%#v", model.hint, notices)
	}
}

func collectBotFlow(t *testing.T, send <-chan tea.Msg, done <-chan struct{}) ([]string, *botFlowResultMsg) {
	t.Helper()
	var notices []string
	var result *botFlowResultMsg
	record := func(msg tea.Msg) {
		switch typed := msg.(type) {
		case SlashNoticeMsg:
			notices = append(notices, typed.Text)
		case botFlowResultMsg:
			copied := typed
			result = &copied
		}
	}
	for {
		select {
		case msg := <-send:
			record(msg)
		case <-done:
			for {
				select {
				case msg := <-send:
					record(msg)
				default:
					return notices, result
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the Bot flow")
			return nil, nil
		}
	}
}

func runBotCreateFlowResult(t *testing.T, client appserver.BotClient) ([]string, *botFlowResultMsg) {
	t.Helper()
	send := make(chan tea.Msg, 16)
	done := make(chan struct{})
	runBotCreateFlow(t.Context(), client, bot.Config{Name: "Ada"}, func(msg tea.Msg) { send <- msg })
	close(done)
	return collectBotFlow(t, send, done)
}

func runBotUpdateFlowResult(t *testing.T, client appserver.BotClient, model string) ([]string, *botFlowResultMsg) {
	t.Helper()
	current, err := client.GetBot(t.Context(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	config := current.Config
	config.Model, config.Effort, config.Fast = model, "", false
	send := make(chan tea.Msg, 16)
	done := make(chan struct{})
	runBotSettingsFlow(t.Context(), client, current, config, false, func(msg tea.Msg) { send <- msg })
	close(done)
	return collectBotFlow(t, send, done)
}

func findBotNotice(notices []string, want string) bool {
	for _, notice := range notices {
		if strings.Contains(notice, want) {
			return true
		}
	}
	return false
}

func loadBotPickerForTest(t *testing.T, model *Model, cmd tea.Cmd) *Model {
	t.Helper()
	if model.sessionPicker == nil || cmd == nil {
		t.Fatal("Bot picker was not opened with a list load")
	}
	updated, _ := model.Update(cmd())
	return updated.(*Model)
}

func TestBotAllowlistRefusesNonBotCommands(t *testing.T) {
	var submitted []Submission
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{}
	})
	updated, _ := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	model = publishBotForTest(t, model, "bot-chat-1")
	model.sessionSwitchPending = false

	for _, line := range []string{"/plugin install x", "/review", "/breeze hi", "/compact", "/memory", "/skill:test"} {
		updated, cmd := model.submitInteractiveLine(line, line, nil)
		model = updated.(*Model)
		if cmd == nil {
			t.Fatalf("%s did not surface a refusal hint", line)
		}
	}
	if len(submitted) != 0 {
		t.Fatalf("non-Bot commands reached the router: %#v", submitted)
	}
}

func TestBotAllowlistStillRoutesConnect(t *testing.T) {
	client := &fakeBotClient{}
	model := newBotTestModel(t, 80, 24, client, nil)
	if _, _, handled := model.submitBotLine("/connect", "/connect", nil); handled {
		t.Fatal("/connect was blocked instead of routed to the provider wizard")
	}
	if _, _, handled := model.submitBotLine("/exit", "/exit", nil); handled {
		t.Fatal("/exit was blocked instead of reaching the exit path")
	}
}

func TestBotRecoveredUnknownOutcomeShowsInChrome(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 100, 30, client, nil)
	_ = model.selectBot(client.bots[0])
	model = publishBotForTest(t, model, "bot-chat-1")

	model.applySessionReconnectState(appserver.SessionState{
		SessionID: "bot-chat-1",
		Run:       appserver.RunState{Status: eventstream.LifecycleStateUnknown},
	})
	if !strings.Contains(model.botIdentityText(), botOutcomeUnknownText) {
		t.Fatalf("footer identity = %q, want an outcome-unknown marker", model.botIdentityText())
	}
	if got := model.botConversationText(); got != botOutcomeUnknownText {
		t.Fatalf("conversation text = %q, want %q", got, botOutcomeUnknownText)
	}
	model.syncViewportContent()
	frame := ansi.Strip(strings.Join(viewFrameLines(model), "\n"))
	if !strings.Contains(frame, botOutcomeUnknownText) {
		t.Fatalf("unknown outcome missing from the frame:\n%s", frame)
	}

	// Provisional UI work only hides the marker; canonical admission replaces it.
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	if strings.Contains(model.botIdentityText(), botOutcomeUnknownText) {
		t.Fatalf("a running reply kept the recovered marker: %q", model.botIdentityText())
	}
	model.stopLiveTurn()
	if !strings.Contains(model.botIdentityText(), botOutcomeUnknownText) {
		t.Fatalf("provisional work erased the recovered marker: %q", model.botIdentityText())
	}
	running := eventstream.TurnLifecycle("handle", "run", "turn", eventstream.LifecycleStateRunning, "", "", time.Now())
	running.SessionID = "bot-chat-1"
	model.handleACPEventEnvelope(running)
	completed := eventstream.TurnCompleted("handle", "run", "turn", time.Now())
	completed.SessionID = running.SessionID
	model.handleACPEventEnvelope(completed)
	if strings.Contains(model.botIdentityText(), botOutcomeUnknownText) {
		t.Fatalf("marker returned after the admitted turn ended: %q", model.botIdentityText())
	}
}

func TestBotNeverRunConversationIsIdle(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 100, 30, client, nil)
	_ = model.selectBot(client.bots[0])
	model = publishBotForTest(t, model, "bot-chat-1")
	model.applySessionReconnectState(appserver.SessionState{SessionID: "bot-chat-1"})

	if strings.Contains(model.botIdentityText(), botOutcomeUnknownText) {
		t.Fatalf("idle Bot showed an outcome marker: %q", model.botIdentityText())
	}
	if got := model.botConversationText(); got != "idle" {
		t.Fatalf("conversation text = %q, want idle", got)
	}
}

func TestBotRecoveredTerminalShowsInStatusNotChrome(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: "gpt-5"},
	}}}
	model := newBotTestModel(t, 100, 30, client, nil)
	_ = model.selectBot(client.bots[0])
	model = publishBotForTest(t, model, "bot-chat-1")
	model.applySessionReconnectState(appserver.SessionState{
		SessionID: "bot-chat-1",
		Run:       appserver.RunState{Status: eventstream.LifecycleStateCompleted},
	})

	if got := model.botConversationText(); got != "last reply completed" {
		t.Fatalf("conversation text = %q, want last reply completed", got)
	}
	if strings.Contains(model.botIdentityText(), "completed") {
		t.Fatalf("terminal status leaked into chrome: %q", model.botIdentityText())
	}
}

func TestBotStatusSummaryHidesWorkPathsAndSessionID(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-secret", Revision: 1,
		Config: bot.Config{Name: "Ada", Description: "helps with sums\nsecond line", Model: "gpt-5", Effort: "high"},
	}}}
	model := newBotTestModel(t, 100, 30, client, nil)
	updated, _ := model.Update(model.beginBotBootstrap()())
	model = updated.(*Model)
	model = publishBotForTest(t, model, "bot-chat-secret")
	model.cfg.Workspace = "/Users/example/secret-project"
	model.syncViewportContent()

	next, _, handled := model.submitBotLine("/status", "/status", nil)
	if !handled {
		t.Fatal("/status was not handled by the Bot surface")
	}
	model = next.(*Model)
	model.syncViewportContent()
	frame := ansi.Strip(strings.Join(viewFrameLines(model), "\n"))
	for _, want := range []string{"Ada", "gpt-5", "high", "idle", "helps with sums"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("Bot status omitted %q:\n%s", want, frame)
		}
	}
	for _, forbidden := range []string{"bot-chat-secret", "secret-project", "Sandbox", "Store"} {
		if strings.Contains(frame, forbidden) {
			t.Fatalf("Bot status leaked %q:\n%s", forbidden, frame)
		}
	}
}

func TestBotSelectionPublishesOnlyAfterAttach(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 1, Config: bot.Config{Name: "Grace"}},
	}}
	model := newBotTestModel(t, 80, 24, client, nil)
	if cmd := model.selectBot(client.bots[0]); cmd == nil {
		t.Fatal("selection did not schedule the conversation attach")
	}
	if model.activeBotSelected() {
		t.Fatal("Bot became active before its conversation attached")
	}
	if _, _, handled := model.submitBotLine("/bots", "/bots", nil); !handled {
		t.Fatal("/bots did not open the picker")
	}
	if model.activeBotSelected() {
		t.Fatal("opening the picker activated a Bot")
	}
	model = publishBotForTest(t, model, "bot-chat-2")
	if model.activeBotSelected() {
		t.Fatal("a view for an unrelated Session published the staged Bot")
	}
	model = publishBotForTest(t, model, "bot-chat-1")
	if name := model.botName(); name != "Ada" {
		t.Fatalf("attach did not publish the staged Bot: %q", name)
	}
}

func TestBotRepeatedSelectionKeepsAcceptedTarget(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-c", SessionID: "chat-c", Config: bot.Config{Name: "C"}},
		{ID: "bot-a", SessionID: "chat-a", Config: bot.Config{Name: "A"}},
		{ID: "bot-b", SessionID: "chat-b", Config: bot.Config{Name: "B"}},
	}}
	var target, prompted string
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		if strings.HasPrefix(sub.Text, "/resume ") {
			target = strings.TrimPrefix(sub.Text, "/resume ")
		} else {
			prompted = target
		}
		return TaskResultMsg{}
	})
	attach := func(cmd tea.Cmd, id string) {
		t.Helper()
		if cmd == nil {
			t.Fatal("selection was not admitted")
		}
		result := cmd()
		model.Update(sessionViewStartMsg{generation: model.viewGeneration + 1, state: appserver.SessionState{SessionID: id}})
		model.Update(sessionHistoryReadyMsg{})
		model.Update(result)
	}
	attach(model.selectBot(client.bots[0]), "chat-c")
	accepted := model.selectBot(client.bots[1])
	if rejected := model.selectBot(client.bots[2]); rejected != nil {
		t.Fatal("overlapping selection was admitted")
	}
	if model.bot.pending.ID != "bot-a" || model.botName() != "C" || !model.sessionSwitchPending {
		t.Fatalf("rejected selection changed accepted target: %+v", model.bot)
	}
	attach(accepted, "chat-a")
	if model.botName() != "A" || model.bot.hasPending || model.currentSessionID != target || model.sessionSwitchPending {
		t.Fatalf("identity and target diverged after attach: bot=%+v session=%q target=%q switching=%v", model.bot, model.currentSessionID, target, model.sessionSwitchPending)
	}
	_, cmd := model.submitInteractiveLine("hello A", "hello A", nil)
	if cmd == nil {
		t.Fatal("input stayed blocked after attach")
	}
	runTeaCmds(t, model, cmd)
	if prompted != "chat-a" {
		t.Fatalf("input targeted %q, want displayed Bot A", prompted)
	}
}

func TestBotFailedAttachKeepsPreviousBot(t *testing.T) {
	client := &fakeBotClient{bots: []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: 1, Config: bot.Config{Name: "Grace"}},
	}}
	var submitted []Submission
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{}
	})
	if cmd := model.selectBot(client.bots[0]); cmd == nil {
		t.Fatal("selecting the first Bot did not open its conversation")
	}
	model = publishBotForTest(t, model, "bot-chat-1")
	if name := model.botName(); name != "Ada" {
		t.Fatalf("precondition: active = %q, want Ada", name)
	}

	if _, openCmd, handled := model.submitBotLine("/bots", "/bots", nil); !handled {
		t.Fatal("/bots did not open the picker")
	} else {
		model = loadBotPickerForTest(t, model, openCmd)
	}
	updated, _ := model.Update(keyPress("down"))
	model = updated.(*Model)
	updated, selectCmd := model.Update(keyPress("enter"))
	model = updated.(*Model)
	if selectCmd == nil {
		t.Fatal("selection did not schedule the conversation attach")
	}
	_ = selectCmd()
	if name := model.botName(); name != "Ada" {
		t.Fatalf("pending selection changed displayed Bot to %q before attach", name)
	}

	// The attach fails; the previously published Bot must remain the target.
	updated, _ = model.Update(sessionObservationErrorMsg{err: errors.New("attach failed")})
	model = updated.(*Model)
	if name := model.botName(); name != "Ada" {
		t.Fatalf("failed attach changed active Bot to %q, want Ada", name)
	}
	isLast := func(text string) bool {
		return len(submitted) > 0 && submitted[len(submitted)-1].Text == text
	}
	if !isLast("/resume bot-chat-2") {
		t.Fatalf("unexpected routing: %#v", submitted)
	}
}

func TestBotInputBlockedBeforeBootstrap(t *testing.T) {
	var submitted []Submission
	client := &fakeBotClient{bots: []bot.Bot{{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada"},
	}}}
	model := newBotTestModel(t, 80, 24, client, func(sub Submission) TaskResultMsg {
		submitted = append(submitted, sub)
		return TaskResultMsg{}
	})
	_, cmd := model.submitInteractiveLine("hello", "hello", nil)
	if cmd == nil {
		t.Fatal("input before bootstrap did not surface a hint")
	}
	if len(submitted) != 0 {
		t.Fatalf("input before bootstrap reached the router: %#v", submitted)
	}
}

func TestRunBotCreateFlowUnknownOutcomeDoesNotClaimCreated(t *testing.T) {
	client := &fakeBotClient{createOutcome: appserver.OutcomeUnknown}
	notices, result := runBotCreateFlowResult(t, client)
	if result != nil {
		t.Fatalf("unknown creation reported a created Bot: %#v", result.bot)
	}
	if !findBotNotice(notices, "inspect /bots") {
		t.Fatalf("unknown creation did not point at /bots: %#v", notices)
	}
}

func TestRunBotCreateFlowUnknownOutcomeReconcilesByIdentity(t *testing.T) {
	client := &fakeBotClient{
		bots:          []bot.Bot{{ID: "bot-known", SessionID: "bot-chat-known", Revision: 1, Config: bot.Config{Name: "Ada"}}},
		createOutcome: appserver.OutcomeUnknown,
		createResult: appserver.CommandResult{
			Resource: &appserver.CommandResource{Kind: appserver.CommandResourceBot, Ref: "bot-known"},
		},
	}
	notices, result := runBotCreateFlowResult(t, client)
	if result == nil || !result.created || result.bot.ID != "bot-known" {
		t.Fatalf("identity reconcile did not adopt the existing Bot: %#v", result)
	}
	if len(client.created) != 1 {
		t.Fatalf("reconcile resent creation: %d calls", len(client.created))
	}
	if !findBotNotice(notices, "already created") {
		t.Fatalf("reconcile did not explain itself: %#v", notices)
	}
}

func TestRunBotCreateFlowTransportErrorKeepsIdentityHint(t *testing.T) {
	client := &fakeBotClient{createErr: errors.New("transport reset")}
	notices, result := runBotCreateFlowResult(t, client)
	if result != nil {
		t.Fatalf("transport failure reported a created Bot: %#v", result.bot)
	}
	if !findBotNotice(notices, "/bots") {
		t.Fatalf("transport failure did not point at /bots: %#v", notices)
	}
	if findBotNotice(notices, "Could not create Bot: ") == false {
		t.Fatalf("transport failure did not report the error: %#v", notices)
	}
}

func TestRunBotCreateFlowConflictNotice(t *testing.T) {
	client := &fakeBotClient{createOutcome: appserver.OutcomeConflicted}
	notices, result := runBotCreateFlowResult(t, client)
	if result != nil {
		t.Fatalf("conflicted creation reported success: %#v", result.bot)
	}
	if !findBotNotice(notices, "conflicted") {
		t.Fatalf("conflicted creation did not report the conflict: %#v", notices)
	}
}

func TestRunBotUpdateFlowConflictKeepsConfig(t *testing.T) {
	client := &fakeBotClient{
		bots:          []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}}},
		updateOutcome: appserver.OutcomeConflicted,
	}
	notices, result := runBotUpdateFlowResult(t, client, "claude-4")
	if result != nil {
		t.Fatalf("conflicted update reported success: %#v", result.bot)
	}
	if !findBotNotice(notices, "not saved") {
		t.Fatalf("conflicted update did not explain the conflict: %#v", notices)
	}
	stored, err := client.GetBot(context.Background(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Config.Model != "gpt-5" {
		t.Fatalf("conflicted update changed stored config: %#v", stored.Config)
	}
}

func TestRunBotUpdateFlowUnknownDoesNotReportSaved(t *testing.T) {
	client := &fakeBotClient{
		bots:          []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}}},
		updateOutcome: appserver.OutcomeUnknown,
	}
	notices, result := runBotUpdateFlowResult(t, client, "claude-4")
	if result != nil {
		t.Fatalf("unknown update reported success: %#v", result.bot)
	}
	if !findBotNotice(notices, "outcome unknown") || !findBotNotice(notices, "/settings") {
		t.Fatalf("unknown update did not point at /settings: %#v", notices)
	}
}

func TestRunBotUpdateFlowAcceptedIsNotSuccess(t *testing.T) {
	client := &fakeBotClient{
		bots:          []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}}},
		updateOutcome: appserver.OutcomeAccepted,
	}
	notices, result := runBotUpdateFlowResult(t, client, "claude-4")
	if result != nil {
		t.Fatalf("accepted update reported success: %#v", result.bot)
	}
	if !findBotNotice(notices, "outcome unknown") {
		t.Fatalf("accepted update was not treated as unproven: %#v", notices)
	}
}
