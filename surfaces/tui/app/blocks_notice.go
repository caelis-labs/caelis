package tuiapp

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func (b *MainACPTurnBlock) AddNotice(text string, occurredAt time.Time, noticeKind transcript.NoticeKind) {
	if b == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
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
	if appendNoticeEvent(&b.Events, text, occurredAt, noticeKind) {
		b.advanceNarrativeBoundary()
	}
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
