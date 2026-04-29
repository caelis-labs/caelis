package tuiapp

import (
	"charm.land/bubbles/v2/list"
)

// OverlayState groups all overlay-related state: BTW drawer, prompt modal,
// slash completion, palette, mention/skill completions, resume picker, and
// slash-arg overlays. It is embedded in Model so that field access
// (e.g. m.btwOverlay) continues to work unchanged.
//
// Overlays MUST NOT directly modify Document blocks. They render above or
// below the viewport as temporary UI and communicate results via Submission
// or callback messages.
type OverlayState struct {
	btwOverlay   *btwOverlayState
	btwDismissed bool

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

	skillQuery      string
	skillCandidates []CompletionCandidate
	skillIndex      int
	skillStart      int
	skillEnd        int

	slashCandidates []string
	slashIndex      int
	slashPrefix     string

	resumeActive     bool
	resumeQuery      string
	resumeCandidates []ResumeCandidate
	resumeIndex      int

	slashArgActive     bool
	slashArgCommand    string
	slashArgQuery      string
	slashArgCandidates []SlashArgCandidate
	slashArgIndex      int
}

// HasActiveOverlay returns true if any overlay is currently visible.
func (o *OverlayState) HasActiveOverlay() bool {
	return o.btwOverlay != nil ||
		o.activePrompt != nil ||
		o.showPalette ||
		len(o.mentionCandidates) > 0 ||
		len(o.skillCandidates) > 0 ||
		len(o.slashCandidates) > 0 ||
		o.resumeActive ||
		o.slashArgActive
}
