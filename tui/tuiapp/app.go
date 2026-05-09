package tuiapp

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func requestBackgroundColorCmd() tea.Cmd {
	return tea.RequestBackgroundColor
}

func NewModel(cfg Config) *Model {
	theme := tuikit.ResolveThemeFromOptions(cfg.NoColor, cfg.ColorProfile)
	themeAuto := tuikit.ThemeUsesAutoBackground()

	delegate := list.NewDefaultDelegate()
	palette := list.New(nil, delegate, 20, 10)
	palette.SetShowHelp(false)
	palette.SetShowStatusBar(false)
	palette.SetFilteringEnabled(true)
	palette.Styles.Title = theme.TitleStyle()
	palette.Styles.PaginationStyle = theme.HelpHintTextStyle()
	palette.Styles.HelpStyle = theme.HelpHintTextStyle()

	ta := textarea.New()
	ta.Placeholder = "Type your message, @agent, #path/to/file, or $skill"
	ta.Prompt = "> "
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "> "
		}
		return "  "
	})
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.MaxHeight = maxInputBarRows
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(false)
	taStyles := ta.Styles()
	taStyles.Focused.CursorLine = lipgloss.NewStyle()
	taStyles.Focused.Base = lipgloss.NewStyle()
	taStyles.Focused.Prompt = theme.PromptStyle()
	taStyles.Focused.Text = theme.TextStyle()
	taStyles.Focused.Placeholder = theme.HelpHintTextStyle()
	taStyles.Blurred.CursorLine = lipgloss.NewStyle()
	taStyles.Blurred.Base = lipgloss.NewStyle()
	taStyles.Blurred.Prompt = theme.PromptStyle()
	taStyles.Blurred.Text = theme.TextStyle()
	taStyles.Blurred.Placeholder = theme.HelpHintTextStyle()
	taStyles.Cursor.Color = theme.CursorFg
	taStyles.Cursor.Shape = tea.CursorBlock
	taStyles.Cursor.Blink = true
	ta.SetStyles(taStyles)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Spinner{
		Frames: runningSpinnerFrames,
		FPS:    60 * time.Millisecond,
	}
	sp.Style = theme.SpinnerStyle()

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 5
	vp.SetHorizontalStep(0)
	vp.KeyMap.Up.SetEnabled(false)
	vp.KeyMap.Down.SetEnabled(false)
	vp.KeyMap.HalfPageUp = key.NewBinding(key.WithKeys("shift+pgup"))
	vp.KeyMap.HalfPageDown = key.NewBinding(key.WithKeys("shift+pgdown"))
	vp.KeyMap.Left.SetEnabled(false)
	vp.KeyMap.Right.SetEnabled(false)
	vp.KeyMap.PageDown = key.NewBinding(key.WithKeys("pgdown"))
	vp.KeyMap.PageUp = key.NewBinding(key.WithKeys("pgup"))

	m := &Model{
		cfg:          cfg,
		theme:        theme,
		themeAuto:    themeAuto,
		noColor:      cfg.NoColor,
		noAnimation:  cfg.NoAnimation,
		colorProfile: theme.Profile,
		keys:         defaultKeyMap(isWSL()),
		spinner:      sp,
		viewport:     vp,
		doc:          NewDocument(),
		Composer: Composer{
			textarea:     ta,
			historyIndex: -1,
		},
		OverlayState: OverlayState{
			palette: palette,
		},
		selectionStart:      textSelectionPoint{line: -1, col: -1},
		selectionEnd:        textSelectionPoint{line: -1, col: -1},
		inputSelectionStart: textSelectionPoint{line: -1, col: -1},
		inputSelectionEnd:   textSelectionPoint{line: -1, col: -1},
		fixedSelectionArea:  fixedSelectionNone,
		fixedSelectionStart: textSelectionPoint{line: -1, col: -1},
		fixedSelectionEnd:   textSelectionPoint{line: -1, col: -1},
		inputLatencyWindow:  make([]time.Duration, 0, 128),
		diag:                newDiagnostics(),
		focused:             true,
		welcomeCardPending:  cfg.ShowWelcomeCard,
	}
	m.help = help.New()
	m.applyTheme(theme)
	if cfg.ToggleMode == nil {
		m.keys.Mode.SetEnabled(false)
	}

	if cfg.RefreshStatus != nil {
		m.observeDriverStatusCall()
		modelText, contextText := cfg.RefreshStatus()
		m.statusModel = normalizeStatusModel(modelText)
		m.statusContext = strings.TrimSpace(contextText)
	}
	if cfg.RefreshStatusView != nil {
		m.statusView = cfg.RefreshStatusView()
	}
	m.refreshModeLabelFromConfig()
	if cfg.RefreshWorkspace != nil {
		if workspace := strings.TrimSpace(cfg.RefreshWorkspace()); workspace != "" {
			m.cfg.Workspace = workspace
		}
	}
	m.setCommands(cfg.Commands)
	m.syncTextareaChrome()
	return m
}

func (m *Model) setCommands(commands []string) {
	if m == nil {
		return
	}
	m.cfg.Commands = append([]string(nil), commands...)
	items := make([]list.Item, 0, len(m.cfg.Commands))
	for _, one := range m.cfg.Commands {
		name := strings.TrimSpace(one)
		if name == "" {
			continue
		}
		items = append(items, commandItem{name: name})
	}
	if m.palette.Title == "" {
		m.palette.Title = "Commands"
	}
	m.palette.SetItems(items)
	m.refreshSlashCommands()
}

func (m *Model) Init() tea.Cmd {
	for _, line := range m.cfg.InitialLogs {
		if strings.TrimSpace(line) == "" {
			continue
		}
		style := tuikit.DetectLineStyle(line)
		m.doc.Append(NewTranscriptBlock(line, style))
	}
	m.hasCommittedLine = m.doc.Len() > 0
	m.syncViewportContent()
	cmds := []tea.Cmd{tickStatusCmd(), m.spinner.Tick}
	if m.themeAuto {
		cmds = append(cmds, requestBackgroundColorCmd())
	}
	return tea.Batch(cmds...)
}

func (m *Model) appendWelcomeCard() {
	m.doc.Append(NewWelcomeBlock(m.cfg.Version, m.cfg.Workspace, m.currentWelcomeModelName()))
	m.hasCommittedLine = true
	m.lastCommittedStyle = tuikit.LineStyleDefault
}

func (m *Model) currentWelcomeModelName() string {
	modelName := strings.TrimSpace(m.statusModel)
	if modelName == "" {
		modelName = strings.TrimSpace(m.cfg.ModelAlias)
	}
	if modelName == "" {
		modelName = "not configured (/connect)"
	}
	return modelName
}

func (m *Model) syncWelcomeCardBlock() bool {
	if m == nil || m.doc == nil {
		return false
	}
	blocks := m.doc.FindByKind(BlockWelcome)
	if len(blocks) == 0 {
		return false
	}
	welcome, ok := blocks[0].(*WelcomeBlock)
	if !ok {
		return false
	}
	workspace := strings.TrimSpace(m.cfg.Workspace)
	modelName := m.currentWelcomeModelName()
	if welcome.Workspace == workspace && welcome.ModelName == modelName {
		return false
	}
	welcome.Workspace = workspace
	welcome.ModelName = modelName
	m.markViewportBlockDirty(welcome.BlockID())
	return true
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handledModel, handledCmd, handled := m.dispatchRenderEvent(msg); handled {
		return handledModel, handledCmd
	}
	m.flushPendingDeferredBatches()

	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.syncTextareaChrome()
		m.help.SetWidth(maxInt(20, m.fixedRowWidth()/2))
		paletteWidth := minInt(maxInt(30, m.fixedRowWidth()-4), maxInt(30, m.width-12))
		m.palette.SetSize(paletteWidth, maxInt(8, minInt(16, m.height-10)))

		vpHeight, _ := m.computeLayout()
		m.viewport.SetWidth(m.viewportContentWidth())
		m.viewport.SetHeight(vpHeight)
		m.syncPaletteAnimationTarget()
		if m.welcomeCardPending {
			m.appendWelcomeCard()
			m.welcomeCardPending = false
		}
		// In the document model, blocks re-render from their data on each
		// syncViewportContent call, so no explicit rerender needed.
		m.syncViewportContent()

		if !m.ready {
			m.ready = true
			m.viewport.GotoBottom()
		}
		return m, m.requestBackgroundColorIfAutoCmd()

	case tea.BackgroundColorMsg:
		if !m.themeAuto {
			return m, nil
		}
		nextTheme := tuikit.ResolveThemeWithBackgroundColor(typed.Color, m.noColor, m.colorProfile)
		m.applyTheme(nextTheme)
		return m, nil

	case tea.ColorProfileMsg:
		if m.noColor {
			return m, nil
		}
		if typed.Profile == colorprofile.Unknown || typed.Profile == m.colorProfile {
			return m, nil
		}
		m.colorProfile = typed.Profile
		nextTheme := tuikit.ResolveThemeWithState(m.theme.IsDark, m.noColor, m.colorProfile)
		m.applyTheme(nextTheme)
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(typed)

	case tea.FocusMsg:
		m.focused = true
		return m, m.requestBackgroundColorIfAutoCmd()

	case tea.BlurMsg:
		m.focused = false
		return m, nil

	case clearHintMsg:
		m.removeHintByID(typed.id)
		return m, nil

	case clipboardCopyResultMsg:
		return m, m.handleClipboardCopyResult(typed)

	case ctrlCExpireMsg:
		if m.ctrlCArmSeq == typed.seq && m.lastCtrlCAt.Equal(typed.armedAt) {
			m.ctrlCArmed = false
			m.lastCtrlCAt = time.Time{}
			m.removeHintsByText("press Ctrl+C again to quit")
		}
		return m, nil

	case paletteAnimationMsg:
		if !m.paletteAnimating {
			return m, nil
		}
		if m.noAnimation {
			m.paletteAnimLines = m.paletteAnimationTarget()
			m.paletteAnimating = false
			return m, nil
		}
		target := m.paletteAnimationTarget()
		switch {
		case m.paletteAnimLines < target:
			m.paletteAnimLines += paletteAnimationStep
			if m.paletteAnimLines > target {
				m.paletteAnimLines = target
			}
		case m.paletteAnimLines > target:
			m.paletteAnimLines -= paletteAnimationStep
			if m.paletteAnimLines < target {
				m.paletteAnimLines = target
			}
		}
		if m.paletteAnimLines == target {
			m.paletteAnimating = false
			return m, nil
		}
		return m, m.paletteAnimationCmd()

	case spinner.TickMsg:
		m.spinnerTickScheduled = false
		if m.running {
			if m.shouldThrottleRunningAnimation() {
				return m, m.scheduleSpinnerTick()
			}
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if cmd != nil {
				m.spinnerTickScheduled = true
			}
			if m.activePrompt == nil {
				m.advanceRunningAnimation()
			}
			return m, cmd
		}
		return m, nil

	case tea.PasteMsg:
		now := time.Now()
		m.diag.LastInputAt = now
		m.pendingInputAt = now
		return m.handlePaste(typed)

	case tea.KeyMsg:
		now := time.Now()
		m.diag.LastInputAt = now
		m.pendingInputAt = now
		return m.handleKey(typed)
	}
	return m, nil
}

func (m *Model) applyTheme(theme tuikit.Theme) {
	if m == nil {
		return
	}
	m.theme = theme
	m.themeCacheKey = themeRenderCacheKey(theme)
	m.runningTickerStyles = nil
	m.runningTickerThemeKey = ""
	clearGlamourCache()
	configureHelpStyles(&m.help, theme)
	m.applyPaletteTheme(theme)
	m.applyTextareaStyles(theme)
	m.spinner.Style = theme.SpinnerStyle()
	m.rethemeHistory()
	m.syncTextareaChrome()
	m.syncViewportContent()
}

func (m *Model) requestBackgroundColorIfAutoCmd() tea.Cmd {
	if m == nil || !m.themeAuto {
		return nil
	}
	return requestBackgroundColorCmd()
}

func (m *Model) applyPaletteTheme(theme tuikit.Theme) {
	styles := m.palette.Styles
	styles.Title = theme.TitleStyle()
	styles.PaginationStyle = theme.HelpHintTextStyle()
	styles.HelpStyle = theme.HelpHintTextStyle()
	m.palette.Styles = styles
}

func (m *Model) applyTextareaStyles(theme tuikit.Theme) {
	styles := m.textarea.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Base = lipgloss.NewStyle()
	styles.Focused.Prompt = theme.PromptStyle()
	styles.Focused.Text = theme.TextStyle()
	styles.Focused.Placeholder = theme.HelpHintTextStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	styles.Blurred.Base = lipgloss.NewStyle()
	styles.Blurred.Prompt = theme.PromptStyle()
	styles.Blurred.Text = theme.TextStyle()
	styles.Blurred.Placeholder = theme.HelpHintTextStyle()
	styles.Cursor.Color = theme.CursorFg
	styles.Cursor.Shape = tea.CursorBlock
	styles.Cursor.Blink = true
	m.textarea.SetStyles(styles)
}

func (m *Model) rethemeHistory() {
	if m == nil {
		return
	}
	m.refreshHistoryTailState()
}

func (m *Model) syncInputFromTextarea() {
	m.input = []rune(m.textarea.Value())
	m.cursor = m.textareaCursorIndex()
	m.adjustTextareaHeight()
}

func (m *Model) syncTextareaFromInput() {
	before := m.textarea.Value()
	after := string(m.input)
	m.textarea.SetValue(after)
	m.inputAttachments = adjustAttachmentOffsetsForTextEdit(m.inputAttachments, before, after)
	m.syncAttachmentSummary()
	m.moveTextareaCursorToIndex(m.cursor)
	m.adjustTextareaHeight()
}

func (m *Model) viewportScrollbarWidth() int {
	if m.width < 48 {
		return 0
	}
	return 1
}

func (m *Model) viewportContentWidth() int {
	return m.readableContentWidth()
}

func (m *Model) readableContentWidth() int {
	return maxInt(1, m.width-tuikit.GutterNarrative-m.viewportScrollbarWidth())
}

func (m *Model) mainColumnWidth() int {
	if m.width > 0 {
		return m.width
	}
	return maxInt(1, m.readableContentWidth()+tuikit.GutterNarrative+m.viewportScrollbarWidth())
}

func (m *Model) mainColumnX() int {
	return 0
}

func (m *Model) placeInMainColumn(block string) string {
	if block == "" {
		return ""
	}
	return indentBlock(block, m.mainColumnX())
}

func (m *Model) fixedRowWidth() int {
	return maxInt(20, m.mainColumnWidth())
}

func (m *Model) fixedRowContentWidth() int {
	return maxInt(1, m.fixedRowWidth()-(tuikit.StatusInset*2))
}

func (m *Model) paletteAnimationTarget() int {
	if !m.showPalette {
		return 0
	}
	return m.fullPaletteLineCount()
}

func (m *Model) syncPaletteAnimationTarget() {
	target := m.paletteAnimationTarget()
	if m.paletteAnimating {
		return
	}
	m.paletteAnimLines = target
}
