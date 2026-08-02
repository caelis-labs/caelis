package tuiapp

import (
	"context"
	"time"

	"charm.land/bubbles/v2/list"
)

// OverlayState groups all overlay-related state: BTW drawer, prompt modal,
// slash completion, palette, file mentions, resume picker, and
// slash-arg overlays. It is embedded in Model so that field access
// (e.g. m.btwOverlay) continues to work unchanged.
//
// Overlays MUST NOT directly modify Document blocks. They render above or
// below the viewport as temporary UI and communicate results via Submission
// or callback messages.
type OverlayState struct {
	btwOverlay   *btwOverlayState
	btwDismissed bool

	subagentOverlay    *subagentOverlayState
	subagentRequestSeq uint64

	subagentOutputOverlay *subagentOutputOverlayState

	activePrompt  *promptState
	pendingPrompt []PromptRequestMsg

	showPalette      bool
	palette          list.Model
	paletteAnimLines int
	paletteAnimating bool

	mentionQuery      string
	mentionPrefix     string
	mentionCandidates []CompletionCandidate
	mentionIndex      int
	mentionStart      int
	mentionEnd        int
	mentionLimit      int

	slashCandidates       []string
	slashDisplays         map[string]string
	slashDetails          map[string]string
	slashIndex            int
	slashPrefix           string
	slashSkillCatalog     []CompletionCandidate
	slashSkillLoaded      bool
	slashSkillLoadPending bool
	slashSkillLoadSeq     uint64

	resumeActive         bool
	resumeQuery          string
	resumeLoaded         bool
	resumeCandidates     []ResumeCandidate
	resumeIndex          int
	resumeRequestSeq     uint64
	resumeRequestQuery   string
	resumeRequestPending bool
	resumeRequestCancel  context.CancelFunc

	slashArgActive           bool
	slashArgCommand          string
	slashArgQuery            string
	slashArgCandidates       []SlashArgCandidate
	slashArgIndex            int
	slashArgLoadSeq          uint64
	slashArgLoadPending      bool
	slashArgLoadLabel        string
	slashArgLoadStartedAt    time.Time
	slashArgLoadBytes        int64
	slashArgLoadAuthURL      string
	slashArgLoadAuthCode     string
	slashArgLoadAuthPrompt   chan PromptResponse
	slashArgLoadCancel       context.CancelFunc
	slashArgLoaded           bool
	slashArgLoadedCommand    string
	slashArgLoadedCandidates []SlashArgCandidate

	completionWindow completionWindowState
	completionMouse  completionMouseState

	completionRefreshSeq uint64
}

// HasActiveOverlay returns true if any overlay is currently visible.
func (o *OverlayState) HasActiveOverlay() bool {
	return o.btwOverlay != nil ||
		o.subagentOverlay != nil ||
		o.subagentOutputOverlay != nil ||
		o.activePrompt != nil ||
		o.showPalette ||
		len(o.mentionCandidates) > 0 ||
		len(o.slashCandidates) > 0 ||
		o.resumeActive ||
		o.slashArgActive
}
