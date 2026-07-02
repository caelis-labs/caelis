package tuiapp

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	controlprompt "github.com/OnslaughtSnail/caelis/ports/controlprompt"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

const maxInputBarRows = 4
const ctrlCExitWindow = 2 * time.Second
const terminalResponseGuardDuration = 1500 * time.Millisecond
const terminalResponsePendingFlushDelay = 120 * time.Millisecond
const runningHintRotateEveryTicks = 90
const runningLightSpeed = 0.55
const runningLightBandRadius = 5.5
const runningLightLead = 4.0
const runningTickerStaticFrameCostThreshold = 40 * time.Millisecond
const copyHintDuration = 1600 * time.Millisecond
const inputHorizontalInset = tuikit.InputInset
const paletteAnimationInterval = 16 * time.Millisecond
const paletteAnimationStep = 3
const systemHintDuration = 1800 * time.Millisecond
const streamSmoothingTickIntervalDefault = 16 * time.Millisecond
const streamSmoothingWarmDelayDefault = 40 * time.Millisecond
const streamSmoothingTargetLagDefault = 160 * time.Millisecond
const streamSmoothingNormalCPSDefault = 68.0
const streamSmoothingCatchupCPSDefault = 128.0
const streamSmoothingNormalMaxPerFrameDefault = 5
const streamSmoothingCatchupMaxPerFrameDefault = 12
const scrollbarVisibleDuration = 900 * time.Millisecond
const offscreenViewportSyncIntervalFloor = 80 * time.Millisecond
const offscreenViewportSyncIntervalMax = 160 * time.Millisecond
const completionRefreshDebounce = 100 * time.Millisecond
const sandboxProgressOverlayWidth = 24
const sandboxProgressOverlayMinWidth = 10
const sandboxProgressOverlayTopInset = 1
const sandboxProgressOverlayRightInset = inputHorizontalInset * 2

type hintEntry struct {
	id             uint64
	text           string
	priority       HintPriority
	clearOnMessage bool
}

type liveTurnState struct {
	Active          bool
	Mode            SubmissionMode
	Divider         bool
	StartedAt       time.Time
	LastDuration    time.Duration
	HasLastDuration bool
}

var runningSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var runningCarouselLines = []string{
	"Working on the turn...",
	"You can add a follow-up while this runs.",
	"Esc interrupts the current turn.",
	"Use #path when a specific file matters.",
	"Reviewing the latest context and tool output.",
}

type runningActivityKind string

const (
	runningActivityApprovalReview runningActivityKind = "approval_review"
	runningActivityInterrupting   runningActivityKind = "interrupting"
)

type runningActivityState struct {
	Kind   runningActivityKind
	Detail string
}

type Diagnostics struct {
	Frames                        uint64
	IncrementalFrames             uint64
	FullRepaints                  uint64
	SlowFrames                    uint64
	LastFrameDuration             time.Duration
	AvgFrameDuration              time.Duration
	MaxFrameDuration              time.Duration
	RenderBytes                   uint64
	PeakFrameBytes                uint64
	ViewportFullSyncs             uint64
	ViewportIncrementalSyncs      uint64
	ViewportQueuedSyncs           uint64
	ViewportSkippedSyncs          uint64
	ViewportSetContentLines       uint64
	ViewportSetContentLineCount   uint64
	ViewportSetContentBytes       uint64
	SelectionVisibleRenders       uint64
	UpdateMessagesByLane          map[renderEventLane]uint64
	UpdateMessagesByType          map[string]uint64
	ViewportSetContentReason      map[string]uint64
	BlockRenderCallsByKind        map[BlockKind]uint64
	StreamSmoothingFlushReason    map[string]uint64
	GlamourRenderCalls            uint64
	InlineMarkdownCalls           uint64
	ControlStatusCalls            uint64
	RunningTickerAnimatedRenders  uint64
	RunningTickerStaticRenders    uint64
	RunningTickerStyleCacheMisses uint64
	DiagnosticsDebugWriteErrors   uint64
	ProgramSendsAfterClose        uint64
	LastRenderAt                  time.Time
	LastInputAt                   time.Time
	LastInputLatency              time.Duration
	AvgInputLatency               time.Duration
	P95InputLatency               time.Duration
	LastMentionLatency            time.Duration
	RedrawMode                    string
}

type viewportFollowState int

const (
	viewportFollowTail viewportFollowState = iota
	viewportPinnedHistory
	viewportSelecting
)

type Config struct {
	Context                context.Context
	AppName                string
	Version                string
	Workspace              string
	ModelAlias             string
	ShowWelcomeCard        bool
	InitialLogs            []string
	Commands               []string
	Wizards                []WizardDef
	ControlService         control.Service
	ProgramSender          *ProgramSender
	PromptRouterFactory    controlprompt.RouterFactory
	OnStart                func()
	ExecuteLine            func(Submission) TaskResultMsg
	executeLineCmd         func(Submission) tea.Msg
	CanSubmitRunningPrompt func() bool
	CancelRunning          func() bool
	ToggleMode             func() (string, error)
	ModeLabel              func() string
	RefreshWorkspace       func() string
	RefreshStatus          func() (string, string)
	RefreshStatusView      func() StatusViewModel
	MentionComplete        func(string, int) ([]CompletionCandidate, error)
	FileComplete           func(string, int) ([]CompletionCandidate, error)
	SkillComplete          func(string, int) ([]CompletionCandidate, error)
	ResumeComplete         func(string, int) ([]ResumeCandidate, error)
	SlashArgComplete       func(command string, query string, limit int) ([]SlashArgCandidate, error)
	ReadClipboardText      func() (string, error)
	WriteClipboardText     func(string) error
	PasteClipboardImage    func() ([]string, string, error)
	ClearAttachments       func() []string
	SetAttachments         func([]string) []string
	OnDiagnostics          func(Diagnostics)
	DiagnosticsDebugFile   string
	StreamTickInterval     time.Duration
	StreamWarmDelay        time.Duration
	StreamNormalCPS        float64
	StreamCatchupCPS       float64
	StreamTargetLag        time.Duration
	StreamThreshold        int
	StreamNormalMaxTick    int
	StreamCatchupMaxTick   int
	NoColor                bool
	NoAnimation            bool
	// RenderFPS is a Bubble Tea renderer cap only. Stream coalescing remains
	// owned by the TUI frame scheduler.
	RenderFPS    int
	ColorProfile colorprofile.Profile
}

type CompletionCandidate struct {
	Value   string
	Display string
	Kind    string
	Detail  string
	Path    string
}

type ResumeCandidate struct {
	SessionID string
	Title     string
	Prompt    string
	Model     string
	Workspace string
	Age       string
	UpdatedAt time.Time
}

type SlashArgCandidate struct {
	Value   string
	Display string
	Detail  string
	NoAuth  bool
}

type commandItem struct {
	name string
}

func (i commandItem) Title() string       { return "/" + i.name }
func (i commandItem) Description() string { return "Run slash command " + i.name }
func (i commandItem) FilterValue() string { return i.name }

type streamSmoothingState struct {
	targetKind   string
	streamKind   string
	sessionKey   string
	actor        string
	pending      []string
	firstSeen    time.Time
	firstPaint   time.Time
	pendingSince time.Time
	lastTick     time.Time
	budget       float64
	upstreamDone bool
	rendered     int
}

type streamPlaybackMetrics struct {
	FirstByteLatency     time.Duration
	BacklogRunes         int
	MaxBacklogRunes      int
	LastFrameAppendRunes int
	LastFrameRenderCost  time.Duration
	LastFrameAt          time.Time
	Frames               uint64
}

type promptState struct {
	title               string
	prompt              string
	details             []PromptDetail
	secret              bool
	input               []rune
	cursor              int
	choices             []promptChoice
	choiceIndex         int
	scrollOffset        int
	filter              []rune
	filterable          bool
	multiSelect         bool
	allowEmptySelection bool
	allowFreeformInput  bool
	selected            map[string]struct{}
	response            chan PromptResponse
}

type promptChoice struct {
	label         string
	value         string
	detail        string
	alwaysVisible bool
}

type textSelectionPoint struct {
	line int
	col  int
}

type fixedSelectionArea string

const (
	fixedSelectionNone   fixedSelectionArea = ""
	fixedSelectionHint   fixedSelectionArea = "hint"
	fixedSelectionHeader fixedSelectionArea = "header"
	fixedSelectionFooter fixedSelectionArea = "footer"
)

type pendingPrompt struct {
	execLine    string
	displayLine string
	attachments []Attachment
	dispatched  bool
}

func (p pendingPrompt) matchesUserMessage(text string) bool {
	text = strings.TrimSpace(text)
	return text != "" && (text == strings.TrimSpace(p.execLine) || text == strings.TrimSpace(p.displayLine))
}

type inputAttachment struct {
	Name   string
	Offset int
}

type planEntryState struct {
	Content string
	Status  string
}

type btwOverlayState struct {
	Question string
	Answer   string
	Loading  bool
	Scroll   int
}

type sandboxProgressState struct {
	Title     string
	Source    string
	Phase     string
	Message   string
	Step      int
	Total     int
	Done      bool
	UpdatedAt time.Time
}

type Model struct {
	cfg           Config
	theme         tuikit.Theme
	themeCacheKey string
	themeAuto     bool
	noColor       bool
	noAnimation   bool
	colorProfile  colorprofile.Profile
	keys          appKeyMap
	help          help.Model

	width   int
	height  int
	focused bool

	terminalResponseGuardUntil time.Time
	terminalResponsePending    string
	terminalResponsePendingSeq uint64

	// --- Document model (source of truth for viewport content) ---
	doc *Document

	// Active block tracking IDs (empty string means no active block).
	activeAssistantID    string
	activeAssistantActor string
	activeReasoningID    string
	activeReasoningActor string

	// Maps external keys to doc block IDs.
	activeMainACPTurnID            string
	pendingMainACPSessionID        string
	pendingMainACPStartedAt        time.Time
	participantTurnIDs             map[string]string
	activeParticipantTurnSessionID string

	streamLine         string
	pendingLogBuffer   logChunkBuffer
	logStreamBuffer    logStreamBuffer
	lastCommittedStyle tuikit.LineStyle
	lastCommittedRaw   string
	hasCommittedLine   bool
	planEntries        []planEntryState
	welcomeCardPending bool
	liveTurn           liveTurnState

	// Transient log replacement tracking — now uses block IDs.
	transientBlockID string
	transientIsRetry bool
	transientRemove  bool

	// Viewport caches — populated by syncViewportContent from Document.
	viewportStyledLines           []string
	viewportPlainLines            []string
	viewportBlockIDs              []string
	viewportClickTokens           []string
	frameTopTrim                  int
	viewport                      viewport.Model
	viewportFollowState           viewportFollowState
	userScrolledUp                bool
	ready                         bool
	viewportScrollbarVisibleUntil time.Time
	scrollbarDrag                 scrollbarDragState

	selecting           bool
	selectionStart      textSelectionPoint
	selectionEnd        textSelectionPoint
	selectionAutoScroll selectionAutoScrollState

	inputSelecting      bool
	inputSelectionStart textSelectionPoint
	inputSelectionEnd   textSelectionPoint

	fixedSelecting      bool
	fixedSelectionArea  fixedSelectionArea
	fixedSelectionStart textSelectionPoint
	fixedSelectionEnd   textSelectionPoint

	// --- Composer (independent sub-model for input management) ---
	Composer

	// --- Overlay state (unified overlay management) ---
	OverlayState

	spinner spinner.Model
	quit    bool

	runningInterruptRequested bool
	runningTick               uint64
	runningTip                int
	runningActivity           runningActivityState
	runningTickerStyles       []lipgloss.Style
	runningTickerThemeKey     string

	statusModel            string
	statusContext          string
	statusModeLabel        string
	statusView             StatusViewModel
	stableWorkspaceDisplay string
	statusRefreshInFlight  bool
	sandboxProgress        *sandboxProgressState
	sandboxProgressBar     progress.Model
	hint                   string
	hintEntries            []hintEntry
	nextHintID             uint64

	pendingInputAt            time.Time
	inputLatencyWindow        []time.Duration
	inputLatencyCount         uint64
	diag                      Diagnostics
	lastDiagnosticsDebugWrite time.Time

	ctrlCArmed  bool
	lastCtrlCAt time.Time
	ctrlCArmSeq uint64

	streamSmoothing                map[string]*streamSmoothingState
	streamSmoothingTickScheduled   bool
	pendingRenderEvents            pendingRenderEvents
	renderDrainTickScheduled       bool
	spinnerTickScheduled           bool
	deferredBatchTickScheduled     bool
	offscreenViewportDirty         bool
	offscreenViewportTickScheduled bool
	offscreenViewportSyncAt        time.Time
	viewportSyncPending            bool
	viewportSyncTickScheduled      bool
	scrollbarTickScheduled         bool
	streamPlayback                 streamPlaybackMetrics
	viewportRenderEntries          []viewportRenderEntry
	lastViewportRenderContextKey   string
	lastViewportRenderHeight       int
	dirtyViewportBlocks            map[string]struct{}
	viewportStructureDirty         bool
	viewportContentVersion         uint64
	lastViewportContentVersion     uint64
	viewportSelectionVersion       uint64
	lastViewportContent            string
	viewportContentStale           bool
	lastViewportStreamLine         string
	lastViewportViewKey            string
	lastViewportViewRendered       string
	viewportSyncDepth              int
	viewportDirty                  bool
}
