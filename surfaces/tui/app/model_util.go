package tuiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

func newDiagnostics() Diagnostics {
	return Diagnostics{
		RedrawMode:                 "fullscreen",
		UpdateMessagesByLane:       make(map[renderEventLane]uint64),
		UpdateMessagesByType:       make(map[string]uint64),
		ViewportSetContentReason:   make(map[string]uint64),
		BlockRenderCallsByKind:     make(map[BlockKind]uint64),
		StreamSmoothingFlushReason: make(map[string]uint64),
	}
}

func (m *Model) ensureDiagnosticsMaps() {
	if m == nil {
		return
	}
	if m.diag.UpdateMessagesByLane == nil {
		m.diag.UpdateMessagesByLane = make(map[renderEventLane]uint64)
	}
	if m.diag.UpdateMessagesByType == nil {
		m.diag.UpdateMessagesByType = make(map[string]uint64)
	}
	if m.diag.ViewportSetContentReason == nil {
		m.diag.ViewportSetContentReason = make(map[string]uint64)
	}
	if m.diag.BlockRenderCallsByKind == nil {
		m.diag.BlockRenderCallsByKind = make(map[BlockKind]uint64)
	}
	if m.diag.StreamSmoothingFlushReason == nil {
		m.diag.StreamSmoothingFlushReason = make(map[string]uint64)
	}
}

func (m *Model) observeRenderMessage(msg tea.Msg, policy renderEventPolicy) {
	if m == nil {
		return
	}
	m.ensureDiagnosticsMaps()
	m.diag.UpdateMessagesByLane[policy.lane]++
	m.diag.UpdateMessagesByType[fmt.Sprintf("%T", msg)]++
}

func (m *Model) observeViewportSetContent(lines []string, reason string) {
	if m == nil {
		return
	}
	m.ensureDiagnosticsMaps()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unknown"
	}
	m.diag.ViewportSetContentLines++
	m.diag.ViewportSetContentLineCount += uint64(len(lines))
	var bytes uint64
	for _, line := range lines {
		bytes += uint64(len(line))
	}
	m.diag.ViewportSetContentBytes += bytes
	m.diag.ViewportSetContentReason[reason]++
}

func (m *Model) observeBlockRender(kind BlockKind) {
	if m == nil {
		return
	}
	m.ensureDiagnosticsMaps()
	m.diag.BlockRenderCallsByKind[kind]++
}

func (m *Model) observeStreamSmoothingFlush(reason string) {
	if m == nil {
		return
	}
	m.ensureDiagnosticsMaps()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unknown"
	}
	m.diag.StreamSmoothingFlushReason[reason]++
}

func (m *Model) observeGlamourRender() {
	if m == nil {
		return
	}
	m.diag.GlamourRenderCalls++
}

func (m *Model) observeInlineMarkdownRender() {
	if m == nil {
		return
	}
	m.diag.InlineMarkdownCalls++
}

func (m *Model) observeControlStatusCall() {
	if m == nil {
		return
	}
	m.diag.ControlStatusCalls++
}

func (m *Model) observeRender(duration time.Duration, bytes int, redrawMode string) {
	if m.cfg.ProgramSender != nil {
		m.diag.ProgramSendsAfterClose = m.cfg.ProgramSender.DroppedAfterClose()
	}
	m.diag.Frames++
	m.diag.LastFrameDuration = duration
	if strings.TrimSpace(redrawMode) == "" {
		redrawMode = "incremental"
	}
	m.diag.RedrawMode = redrawMode
	if redrawMode == "fullscreen" || redrawMode == "full" {
		m.diag.FullRepaints++
	} else {
		m.diag.IncrementalFrames++
	}
	if duration >= 40*time.Millisecond {
		m.diag.SlowFrames++
	}
	if duration > m.diag.MaxFrameDuration {
		m.diag.MaxFrameDuration = duration
	}
	if m.diag.Frames == 1 {
		m.diag.AvgFrameDuration = duration
	} else {
		total := time.Duration(int64(m.diag.AvgFrameDuration)*(int64(m.diag.Frames)-1) + int64(duration))
		m.diag.AvgFrameDuration = total / time.Duration(m.diag.Frames)
	}
	if bytes > 0 {
		m.diag.RenderBytes += uint64(bytes)
		if uint64(bytes) > m.diag.PeakFrameBytes {
			m.diag.PeakFrameBytes = uint64(bytes)
		}
	}
	m.observeInputLatency()
	m.diag.LastRenderAt = time.Now()
	if m.cfg.OnDiagnostics != nil {
		m.cfg.OnDiagnostics(m.diag)
	}
	m.writeDiagnosticsDebugFile()
}

func (m *Model) writeDiagnosticsDebugFile() {
	if m == nil {
		return
	}
	path := strings.TrimSpace(m.cfg.DiagnosticsDebugFile)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("CAELIS_TUI_RENDER_DEBUG_FILE"))
	}
	if path == "" {
		return
	}
	now := time.Now()
	if !m.lastDiagnosticsDebugWrite.IsZero() && now.Sub(m.lastDiagnosticsDebugWrite) < time.Second {
		return
	}
	payload, err := json.MarshalIndent(m.diag, "", "  ")
	if err != nil {
		m.diag.DiagnosticsDebugWriteErrors++
		return
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		m.diag.DiagnosticsDebugWriteErrors++
		return
	}
	m.lastDiagnosticsDebugWrite = now
}

func (m *Model) observeInputLatency() {
	if m.pendingInputAt.IsZero() {
		return
	}
	latency := time.Since(m.pendingInputAt)
	m.pendingInputAt = time.Time{}
	m.diag.LastInputLatency = latency
	m.inputLatencyCount++
	if m.diag.AvgInputLatency == 0 || m.inputLatencyCount <= 1 {
		m.diag.AvgInputLatency = latency
	} else {
		total := time.Duration(int64(m.diag.AvgInputLatency)*(int64(m.inputLatencyCount)-1) + int64(latency))
		m.diag.AvgInputLatency = total / time.Duration(m.inputLatencyCount)
	}
	const window = 128
	if len(m.inputLatencyWindow) >= window {
		copy(m.inputLatencyWindow, m.inputLatencyWindow[1:])
		m.inputLatencyWindow = m.inputLatencyWindow[:window-1]
	}
	m.inputLatencyWindow = append(m.inputLatencyWindow, latency)
	m.diag.P95InputLatency = percentileDuration(m.inputLatencyWindow, 0.95)
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

func normalizeStatusModel(model string) string {
	if model = strings.TrimSpace(model); model != "" {
		return model
	}
	return "not configured (/connect)"
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func (m *Model) cachedThemeRenderKey() string {
	if m == nil {
		return ""
	}
	if m.themeCacheKey == "" {
		m.themeCacheKey = themeRenderCacheKey(m.theme)
	}
	return m.themeCacheKey
}

func (m *Model) blockRenderContext(width int) BlockRenderContext {
	if width <= 0 {
		width = 1
	}
	return BlockRenderContext{
		Width:                 width,
		Height:                m.viewport.Height(),
		TermWidth:             m.width,
		Theme:                 m.theme,
		ThemeKey:              m.cachedThemeRenderKey(),
		Workspace:             m.renderWorkspacePath(),
		SpinnerView:           m.spinner.View(),
		ObserveGlamourRender:  m.observeGlamourRender,
		ObserveInlineMarkdown: m.observeInlineMarkdownRender,
	}
}

func (m *Model) renderWorkspacePath() string {
	if m == nil {
		return ""
	}
	if workspace := strings.TrimSpace(m.statusView.Workspace); workspace != "" {
		if path, _, _, ok := parseWorkspaceStatusDisplay(workspace); ok {
			return path
		}
		return workspace
	}
	return strings.TrimSpace(m.cfg.Workspace)
}

func (m *Model) setWorkspaceDisplay(workspace string) (string, bool) {
	workspace = strings.TrimSpace(workspace)
	if m == nil || workspace == "" {
		return workspace, false
	}
	previous := firstNonEmpty(strings.TrimSpace(m.stableWorkspaceDisplay), strings.TrimSpace(m.cfg.Workspace))
	next := stabilizeWorkspaceDisplay(previous, workspace)
	if next == "" {
		return "", false
	}
	changed := next != strings.TrimSpace(m.cfg.Workspace)
	m.cfg.Workspace = next
	m.stableWorkspaceDisplay = next
	return next, changed
}

func (m *Model) normalizeStatusViewWorkspace() bool {
	if m == nil {
		return false
	}
	workspace := strings.TrimSpace(m.statusView.Workspace)
	if workspace == "" {
		return false
	}
	next, cfgChanged := m.setWorkspaceDisplay(workspace)
	viewChanged := next != workspace
	m.statusView.Workspace = next
	return cfgChanged || viewChanged
}

func stabilizeWorkspaceDisplay(previous string, next string) string {
	previous = strings.TrimSpace(previous)
	next = strings.TrimSpace(next)
	if next == "" {
		return ""
	}
	if _, branch, _, ok := parseWorkspaceStatusDisplay(next); ok && strings.TrimSpace(branch) != "" {
		return next
	}
	if prevPath, _, _, ok := parseWorkspaceStatusDisplay(previous); ok && sameWorkspaceDisplayPath(prevPath, next) {
		return previous
	}
	return next
}

func sameWorkspaceDisplayPath(left string, right string) bool {
	leftKey := workspacePathCompareKey(left)
	rightKey := workspacePathCompareKey(right)
	return leftKey != "" && rightKey != "" && leftKey == rightKey
}

func workspacePathCompareKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			home = strings.TrimRight(strings.TrimSpace(home), `/\`)
			switch {
			case path == "~":
				path = home
			case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
				path = home + path[1:]
			}
		}
	}
	windowsPath := looksWindowsPath(path)
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	if windowsPath {
		path = strings.ToLower(path)
	}
	return path
}

func looksWindowsPath(path string) bool {
	if strings.Contains(path, `\`) {
		return true
	}
	return len(path) >= 2 && path[1] == ':' &&
		((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z'))
}

func (m *Model) renderInlineMarkdown(text string, base lipgloss.Style) string {
	m.observeInlineMarkdownRender()
	return renderInlineMarkdown(text, base, m.theme)
}

func (ctx BlockRenderContext) renderThemeKey() string {
	if key := strings.TrimSpace(ctx.ThemeKey); key != "" {
		return key
	}
	return themeRenderCacheKey(ctx.Theme)
}

func (ctx BlockRenderContext) observeGlamourRender() {
	if ctx.ObserveGlamourRender != nil {
		ctx.ObserveGlamourRender()
	}
}

func (ctx BlockRenderContext) observeInlineMarkdownRender() {
	if ctx.ObserveInlineMarkdown != nil {
		ctx.ObserveInlineMarkdown()
	}
}

func (m *Model) refreshModeLabelFromConfig() bool {
	if m == nil || m.cfg.ModeLabel == nil {
		return false
	}
	next := strings.TrimSpace(m.cfg.ModeLabel())
	if next == m.statusModeLabel {
		return false
	}
	m.statusModeLabel = next
	return true
}

func mentionQueryAtCursor(input []rune, cursor int) (int, int, string, bool) {
	start, end, query, _, ok := mentionQueryAtCursorWithPrefix(input, cursor)
	return start, end, query, ok
}

func mentionQueryAtCursorWithPrefix(input []rune, cursor int) (int, int, string, string, bool) {
	if len(input) == 0 {
		return 0, 0, "", "", false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 && isMentionQueryRune(input[start-1]) {
		start--
	}
	if start == 0 || (input[start-1] != '@' && input[start-1] != '#') {
		return 0, 0, "", "", false
	}
	at := start - 1
	prefix := string(input[at])
	if at > 0 {
		prev := input[at-1]
		if prev != ' ' && prev != '\t' && prev != '(' && prev != '[' && prev != '{' && prev != ',' && prev != ';' && prev != ':' && prev != '"' && prev != '\'' {
			return 0, 0, "", "", false
		}
	}
	end := cursor
	for end < len(input) && isMentionQueryRune(input[end]) {
		end++
	}
	return at, end, string(input[start:end]), prefix, true
}

func resumeQueryAtCursor(input []rune, cursor int) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	text := strings.TrimSpace(string(input[:cursor]))
	if text == "" {
		return "", false
	}
	if text == "/resume" {
		return "", true
	}
	if !strings.HasPrefix(text, "/resume ") {
		return "", false
	}
	query := strings.TrimSpace(strings.TrimPrefix(text, "/resume "))
	return query, true
}

func slashArgQueryAtCursor(input []rune, cursor int) (string, string, bool) {
	if len(input) == 0 {
		return "", "", false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	raw := string(input[:cursor])
	text := strings.TrimSpace(raw)
	if text == "" || !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", false
	}
	command := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fields[0])), "/")
	hasTrailingDelimiter := false
	if len(raw) > 0 {
		last := raw[len(raw)-1]
		hasTrailingDelimiter = last == ' ' || last == '\t'
	}
	switch command {
	case "model":
		if len(fields) == 1 {
			if !hasTrailingDelimiter {
				return "", "", false
			}
			return command, "", true
		}
		action := strings.ToLower(strings.TrimSpace(fields[1]))
		if len(fields) == 2 {
			if hasTrailingDelimiter {
				switch action {
				case "use":
					return "model " + action, "", true
				case "del":
					return "model " + action, "", true
				default:
					return "", "", false
				}
			}
			if action == "" {
				return "", "", false
			}
			switch action {
			case "use", "del":
			default:
				return "model", action, true
			}
			return "model", action, true
		}
		switch action {
		case "use", "del":
		default:
			return "", "", false
		}
		if action == "del" {
			return "model " + action, strings.TrimSpace(fields[2]), true
		}
		alias := strings.TrimSpace(fields[2])
		if alias == "" {
			return "", "", false
		}
		if len(fields) == 3 {
			if hasTrailingDelimiter {
				return "model use " + alias, "", true
			}
			return "model use", alias, true
		}
		return "model use " + alias, strings.TrimSpace(strings.Join(fields[3:], " ")), true
	case "agent":
		if len(fields) == 1 {
			if !hasTrailingDelimiter {
				return "", "", false
			}
			return command, "", true
		}
		action := strings.ToLower(strings.TrimSpace(fields[1]))
		if len(fields) == 2 {
			if hasTrailingDelimiter {
				switch action {
				case "add", "install", "remove", "rm", "use":
					return "agent " + action, "", true
				case "list":
					return "", "", false
				default:
					return "", "", false
				}
			}
			if action == "" {
				return "", "", false
			}
			switch action {
			case "list", "add", "rm", "use":
			default:
				return "agent", action, true
			}
			return "agent", action, true
		}
		switch action {
		case "add", "install", "remove", "rm", "use":
		default:
			return "", "", false
		}
		if len(fields) == 3 {
			if hasTrailingDelimiter {
				if action == "add" && strings.EqualFold(strings.TrimSpace(fields[2]), "--install") {
					return "agent add --install", "", true
				}
				return "", "", false
			}
			return "agent " + action, strings.TrimSpace(fields[2]), true
		}
		if action == "add" && len(fields) == 4 && strings.EqualFold(strings.TrimSpace(fields[2]), "--install") {
			return "agent add --install", strings.TrimSpace(fields[3]), true
		}
		return "", "", false
	case "plugin":
		return slashRootActionQuery(command, fields, hasTrailingDelimiter, []string{"install", "manage", "rm"}, []string{"install", "rm"})
	case "subagent":
		if len(fields) == 1 {
			if !hasTrailingDelimiter {
				return "", "", false
			}
			return command, "", true
		}
		action := strings.ToLower(strings.TrimSpace(fields[1]))
		if len(fields) == 2 {
			if hasTrailingDelimiter {
				switch action {
				case "run", "bind":
					return "subagent " + action, "", true
				case "list":
					return "", "", false
				default:
					return "", "", false
				}
			}
			if action == "" {
				return "", "", false
			}
			switch action {
			case "list", "run", "bind":
			default:
				return "subagent", action, true
			}
			return "subagent", action, true
		}
		switch action {
		case "run":
			if len(fields) == 3 && !hasTrailingDelimiter {
				return "subagent run", strings.TrimSpace(fields[2]), true
			}
			return "", "", false
		case "bind":
		default:
			return "", "", false
		}
		profileID := strings.TrimSpace(fields[2])
		if profileID == "" {
			return "", "", false
		}
		if len(fields) == 3 {
			if hasTrailingDelimiter {
				return "subagent bind " + profileID, "", true
			}
			return "subagent bind", profileID, true
		}
		target := strings.ToLower(strings.TrimSpace(fields[3]))
		if len(fields) == 4 {
			if hasTrailingDelimiter {
				switch target {
				case "model", "acp":
					return "subagent bind " + profileID + " " + target, "", true
				default:
					return "", "", false
				}
			}
			return "subagent bind " + profileID, target, true
		}
		switch target {
		case "model":
			modelAlias := strings.TrimSpace(fields[4])
			if modelAlias == "" {
				return "", "", false
			}
			if len(fields) == 5 {
				if hasTrailingDelimiter {
					return "subagent bind " + profileID + " model " + modelAlias, "", true
				}
				return "subagent bind " + profileID + " model", modelAlias, true
			}
			return "subagent bind " + profileID + " model " + modelAlias, strings.TrimSpace(strings.Join(fields[5:], " ")), true
		case "acp":
			if len(fields) == 5 && !hasTrailingDelimiter {
				return "subagent bind " + profileID + " acp", strings.TrimSpace(fields[4]), true
			}
			return "", "", false
		default:
			return "", "", false
		}
	case "sandbox":
		if len(fields) == 1 {
			if !hasTrailingDelimiter {
				return "", "", false
			}
			return command, "", true
		}
		if len(fields) == 2 {
			return command, strings.TrimSpace(fields[1]), true
		}
		return "", "", false
	default:
		return "", "", false
	}
}

func slashRootActionQuery(command string, fields []string, hasTrailingDelimiter bool, rootActions []string, valueActions []string) (string, string, bool) {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || len(fields) == 0 {
		return "", "", false
	}
	if len(fields) == 1 {
		return command, "", true
	}
	action := strings.ToLower(strings.TrimSpace(fields[1]))
	if len(fields) == 2 {
		if hasTrailingDelimiter {
			if slashArgStringSliceContains(valueActions, action) {
				return command + " " + action, "", true
			}
			return "", "", false
		}
		if action == "" {
			return "", "", false
		}
		if slashArgStringSliceContains(rootActions, action) {
			return command, action, true
		}
		return command, action, true
	}
	if slashArgStringSliceContains(valueActions, action) {
		return command + " " + action, strings.TrimSpace(strings.Join(fields[2:], " ")), true
	}
	return "", "", false
}

func slashArgStringSliceContains(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func slashArgQueryAtEnd(input []rune) (string, string, bool) {
	return slashArgQueryAtCursor(input, len(input))
}

func resumeQueryAtEnd(input []rune) (string, bool) {
	return resumeQueryAtCursor(input, len(input))
}

func slashCommandQueryAtCursor(input []rune, cursor int) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	text := strings.TrimSpace(string(input[:cursor]))
	if text == "" || !strings.HasPrefix(text, "/") {
		return "", false
	}
	if strings.Contains(text, " ") {
		return "", false
	}
	query := strings.TrimPrefix(text, "/")
	return query, true
}

func isMentionQueryRune(r rune) bool {
	if r == '_' || r == '-' || r == '.' || r == '/' || r == '\\' {
		return true
	}
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// skillQueryAtCursor detects a $skill token at cursor position.
// Returns the span [start, end) and the query text after '$'.
func skillQueryAtCursor(input []rune, cursor int) (int, int, string, bool) {
	if len(input) == 0 {
		return 0, 0, "", false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 && isSkillQueryRune(input[start-1]) {
		start--
	}
	if start == 0 || input[start-1] != '$' {
		return 0, 0, "", false
	}
	dollar := start - 1
	if dollar > 0 {
		prev := input[dollar-1]
		if prev != ' ' && prev != '\t' && prev != '(' && prev != '[' && prev != '{' && prev != ',' && prev != ';' && prev != ':' && prev != '"' && prev != '\'' {
			return 0, 0, "", false
		}
	}
	end := cursor
	for end < len(input) && isSkillQueryRune(input[end]) {
		end++
	}
	return dollar, end, string(input[start:end]), true
}

func isSkillQueryRune(r rune) bool {
	if r == '_' || r == '-' {
		return true
	}
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func replaceRuneSpan(input []rune, start int, end int, replacement string) ([]rune, int) {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(input) {
		end = len(input)
	}
	out := append([]rune(nil), input[:start]...)
	repl := []rune(replacement)
	out = append(out, repl...)
	out = append(out, input[end:]...)
	return out, start + len(repl)
}

func overlayAboveBottomAreaLeft(base string, overlay string, screenWidth int, startX int, bottomHeight int, gap int) string {
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	if len(baseLines) == 0 || len(overlayLines) == 0 {
		return base
	}
	if startX < 0 {
		startX = 0
	}
	startRow := len(baseLines) - maxInt(0, bottomHeight) - len(overlayLines) - gap
	if startRow < 0 {
		startRow = 0
	}
	for i, line := range overlayLines {
		row := startRow + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = overlayLineAt(baseLines[row], line, startX, screenWidth)
	}
	return strings.Join(baseLines, "\n")
}

func overlayTopRight(base string, overlay string, screenWidth int, top int, rightInset int) string {
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	if len(baseLines) == 0 || len(overlayLines) == 0 || screenWidth <= 0 {
		return base
	}
	if top < 0 {
		top = 0
	}
	if rightInset < 0 {
		rightInset = 0
	}
	overlayWidth := 0
	for _, line := range overlayLines {
		overlayWidth = maxInt(overlayWidth, lipgloss.Width(line))
	}
	if overlayWidth <= 0 {
		return base
	}
	startX := maxInt(0, screenWidth-rightInset-overlayWidth)
	startRow := topRightOverlayRow(baseLines, overlayLines, startX, top)
	for i, line := range overlayLines {
		row := startRow + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = overlayLineAtPreservingPrefix(baseLines[row], line, startX, screenWidth)
	}
	return strings.Join(baseLines, "\n")
}

func topRightOverlayRow(baseLines []string, overlayLines []string, startX int, top int) int {
	if len(baseLines) == 0 {
		return 0
	}
	maxStart := maxInt(0, len(baseLines)-len(overlayLines))
	if top > maxStart {
		top = maxStart
	}
	end := minInt(maxStart, top+8)
	for row := top; row <= end; row++ {
		clear := true
		for i := range overlayLines {
			if rightTrimmedDisplayWidth(baseLines[row+i]) > startX {
				clear = false
				break
			}
		}
		if clear {
			return row
		}
	}
	return top
}

func rightTrimmedDisplayWidth(line string) int {
	return displayColumns(strings.TrimRight(ansi.Strip(line), " \t"))
}

func overlayLineAtPreservingPrefix(baseLine string, overlayLine string, startX int, screenWidth int) string {
	if startX < 0 {
		startX = 0
	}
	overlayWidth := lipgloss.Width(overlayLine)
	prefix := ansi.Truncate(baseLine, startX, "")
	if prefixWidth := lipgloss.Width(prefix); prefixWidth < startX {
		prefix += strings.Repeat(" ", startX-prefixWidth)
	}
	remaining := screenWidth - startX - overlayWidth
	suffix := ""
	if remaining > 0 {
		suffix = strings.Repeat(" ", remaining)
	}
	return prefix + overlayLine + suffix
}

func overlayLineAt(_ string, overlayLine string, startX int, screenWidth int) string {
	if startX < 0 {
		startX = 0
	}
	prefix := strings.Repeat(" ", startX)
	overlayWidth := lipgloss.Width(overlayLine)
	remaining := screenWidth - startX - overlayWidth
	suffix := ""
	if remaining > 0 {
		suffix = strings.Repeat(" ", remaining)
	}
	return prefix + overlayLine + suffix
}

func normalizeFullscreenFrame(view string, width int, height int) string {
	normalized, _ := normalizeFullscreenFrameWithTopTrim(view, width, height)
	return normalized
}

func normalizeFullscreenFrameWithTopTrim(view string, width int, height int) (string, int) {
	if width <= 0 && height <= 0 {
		return view, 0
	}
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	topTrim := 0
	if height > 0 && len(lines) > height {
		// Keep the bottom portion so fixed input/footer rows survive if a
		// transient resize frame overproduces viewport rows.
		topTrim = len(lines) - height
		lines = lines[len(lines)-height:]
	}
	if width > 0 {
		for i, line := range lines {
			if pad := width - displayColumns(line); pad > 0 {
				lines[i] = line + strings.Repeat(" ", pad)
			}
			lines[i] = protectWideCellRepaintLine(lines[i], width)
		}
	}
	if height > 0 && len(lines) < height {
		blank := ""
		if width > 0 {
			blank = strings.Repeat(" ", width)
		}
		for len(lines) < height {
			lines = append(lines, blank)
		}
	}
	return strings.Join(lines, "\n"), topTrim
}

const wideCellRendererSentinelURI = "caelis://wide-cell-render-sentinel"

func protectWideCellRepaintBlock(text string, width int) string {
	if text == "" || width <= 1 {
		return text
	}
	lines := strings.Split(text, "\n")
	changed := false
	for idx, line := range lines {
		next := protectWideCellRepaintLine(line, width)
		if next != line {
			lines[idx] = next
			changed = true
		}
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

func protectWideCellRepaintLine(line string, width int) string {
	if line == "" || width <= 1 || !lineContainsWideCell(line) {
		return line
	}
	lineWidth := displayColumns(line)
	if lineWidth < width {
		return line + strings.Repeat(" ", width-lineWidth-1) + wideCellRendererSentinel()
	}
	if lineWidth == width && strings.HasSuffix(line, " ") {
		return strings.TrimSuffix(line, " ") + wideCellRendererSentinel()
	}
	return line
}

func wideCellRendererSentinel() string {
	return ansi.SetHyperlink(wideCellRendererSentinelURI) + " " + ansi.ResetHyperlink()
}

func lineContainsWideCell(line string) bool {
	plain := ansi.Strip(line)
	for _, cluster := range splitGraphemeClusters(plain) {
		if graphemeWidth(cluster) > 1 {
			return true
		}
	}
	return false
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	if percentile <= 0 {
		percentile = 0
	}
	if percentile >= 1 {
		percentile = 1
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	index := int(float64(len(sorted)-1) * percentile)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func normalizedSelectionRange(start textSelectionPoint, end textSelectionPoint, lineCount int) (textSelectionPoint, textSelectionPoint, bool) {
	if lineCount <= 0 || start.line < 0 || end.line < 0 {
		return textSelectionPoint{}, textSelectionPoint{}, false
	}
	if start.line >= lineCount {
		start.line = lineCount - 1
	}
	if end.line >= lineCount {
		end.line = lineCount - 1
	}
	if start.line > end.line || (start.line == end.line && start.col > end.col) {
		start, end = end, start
	}
	if start.col < 0 {
		start.col = 0
	}
	if end.col < 0 {
		end.col = 0
	}
	return start, end, true
}

func selectionTextFromLines(lines []string, start textSelectionPoint, end textSelectionPoint) string {
	if len(lines) == 0 {
		return ""
	}
	if start.line == end.line && start.col == end.col {
		return ""
	}
	var out []string
	for i := start.line; i <= end.line && i < len(lines); i++ {
		line := lines[i]
		width := displayColumns(line)
		from := 0
		to := width
		if i == start.line {
			from = start.col
		}
		if i == end.line {
			to = end.col
		}
		if from < 0 {
			from = 0
		}
		if to > width {
			to = width
		}
		if to < from {
			to = from
		}
		out = append(out, sliceByDisplayColumns(line, from, to))
	}
	return strings.Join(out, "\n")
}

func renderSelectionOnLines(lines []string, start textSelectionPoint, end textSelectionPoint, highlight lipgloss.Style) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if i < start.line || i > end.line {
			out = append(out, lines[i])
			continue
		}
		line := lines[i]
		width := displayColumns(line)
		from := 0
		to := width
		if i == start.line {
			from = start.col
		}
		if i == end.line {
			to = end.col
		}
		if from < 0 {
			from = 0
		}
		if to > width {
			to = width
		}
		if to < from {
			to = from
		}
		prefix := sliceByDisplayColumns(line, 0, from)
		middle := sliceByDisplayColumns(line, from, to)
		suffix := sliceByDisplayColumns(line, to, width)
		if middle == "" {
			out = append(out, line)
			continue
		}
		out = append(out, prefix+highlight.Render(middle)+suffix)
	}
	return out
}

// renderSelectionOnStyledLines renders selection highlight while preserving
// styled (ANSI-colored) output for non-selected lines. Selected lines show
// plain text with the configured selection style so the selection boundary is
// visually unambiguous.
func renderSelectionOnStyledLines(styledLines, plainLines []string, start textSelectionPoint, end textSelectionPoint, highlight lipgloss.Style) []string {
	if len(styledLines) == 0 {
		return nil
	}
	out := make([]string, 0, len(styledLines))
	for i := 0; i < len(styledLines); i++ {
		if i < start.line || i > end.line {
			// Non-selected: keep styled (colored) output.
			out = append(out, styledLines[i])
			continue
		}
		// Selected line: use plain text with reverse highlight on the
		// selected portion.
		line := plainLines[i]
		width := displayColumns(line)
		from := 0
		to := width
		if i == start.line {
			from = start.col
		}
		if i == end.line {
			to = end.col
		}
		if from < 0 {
			from = 0
		}
		if to > width {
			to = width
		}
		if to < from {
			to = from
		}
		prefix := sliceByDisplayColumns(line, 0, from)
		middle := sliceByDisplayColumns(line, from, to)
		suffix := sliceByDisplayColumns(line, to, width)
		if middle == "" {
			out = append(out, styledLines[i])
			continue
		}
		out = append(out, prefix+highlight.Render(middle)+suffix)
	}
	return out
}

func displayColumns(s string) int {
	return graphemeWidth(s)
}

func sliceByDisplayColumns(s string, start int, end int) string {
	return graphemeSlice(s, start, end)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
