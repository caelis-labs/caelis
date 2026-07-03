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
	ev := SubagentEvent{Kind: SENotice, Text: text, NoticeKind: noticeKind}
	if !occurredAt.IsZero() {
		ev.StartedAt = occurredAt
		ev.EndedAt = occurredAt
	}
	if noticeKind == transcript.NoticeKindModelRetry {
		if n := len(b.Events); n > 0 && b.Events[n-1].Kind == SENotice && b.Events[n-1].NoticeKind == transcript.NoticeKindModelRetry {
			b.Events[n-1] = ev
			return
		}
	}
	b.Events = append(b.Events, ev)
}
