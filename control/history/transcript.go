package history

import (
	"encoding/json"
	"reflect"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	TranscriptBytes        = 2 << 20
	TranscriptEvents       = 4096
	transcriptMessageBytes = 64 << 10
)

// Transcript is a disposable display projection, never model context. The
// newest two Turns retain details; older Turns retain dialogue (including
// Agent communication). Both a single Turn and the total window are bounded.
// Callers serialize access and retain ownership of the authoritative history.
type Transcript struct {
	events  []*session.Event
	sizes   []int
	bytes   int
	turns   []string
	trimmed bool
}

// Append returns the retained projection receiving source, or nil when source
// is omitted. Its identity is stable across coalesced chunks, but later appends
// may evict it. Callers may associate display metadata; they must not mutate it.
func (t *Transcript) Append(source *session.Event) *session.Event {
	if source == nil {
		return nil
	}
	e := session.CloneEvent(source)
	turn := transcriptTurn(e)
	if turn != "" && (len(t.turns) == 0 || t.turns[len(t.turns)-1] != turn) {
		found := false
		for _, id := range t.turns {
			found = found || id == turn
		}
		if !found {
			t.turns = append(t.turns, turn)
			t.compact()
		}
		if found && len(t.turns) > 2 && turn != t.turns[len(t.turns)-2] && !transcriptDialogue(e) {
			t.trimmed = true
			return nil
		}
	}
	// Text chunks carry no independent tool state. Coalesce only adjacent
	// chunks from the same message and speaker, without crossing a Turn.
	if len(t.events) > 0 && mergeTranscriptText(t.events[len(t.events)-1], e) {
		i := len(t.events) - 1
		raw, _ := json.Marshal(t.events[i])
		t.bytes += len(raw) - t.sizes[i]
		t.sizes[i] = len(raw)
		e = t.events[i]
	} else {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil
		}
		if len(raw) > TranscriptBytes/2 {
			// Oversized display records cannot exclude every later event. The
			// provider's raw record remains available in its own Session store.
			t.trimmed = true
			return nil
		}
		t.events = append(t.events, e)
		t.sizes = append(t.sizes, len(raw))
		t.bytes += len(raw)
	}
	for len(t.events) > TranscriptEvents || t.bytes > TranscriptBytes {
		t.dropFirst()
	}
	return e
}

func (t *Transcript) compact() {
	if len(t.turns) <= 2 {
		return
	}
	recent := t.turns[len(t.turns)-2:]
	oldest := ""
	if len(t.turns) > MaxTurns {
		oldest = t.turns[0]
		t.turns = t.turns[1:]
	}
	events, sizes := t.events[:0], t.sizes[:0]
	t.bytes = 0
	for i, e := range t.events {
		turn := transcriptTurn(e)
		keep := turn != oldest || oldest == ""
		keep = keep && (turn == recent[0] || turn == recent[1] || transcriptDialogue(e))
		if !keep {
			t.trimmed = true
			continue
		}
		events, sizes = append(events, e), append(sizes, t.sizes[i])
		t.bytes += t.sizes[i]
	}
	clear(t.events[len(events):])
	t.events, t.sizes = events, sizes
}

func (t *Transcript) dropFirst() {
	t.bytes -= t.sizes[0]
	t.events[0] = nil
	t.events, t.sizes = t.events[1:], t.sizes[1:]
	t.trimmed = true
}

// Events returns the retained, ordered projection. Callers must not mutate it.
func (t *Transcript) Events() []*session.Event { return t.events }

// Trimmed reports whether display details or an older prefix were omitted.
func (t *Transcript) Trimmed() bool { return t.trimmed }

func transcriptTurn(e *session.Event) string {
	if e.Scope != nil {
		return e.Scope.TurnID
	}
	return ""
}

func transcriptDialogue(e *session.Event) bool {
	if u := session.ProtocolUpdateOf(e); u != nil {
		return u.SessionUpdate == "user_message_chunk" || u.SessionUpdate == "agent_message_chunk"
	}
	switch session.EventTypeOf(e) {
	case session.EventTypeUser, session.EventTypeAssistant:
		return true
	}
	return false
}

func mergeTranscriptText(a, b *session.Event) bool {
	ua, ub := session.ProtocolUpdateOf(a), session.ProtocolUpdateOf(b)
	if ua == nil || ub == nil || ua.SessionUpdate != ub.SessionUpdate ||
		a.Visibility != session.VisibilityUIOnly || b.Visibility != session.VisibilityUIOnly ||
		!transcriptDialogue(a) || transcriptTurn(a) != transcriptTurn(b) ||
		session.EventMessageID(a) != session.EventMessageID(b) || !reflect.DeepEqual(a.Actor, b.Actor) ||
		!reflect.DeepEqual(a.ChildOrigin, b.ChildOrigin) {
		return false
	}
	var ca, cb struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	ra, ea := json.Marshal(ua.Content)
	rb, eb := json.Marshal(ub.Content)
	if ea != nil || eb != nil || json.Unmarshal(ra, &ca) != nil || json.Unmarshal(rb, &cb) != nil || ca.Type != "text" || cb.Type != "text" || len(ca.Text)+len(cb.Text) > transcriptMessageBytes {
		return false
	}
	text := session.EventText(a) + session.EventText(b)
	a.Text = text
	if a.Message != nil {
		message := model.NewTextMessage(a.Message.Role, text)
		a.Message = &message
	}
	a.Protocol.Update.Content = session.ProtocolTextContent(text)
	return true
}
