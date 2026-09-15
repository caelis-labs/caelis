package tuiapp

import "testing"

// commitUserDisplayLine seeds committed transcript history for renderer fixtures.
// Submission tests must use submitInteractiveLine and canonical user events.
func (m *Model) commitUserDisplayLine(text string) {
	m.appendUserNarrativeBlock(NewUserNarrativeBlock(text))
}

func TestUserNarrativeIdentityFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		setIdentity func(*TranscriptEvent, string)
	}{
		{"source event", func(e *TranscriptEvent, id string) { e.SourceEventID = id }},
		{"source projection", func(e *TranscriptEvent, id string) { e.SourceProjectionID = id }},
		{"message", func(e *TranscriptEvent, id string) { e.MessageID = id }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			event := TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
				ScopeID: "session-1", TurnID: "turn-1", NarrativeKind: TranscriptNarrativeUser, Text: "same text", Final: true}
			tc.setIdentity(&event, "one")
			model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event, event}})
			if got := countUserNarrativeBlocksForTest(model, event.Text); got != 1 {
				t.Fatalf("retransmission blocks = %d, want one", got)
			}
			tc.setIdentity(&event, "two")
			model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
			if got := countUserNarrativeBlocksForTest(model, event.Text); got != 2 {
				t.Fatalf("distinct identity blocks = %d, want two", got)
			}
		})
	}
}
