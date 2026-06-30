package tuiapp

import (
	"strings"
	"time"
)

func (b *MainACPTurnBlock) AddNotice(text string, occurredAt time.Time) {
	if b == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	ev := SubagentEvent{Kind: SENotice, Text: text}
	if !occurredAt.IsZero() {
		ev.StartedAt = occurredAt
		ev.EndedAt = occurredAt
	}
	b.Events = append(b.Events, ev)
}
