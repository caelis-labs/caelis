package tuiapp

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func (b *MainACPTurnBlock) AddNotice(text string, occurredAt time.Time, noticeKind transcript.NoticeKind) {
	if b == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if noticeKind != transcript.NoticeKindModelRetry {
		b.clearTransientRetryNotice()
	}
	if appendNoticeEvent(&b.Events, text, occurredAt, noticeKind) {
		b.advanceNarrativeBoundary()
	}
}

func (b *ParticipantTurnBlock) AddNotice(text string, occurredAt time.Time, noticeKind transcript.NoticeKind) {
	if b == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if noticeKind != transcript.NoticeKindModelRetry {
		b.clearTransientRetryNotice()
	}
	if appendNoticeEvent(&b.Events, text, occurredAt, noticeKind) {
		b.advanceNarrativeBoundary()
	}
}

func (b *MainACPTurnBlock) clearTransientRetryNotice() {
	if b == nil {
		return
	}
	clearModelRetryNotices(&b.Events)
}

func (b *ParticipantTurnBlock) clearTransientRetryNotice() {
	if b == nil {
		return
	}
	clearModelRetryNotices(&b.Events)
}

func appendNoticeEvent(events *[]SubagentEvent, text string, occurredAt time.Time, noticeKind transcript.NoticeKind) bool {
	if events == nil {
		return false
	}
	ev := SubagentEvent{Kind: SENotice, Text: text, NoticeKind: noticeKind}
	if !occurredAt.IsZero() {
		ev.StartedAt = occurredAt
		ev.EndedAt = occurredAt
	}
	if noticeKind == transcript.NoticeKindModelRetry {
		if n := len(*events); n > 0 && (*events)[n-1].Kind == SENotice && (*events)[n-1].NoticeKind == transcript.NoticeKindModelRetry {
			(*events)[n-1] = ev
			return false
		}
	}
	*events = append(*events, ev)
	return true
}

// clearModelRetryNotices drops ephemeral retry banners. Another retry notice
// merges in place; any other transcript or lifecycle mutation resumes the
// ordinary Turn by removing them.
func clearModelRetryNotices(events *[]SubagentEvent) {
	if events == nil || len(*events) == 0 {
		return
	}
	out := (*events)[:0]
	for _, ev := range *events {
		if isModelRetryNotice(ev) {
			continue
		}
		out = append(out, ev)
	}
	clear((*events)[len(out):])
	*events = out
}
