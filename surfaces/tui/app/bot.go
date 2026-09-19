package tuiapp

// bot.go implements the standalone Bot product mode of the shared TUI Model.
//
// A Bot is a named assistant with one private canonical conversation. Bot mode
// reuses the ordinary Session path for chat, streaming, history, reconnect, and
// drafts; it only changes what the surface presents: named Bots instead of
// workspace Sessions, hidden workspace/branch/Session chrome, a restricted
// command set, and Bot configuration through the focused Bot client.

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const (
	// botSaveAfterReplyNotice explains why a settings save is deferred while a
	// reply is still streaming. Saving never rotates the Bot's conversation.
	botSaveAfterReplyNotice = "Wait for the reply to finish before saving."
	// botCreateConflictNotice and botUpdateConflictNotice report a failed CAS. The
	// original configuration and any local draft are preserved for review.
	botCreateConflictNotice = "Creation conflicted. Review /bots."
	botUpdateConflictNotice = "Bot changed elsewhere; edit not saved. Reopen /settings."
	// botCreateUnknownNotice and botUpdateUnknownNotice report an unprovable
	// effect. Nothing is retried automatically; the user inspects first.
	botCreateUnknownNotice = "Creation outcome unknown; inspect /bots before retrying."
	botUpdateUnknownNotice = "Save outcome unknown. Review /settings before retrying."
	// botCreateFailedSuffix never claims a failed creation had no effect.
	botCreateFailedSuffix = " If a Bot was created it will appear in /bots."
	// botOutcomeUnknownText marks a recovered Run whose terminal state Control
	// could not prove. It stays in chrome until new work starts.
	botOutcomeUnknownText = "outcome unknown"
)

// BotSurface opts the shared TUI Model into Bot mode. When set, the Model
// presents named Bots and their private conversation instead of workspace
// Sessions: it hides workspace, branch, and Session-identity chrome, restricts
// the command set, and routes Bot configuration through the focused client.
// Chat itself remains the ordinary Session path.
type BotSurface struct {
	Client appserver.BotClient
}

type botSurfaceState struct {
	client    appserver.BotClient
	bots      []bot.Bot
	active    bot.Bot
	hasActive bool
	// runStatus is the last observed Run.Status for the active Bot's
	// conversation. A recovered unknown_outcome stays visible until new work
	// starts, while a locally running reply shows its own indicator.
	runStatus string
	// pending is a selection awaiting its Session attach. The active Bot is
	// published only after the conversation is observed, so a failed attach can
	// never leave displayed identity and command target out of sync.
	pending    bot.Bot
	hasPending bool
}

// botCommandStatus is the client-visible recovery state of one Bot mutation.
// Only botCommandCommitted is success; everything else preserves local state.
type botCommandStatus int

const (
	botCommandCommitted botCommandStatus = iota
	botCommandConflict
	botCommandUnknown
	botCommandFailed
)

// botListMsg is the bootstrap listing of Bots owned by the current principal.
type botListMsg struct {
	bots []bot.Bot
	err  error
}

// botPickerResultMsg refreshes the Bot selection overlay.
type botPickerResultMsg struct {
	request uint64
	bots    []bot.Bot
	err     error
}

// botFlowResultMsg reports a completed create or settings flow. created marks a
// flow that must open the new Bot's conversation.
type botFlowResultMsg struct {
	created bool
	bot     bot.Bot
}

func (m *Model) botMode() bool {
	return m != nil && m.bot != nil
}

// botHintOptions keeps transient Bot guidance consistent: high priority, cleared
// on the next message or after a short delay.
func botHintOptions() hintOptions {
	return hintOptions{priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration}
}

// BotCommands returns Bot chat and shared Host configuration commands. Host
// configuration does not grant the Bot coding, participant, or Memory tools.
func BotCommands() []string {
	return []string{"help", "new", "bots", "settings", "model", "connect", "disconnect", "team", "status", "theme", "quit"}
}

// BotCommandDetails labels the Bot command set. Names shared with the coding
// shell carry Bot-specific wording.
func BotCommandDetails() map[string]string {
	return map[string]string{
		"new":        "Create a new Bot",
		"bots":       "Switch between Bots",
		"settings":   "Edit this Bot's name, description, and model",
		"model":      "Change this Bot's model",
		"connect":    "Connect a provider or ACP Agent to the Host",
		"disconnect": "Disconnect a provider or ACP Agent from the Host",
		"team":       "Configure Host Agent bindings",
		"status":     "Show this Bot's settings and conversation status",
		"theme":      "Change the TUI theme",
		"quit":       "Exit the Bot TUI",
	}
}

// botManagedCommands are handled entirely by the Bot surface. Their slash
// args must not auto-open the coding shell's Control pickers, and their Enter
// path is intercepted before the ordinary router.
func isBotManagedCommand(name string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/"))) {
	case "new", "bots", "resume", "settings", "model":
		return true
	default:
		return false
	}
}

// beginBotBootstrap lists the principal's Bots and reconciles the surface: zero
// opens the create flow, one enters that Bot directly, and several
// open the selection overlay.
func (m *Model) beginBotBootstrap() tea.Cmd {
	if m == nil || m.bot == nil || m.bot.client == nil {
		return nil
	}
	client := m.bot.client
	ctx := contextOrBackground(m.cfg.Context)
	return func() tea.Msg {
		bots, err := client.ListBots(ctx)
		return botListMsg{bots: bots, err: err}
	}
}

func (m *Model) handleBotList(msg botListMsg) tea.Cmd {
	if msg.err != nil {
		return m.showHint("Could not load Bots: "+msg.err.Error(), botHintOptions())
	}
	m.bot.bots = msg.bots
	switch len(msg.bots) {
	case 0:
		return m.startBotCreateFlow()
	case 1:
		return m.selectBot(msg.bots[0])
	default:
		return m.openBotPicker()
	}
}

// selectBot stages one Bot as the pending conversation and opens it through the
// ordinary Session switch path. The active Bot is published only once the
// Session view attaches (publishPendingBot); a failed attach keeps the previous
// Bot so the displayed name can never diverge from the command target.
func (m *Model) selectBot(value bot.Bot) tea.Cmd {
	if m == nil || m.bot == nil {
		return nil
	}
	if m.sessionPicker != nil {
		m.closeSessionPicker()
	}
	if strings.TrimSpace(value.SessionID) == "" {
		return m.showHint("Bot has no conversation yet", botHintOptions())
	}
	cmd := m.executeLineCmd(Submission{Text: "/resume " + value.SessionID})
	if cmd != nil {
		// Only an admitted switch owns the pending identity. An overlapping
		// selection must not replace the Bot whose attach is already in flight.
		m.bot.pending = value
		m.bot.hasPending = true
	}
	return cmd
}

// publishPendingBot commits a staged selection once its Session view attaches.
// An unrelated view leaves the staged selection waiting for its own attach; a
// failed attach is reported separately through failPendingBot.
func (m *Model) publishPendingBot(sessionID string) {
	if m == nil || m.bot == nil || !m.bot.hasPending {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || sessionID != strings.TrimSpace(m.bot.pending.SessionID) {
		return
	}
	m.bot.active = m.bot.pending
	m.bot.hasActive = true
	m.bot.hasPending = false
	m.bot.pending = bot.Bot{}
	// The previous conversation's recovered status never belongs to the new Bot.
	m.bot.runStatus = ""
}

// failPendingBot drops a staged selection whose attach failed, leaving the
// previously published Bot (if any) in place. The attach error is reported by
// the ordinary Session-switch error path.
func (m *Model) failPendingBot() {
	if m == nil || m.bot == nil || !m.bot.hasPending {
		return
	}
	m.bot.hasPending = false
	m.bot.pending = bot.Bot{}
}

func (m *Model) activeBot() (bot.Bot, bool) {
	if m == nil || m.bot == nil || !m.bot.hasActive {
		return bot.Bot{}, false
	}
	return m.bot.active, true
}

func (m *Model) botBySessionID(sessionID string) (bot.Bot, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || m.bot == nil || sessionID == "" {
		return bot.Bot{}, false
	}
	for _, candidate := range m.bot.bots {
		if strings.TrimSpace(candidate.SessionID) == sessionID {
			return candidate, true
		}
	}
	if m.bot.hasActive && strings.TrimSpace(m.bot.active.SessionID) == sessionID {
		return m.bot.active, true
	}
	return bot.Bot{}, false
}

// botModelDisplay renders one Bot read's model for presentation. Control
// resolves the public selector for the durable Config.Model identity; a Bot
// read without a resolved selector shows the durable ID. Durable IDs are never
// parsed here.
func botModelDisplay(value bot.Bot) string {
	if selector := strings.TrimSpace(value.ModelSelector); selector != "" {
		return selector
	}
	return strings.TrimSpace(value.Config.Model)
}

// botName and botModelText drive Bot chrome. An unconfigured model is shown
// explicitly so a fail-closed prompt is not mistaken for an idle Bot.
func (m *Model) botName() string {
	value, ok := m.activeBot()
	if !ok {
		return ""
	}
	return strings.TrimSpace(value.Config.Name)
}

func (m *Model) botModelText() string {
	value, ok := m.activeBot()
	if !ok {
		return ""
	}
	model := botModelDisplay(value)
	if model == "" {
		return "no model set"
	}
	return model
}

func (m *Model) botIdentityText() string {
	name := m.botName()
	model := m.botModelText()
	var base string
	switch {
	case name == "" && model == "":
		base = ""
	case model == "":
		base = name
	case name == "":
		base = model
	default:
		base = name + " · " + model
	}
	if m.botOutcomeUnknown() {
		if base == "" {
			return botOutcomeUnknownText
		}
		return base + " · " + botOutcomeUnknownText
	}
	return base
}

// botOutcomeUnknown reports a recovered Run without a provable terminal. It is
// hidden while a local reply runs, whose own indicator is current.
func (m *Model) botOutcomeUnknown() bool {
	if m == nil || m.bot == nil || m.turnRunning() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(m.bot.runStatus), eventstream.LifecycleStateUnknown)
}

// botConversationText describes the active Bot's conversation for /status.
func (m *Model) botConversationText() string {
	if m.turnRunning() {
		return "replying"
	}
	switch status := strings.TrimSpace(m.botRunStatus()); status {
	case "":
		return "idle"
	case eventstream.LifecycleStateUnknown:
		return botOutcomeUnknownText
	case eventstream.LifecycleStateCompleted:
		return "last reply completed"
	case eventstream.LifecycleStateFailed:
		return "last reply failed"
	case eventstream.LifecycleStateInterrupted:
		return "last reply interrupted"
	default:
		return "last reply " + status
	}
}

func (m *Model) botRunStatus() string {
	if m == nil || m.bot == nil {
		return ""
	}
	return m.bot.runStatus
}

// openBotPicker shows the Bot selection overlay, reusing the Session picker's
// geometry, keyboard, and mouse plumbing.
func (m *Model) openBotPicker() tea.Cmd {
	if m == nil || m.bot == nil {
		return nil
	}
	if m.sessionPicker != nil {
		return nil
	}
	m.clearInputOverlays()
	m.sessionPickerSeq++
	m.sessionPicker = &sessionPickerState{request: m.sessionPickerSeq, botPicker: true}
	return m.loadBotPicker()
}

func (m *Model) loadBotPicker() tea.Cmd {
	state := m.sessionPicker
	if state == nil || !state.botPicker || state.loading || m.bot == nil || m.bot.client == nil {
		return nil
	}
	state.loading = true
	ctx, cancel := context.WithCancel(contextOrBackground(m.cfg.Context))
	state.cancel = cancel
	request := state.request
	client := m.bot.client
	return func() tea.Msg {
		bots, err := client.ListBots(ctx)
		return botPickerResultMsg{request: request, bots: bots, err: err}
	}
}

func (m *Model) applyBotPickerResult(msg botPickerResultMsg) {
	state := m.sessionPicker
	if state == nil || !state.botPicker || state.request != msg.request {
		return
	}
	state.loading = false
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	if msg.err != nil {
		state.err = msg.err.Error()
		return
	}
	m.bot.bots = msg.bots
	state.allRows = m.botPickerRows(msg.bots)
	state.filterRows()
	state.err = ""
	state.geometry = subagentOverlayGeometry{}
	state.index = clampInt(state.index, 0, maxInt(0, len(state.rows)-1))
	selected := ""
	if m.bot.hasActive {
		selected = strings.TrimSpace(m.bot.active.SessionID)
	}
	for i, row := range state.rows {
		if row.SessionID == selected {
			state.index = i
			break
		}
	}
}

func (m *Model) botPickerRows(bots []bot.Bot) []ResumeCandidate {
	rows := make([]ResumeCandidate, 0, len(bots))
	for _, value := range bots {
		rows = append(rows, ResumeCandidate{
			SessionID: strings.TrimSpace(value.SessionID),
			Title:     strings.TrimSpace(value.Config.Name),
			Prompt:    botModelDisplay(value),
		})
	}
	return rows
}

// renderBotPicker draws the Bot selection overlay. It mirrors the Session
// picker frame so both share one centered overlay contract.
func (m *Model) renderBotPicker() string {
	state := m.sessionPicker
	if state == nil || !state.botPicker {
		return ""
	}
	width := min(112, maxInt(20, m.width-4))
	inner := maxInt(1, width-m.overlayBorderChromeWidth())
	title := m.theme.TitleStyle().Render("/bots · Bots")
	body := []string{title + strings.Repeat(" ", maxInt(1, inner-displayColumns(title)-1)) + "×", m.theme.HelpHintTextStyle().Render(state.searchLine(inner))}
	count := minInt(len(state.rows), maxInt(1, m.height-9))
	start := maxInt(state.index-count+1, minInt(state.offset, state.index))
	start = clampInt(start, 0, maxInt(0, len(state.rows)-count))
	state.offset = start
	rowOffsets := make([]int, len(state.rows))
	for i := range rowOffsets {
		rowOffsets[i] = -1
	}
	for i := start; i < start+count; i++ {
		row := state.rows[i]
		label := strings.Join(strings.Fields(row.Title), " ")
		if label == "" {
			label = "Untitled Bot"
		}
		model := strings.TrimSpace(row.Prompt)
		if model == "" {
			model = "no model set"
		}
		current := row.SessionID != "" && row.SessionID == strings.TrimSpace(m.botActiveSessionID())
		prefix := "  "
		if i == state.index {
			prefix = "> "
		}
		line := botPickerRowLine(inner, prefix, label, model, current)
		if i == state.index {
			line = m.theme.CommandActiveStyle().Padding(0).Render(line)
		}
		rowOffsets[i] = len(body)
		body = append(body, line)
	}
	if len(state.rows) == 0 {
		text := "No Bots yet"
		if state.loading {
			text = "Loading Bots…"
		}
		body = append(body, m.theme.MutedTextStyle().Render(text))
	}
	if state.err != "" {
		body = append(body, m.theme.ErrorStyle().Render(truncateTailDisplay(state.err, inner)))
	}
	body = append(body, "")
	body = append(body, m.theme.HelpHintTextStyle().Render(truncateTailDisplay("↑↓ select  enter open  esc close", inner)))
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{Body: body, Width: width, UseBorder: m.overlayUsesBorder()})
	w, h := lipgloss.Width(frame), lipgloss.Height(frame)
	x, y := maxInt(0, (m.width-w)/2), maxInt(0, (m.height-h)/2)
	inset := 0
	if m.overlayUsesBorder() {
		inset = 1
	}
	for i, offset := range rowOffsets {
		if offset >= 0 {
			rowOffsets[i] = y + inset + offset
		}
	}
	state.geometry = subagentOverlayGeometry{x: x, y: y, width: w, height: h, rows: rowOffsets, closeX: x + w - 1 - m.overlayBorderChromeWidth()/2, closeY: y + inset}
	return frame
}

func (m *Model) botActiveSessionID() string {
	value, ok := m.activeBot()
	if !ok {
		return ""
	}
	return strings.TrimSpace(value.SessionID)
}

// submitBotLine handles Bot-mode command lines before they reach the ordinary
// router. It reports handled=false so chat text and shared commands keep the
// normal Session path.
func (m *Model) submitBotLine(execLine string, displayLine string, attachments []Attachment) (tea.Model, tea.Cmd, bool) {
	if !m.botMode() {
		return m, nil, false
	}
	trimmed := strings.TrimSpace(execLine)
	switch slashCommandName(trimmed) {
	case "new":
		m.resetComposerAfterOverlayOpen()
		return m, m.startBotCreateFlow(), true
	case "bots":
		m.resetComposerAfterOverlayOpen()
		return m, m.openBotPicker(), true
	case "resume":
		m.resetComposerAfterOverlayOpen()
		return m, m.openBotPicker(), true
	case "settings":
		m.resetComposerAfterOverlayOpen()
		return m, m.startBotSettingsFlow(false), true
	case "model":
		m.resetComposerAfterOverlayOpen()
		return m, m.startBotSettingsFlow(true), true
	case "help":
		m.resetComposerAfterOverlayOpen()
		return m.showBotHelp()
	case "status":
		m.resetComposerAfterOverlayOpen()
		return m.showBotStatus()
	}
	if strings.HasPrefix(trimmed, "/") {
		name := slashCommandName(trimmed)
		if name != "" && !isBotCommand(name) && name != "exit" && name != "quit" {
			// A real allowlist: unknown slash commands are refused instead of
			// reaching the coding shell's router.
			m.resetComposerAfterOverlayOpen()
			return m, m.showHint("Unknown Bot command: /"+name+". Type /help for Bot commands.", botHintOptions()), true
		}
	}
	if !strings.HasPrefix(trimmed, "/") && (trimmed != "" || len(attachments) > 0) && !m.activeBotSelected() {
		// Text, image-only, and file submissions alike are work-bearing and must
		// not reach the Host before a Bot conversation is attached.
		return m, m.showHint("Create or select a Bot first", botHintOptions()), true
	}
	return m, nil, false
}

// isBotCommand accepts the discoverable Bot command set and its shared aliases.
func isBotCommand(name string) bool {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
	if spec, ok := controlprompt.Lookup(name); ok {
		name = spec.Name
	}
	for _, candidate := range BotCommands() {
		if candidate == name {
			return true
		}
	}
	return false
}

func (m *Model) activeBotSelected() bool {
	_, ok := m.activeBot()
	return ok
}

// showBotHelp renders the restricted Bot command set. The shared /help path
// describes the full coding shell, which must not be discoverable here.
func (m *Model) showBotHelp() (tea.Model, tea.Cmd, bool) {
	details := BotCommandDetails()
	table := make([][]string, 0, len(BotCommands()))
	for _, name := range BotCommands() {
		detail := strings.TrimSpace(details[name])
		if detail == "" {
			if spec, ok := controlprompt.Lookup(name); ok {
				detail = strings.TrimSpace(spec.Description)
			}
		}
		table = append(table, []string{"/" + name, detail})
	}
	lines := []slashOutputLine{slashSection("Commands")}
	lines = append(lines, renderSlashPaddedRows(table)...)
	lines = append(lines, slashBlank())
	lines = append(lines, slashOutputLine{Text: "Type a message and press Enter to chat.", Plain: true})
	model, cmd := m.appendSlashOutputLines(lines)
	return model, cmd, true
}

// showBotStatus renders a short Bot summary. It intentionally omits workspace
// paths, Session identity, sandbox, and store details.
func (m *Model) showBotStatus() (tea.Model, tea.Cmd, bool) {
	value, ok := m.activeBot()
	if !ok {
		return m, m.showHint("Select or create a Bot first", botHintOptions()), true
	}
	table := [][]string{
		{"Name", displayOrNone(value.Config.Name)},
		{"Model", displayOrNone(botModelDisplay(value))},
	}
	if effort := strings.TrimSpace(value.Config.Effort); effort != "" {
		table = append(table, []string{"Effort", effort})
	}
	if value.Config.Fast {
		table = append(table, []string{"Fast", "on"})
	}
	table = append(table, []string{"Conversation", m.botConversationText()})
	if description := strings.TrimSpace(value.Config.Description); description != "" {
		table = append(table, []string{"Description", firstLineDisplay(description)})
	}
	lines := []slashOutputLine{slashSection("Bot")}
	lines = append(lines, renderSlashPaddedRows(table)...)
	model, cmd := m.appendSlashOutputLines(lines)
	return model, cmd, true
}

// firstLineDisplay collapses a description to its first non-empty line so the
// status summary stays brief.
func firstLineDisplay(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// botFlowContext returns the Program-lifetime context for one Bot flow so a
// closed program cancels pending reads and writes.
func (m *Model) botFlowContext() context.Context {
	if m.cfg.ProgramSender != nil {
		return m.cfg.ProgramSender.observationContext(m.cfg.Context)
	}
	return contextOrBackground(m.cfg.Context)
}

func runBotCreateFlow(ctx context.Context, client appserver.BotClient, config bot.Config, send func(tea.Msg)) {
	result, err := client.CreateBot(ctx, appserver.CreateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "bot-create-" + uuid.NewString()},
		Config:    config,
	})
	switch botCommandStatusOf(result, err) {
	case botCommandCommitted:
		id := botResourceID(result)
		if id == "" {
			sendNotice(send, "Bot was created but its identity was not reported; inspect /bots.", SlashNoticeHint)
			return
		}
		created, getErr := client.GetBot(ctx, id)
		if getErr != nil {
			sendNotice(send, "Bot was created but could not be opened: "+getErr.Error()+"; inspect /bots.", SlashNoticeHint)
			return
		}
		send(botFlowResultMsg{created: true, bot: created})
	case botCommandConflict:
		sendNotice(send, botCreateConflictNotice, SlashNoticeHint)
	case botCommandUnknown:
		if existing, ok := reconcileCreatedBot(ctx, client, result); ok {
			sendNotice(send, "The Bot was already created; opening it.", SlashNoticeFeedback)
			send(botFlowResultMsg{created: true, bot: existing})
			return
		}
		sendNotice(send, botCreateUnknownNotice, SlashNoticeHint)
	default:
		// Unlike settings, a failed creation may still have committed. A pure
		// read of the reported identity reconciles that without resending.
		if existing, ok := reconcileCreatedBot(ctx, client, result); ok {
			sendNotice(send, "The Bot was already created; opening it.", SlashNoticeFeedback)
			send(botFlowResultMsg{created: true, bot: existing})
			return
		}
		sendNotice(send, "Could not create Bot: "+botCommandDetail(result, err)+"."+botCreateFailedSuffix, SlashNoticeHint)
	}
}

// reconcileCreatedBot observes whether a creation actually took effect. It only
// reads the identity reported by the result and never resends the mutation.
func reconcileCreatedBot(ctx context.Context, client appserver.BotClient, result appserver.CommandResult) (bot.Bot, bool) {
	id := botResourceID(result)
	if id == "" {
		return bot.Bot{}, false
	}
	existing, err := client.GetBot(ctx, id)
	if err != nil {
		return bot.Bot{}, false
	}
	return existing, true
}

// botCommandStatusOf classifies a Bot mutation. Only OutcomeCommitted is
// success; Accepted is not terminal, so it is treated as unproven.
func botCommandStatusOf(result appserver.CommandResult, err error) botCommandStatus {
	switch result.Outcome {
	case appserver.OutcomeCommitted:
		return botCommandCommitted
	case appserver.OutcomeConflicted:
		return botCommandConflict
	case appserver.OutcomeUnknown, appserver.OutcomeAccepted:
		return botCommandUnknown
	case appserver.OutcomeRejected:
		return botCommandFailed
	}
	var outcomeErr *appserver.OutcomeError
	if errors.As(err, &outcomeErr) {
		switch outcomeErr.Outcome {
		case appserver.OutcomeConflicted:
			return botCommandConflict
		case appserver.OutcomeUnknown, appserver.OutcomeAccepted:
			return botCommandUnknown
		}
	}
	return botCommandFailed
}

// botCommandDetail prefers Control's reported detail over a transport error so
// the user sees the recovery-relevant message.
func botCommandDetail(result appserver.CommandResult, err error) string {
	if detail := strings.TrimSpace(result.Detail); detail != "" {
		return detail
	}
	if err != nil {
		return strings.TrimSpace(err.Error())
	}
	if outcome := strings.TrimSpace(string(result.Outcome)); outcome != "" {
		return outcome
	}
	return "unknown error"
}

func runBotSettingsFlow(ctx context.Context, client appserver.BotClient, current bot.Bot, config bot.Config, send func(tea.Msg)) {
	revision := current.Revision
	result, err := client.UpdateBot(ctx, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "bot-update-" + uuid.NewString(),
			SessionID:        current.SessionID,
			ExpectedRevision: &revision,
		},
		BotID:  current.ID,
		Config: config,
	})
	switch botCommandStatusOf(result, err) {
	case botCommandCommitted:
		refreshed, getErr := client.GetBot(ctx, current.ID)
		if getErr != nil {
			sendNotice(send, "Settings saved, but the Bot could not be reloaded: "+getErr.Error(), SlashNoticeHint)
			return
		}
		send(botFlowResultMsg{bot: refreshed})
	case botCommandConflict:
		// The published configuration and any local draft stay untouched.
		sendNotice(send, botUpdateConflictNotice, SlashNoticeHint)
	case botCommandUnknown:
		sendNotice(send, botUpdateUnknownNotice, SlashNoticeHint)
	default:
		sendNotice(send, "Could not save Bot settings: "+botCommandDetail(result, err), SlashNoticeHint)
	}
}

func (m *Model) handleBotFlowResult(msg botFlowResultMsg) tea.Cmd {
	if m == nil || m.bot == nil {
		return nil
	}
	if msg.created {
		return m.selectBot(msg.bot)
	}
	if !m.applyBotConfiguration(msg.bot) {
		// A completed save may belong to a Bot that is no longer selected.
		m.bot.bots = upsertBot(m.bot.bots, msg.bot)
		return nil
	}
	notice := "Bot settings saved"
	return m.showHint(notice, botHintOptions())
}

func upsertBot(bots []bot.Bot, value bot.Bot) []bot.Bot {
	for i, existing := range bots {
		if existing.ID == value.ID {
			if value.Revision >= existing.Revision {
				bots[i] = value
			}
			return bots
		}
	}
	return append(bots, value)
}

func botResourceID(result appserver.CommandResult) string {
	if result.Resource == nil {
		return ""
	}
	if kind := strings.TrimSpace(result.Resource.Kind); kind != "" && kind != appserver.CommandResourceBot {
		return ""
	}
	return strings.TrimSpace(result.Resource.Ref)
}

func displayOrNone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "none"
	}
	return value
}
