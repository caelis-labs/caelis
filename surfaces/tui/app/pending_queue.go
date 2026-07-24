package tuiapp

import (
	"strings"
)

type pendingPromptState uint8

const (
	pendingPromptQueued pendingPromptState = iota
	pendingPromptDispatched
	pendingPromptAwaitingActiveDisplay
	pendingPromptRendered
)

type pendingPrompt struct {
	execLine    string
	displayLine string
	attachments []Attachment
	state       pendingPromptState
}

type pendingPromptQueue []pendingPrompt

type pendingPromptEnqueueOptions struct {
	execLine       string
	displayLine    string
	attachments    []Attachment
	deferUntilIdle bool
	deferDisplay   bool
	submissionMode SubmissionMode
}

func (q *pendingPromptQueue) enqueue(opts pendingPromptEnqueueOptions) {
	if q == nil {
		return
	}
	state := pendingPromptQueued
	if !opts.deferUntilIdle {
		state = pendingPromptDispatched
		if !opts.deferDisplay && opts.submissionMode == SubmissionModeActiveTurn {
			state = pendingPromptAwaitingActiveDisplay
		}
	}
	*q = append(*q, pendingPrompt{
		execLine:    strings.TrimSpace(opts.execLine),
		displayLine: strings.TrimSpace(opts.displayLine),
		attachments: cloneAttachments(opts.attachments),
		state:       state,
	})
}

func (q *pendingPromptQueue) matchGatewayEcho(texts ...string) (pendingPrompt, bool) {
	if q == nil || len(*q) == 0 {
		return pendingPrompt{}, false
	}
	needles := make([]string, 0, len(texts))
	for _, text := range texts {
		if needle := strings.TrimSpace(text); needle != "" {
			needles = append(needles, needle)
		}
	}
	if len(needles) == 0 {
		return pendingPrompt{}, false
	}
	for i, pending := range *q {
		for _, needle := range needles {
			if pending.matchesUserMessage(needle) {
				*q = append((*q)[:i], (*q)[i+1:]...)
				return pending, true
			}
		}
	}
	return pendingPrompt{}, false
}

func (q pendingPromptQueue) visibleCount() int {
	count := 0
	for _, pending := range q {
		if pending.isVisiblePending() {
			count++
		}
	}
	return count
}

func (q pendingPromptQueue) nextVisible() (pendingPrompt, bool) {
	i, ok := q.nextIndex(func(p pendingPrompt) bool {
		return p.isVisiblePending()
	})
	if !ok {
		return pendingPrompt{}, false
	}
	return q[i], true
}

func (q pendingPromptQueue) nextIndex(match func(pendingPrompt) bool) (int, bool) {
	if match == nil {
		return -1, false
	}
	for i, pending := range q {
		if match(pending) {
			return i, true
		}
	}
	return -1, false
}

func (q *pendingPromptQueue) acceptedActiveDisplay() (pendingPrompt, bool) {
	if q == nil {
		return pendingPrompt{}, false
	}
	i, ok := q.nextIndex(func(p pendingPrompt) bool {
		return p.awaitsAcceptedActiveDisplay()
	})
	if !ok {
		return pendingPrompt{}, false
	}
	pending := &(*q)[i]
	out := *pending
	pending.markLocallyRendered()
	return out, true
}

func (q *pendingPromptQueue) onTurnEnd(canDispatchDeferred bool, clearOnAbort bool) (pendingPrompt, bool) {
	if q == nil {
		return pendingPrompt{}, false
	}
	var next pendingPrompt
	hasNext := false
	if canDispatchDeferred {
		next, hasNext = q.takeNextDeferred()
	}
	q.discardDispatched()
	if !hasNext && clearOnAbort {
		q.discardQueuedAfterAbort()
	}
	return next, hasNext
}

func (q *pendingPromptQueue) takeNextDeferred() (pendingPrompt, bool) {
	if q == nil || len(*q) == 0 {
		return pendingPrompt{}, false
	}
	i, ok := q.nextIndex(func(p pendingPrompt) bool {
		return p.canDispatchAfterIdle()
	})
	if !ok {
		return pendingPrompt{}, false
	}
	pending := (*q)[i]
	*q = append((*q)[:i], (*q)[i+1:]...)
	return pending, true
}

func (q *pendingPromptQueue) discardDispatched() {
	if q == nil || len(*q) == 0 {
		return
	}
	out := (*q)[:0]
	for _, pending := range *q {
		if pending.canDispatchAfterIdle() || pending.needsGatewayEchoCorrelation() {
			out = append(out, pending)
		}
	}
	*q = out
}

func (q *pendingPromptQueue) discardQueuedAfterAbort() {
	if q == nil || len(*q) == 0 {
		return
	}
	out := (*q)[:0]
	for _, pending := range *q {
		if pending.needsGatewayEchoCorrelation() {
			out = append(out, pending)
		}
	}
	*q = out
}

func (p pendingPrompt) matchesUserMessage(text string) bool {
	text = strings.TrimSpace(text)
	return text != "" && (text == strings.TrimSpace(p.execLine) || text == strings.TrimSpace(p.displayLine))
}

func (p pendingPrompt) canDispatchAfterIdle() bool {
	return p.state == pendingPromptQueued
}

func (p pendingPrompt) isVisiblePending() bool {
	return p.state != pendingPromptRendered
}

func (p pendingPrompt) isLocallyRendered() bool {
	return p.state == pendingPromptRendered
}

func (p pendingPrompt) needsGatewayEchoCorrelation() bool {
	return p.state == pendingPromptAwaitingActiveDisplay || p.state == pendingPromptRendered
}

func (p pendingPrompt) awaitsAcceptedActiveDisplay() bool {
	return p.state == pendingPromptAwaitingActiveDisplay
}

func (p *pendingPrompt) markLocallyRendered() {
	if p == nil {
		return
	}
	// Keep rendered prompts in the queue until their gateway echo arrives, so
	// echo correlation can remove the exact accepted active-turn prompt.
	p.state = pendingPromptRendered
}

func (p pendingPrompt) displayText() string {
	if text := strings.TrimSpace(p.displayLine); text != "" {
		return text
	}
	return strings.TrimSpace(p.execLine)
}

func (m *Model) renderNextAcceptedPendingPrompt() bool {
	pending, ok := m.pendingQueue.acceptedActiveDisplay()
	if !ok {
		return false
	}
	text := pending.displayText()
	if text == "" {
		return false
	}
	m.commitUserDisplayLine(text)
	m.ensureViewportLayout()
	m.syncViewportContent()
	return true
}
