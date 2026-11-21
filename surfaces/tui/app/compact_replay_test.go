package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func TestCompactCheckpointLiveAndReplay(t *testing.T) {
	t.Parallel()

	ref := session.SessionRef{SessionID: "session-1"}
	turnID := "turn-1"
	at := time.Unix(120, 0)
	checkpoint := func(id string, seq uint64) eventstream.Envelope {
		t.Helper()
		event := &session.Event{ID: id, Seq: seq, Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical,
			Time: at.Add(time.Duration(seq) * time.Second), Text: "CONTEXT CHECKPOINT\nObjective: continue"}
		base := projection.EnvelopeBaseFromSessionEvent(ref, event, projection.SessionEventTransport{TurnID: turnID})
		env := projection.ProjectSessionEventEnvelope(base, event)[0]
		raw, err := wirev1.MarshalEnvelope(env)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := wirev1.UnmarshalEnvelope(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := decoded.Update.(eventstream.ContentChunk); !ok || decoded.EventID != id ||
			decoded.ProjectionID != env.ProjectionID || *decoded.Position.Durable != *env.Position.Durable {
			t.Fatalf("wire checkpoint lost type or identity: %+v", decoded)
		}
		return decoded
	}
	first, second := checkpoint("checkpoint-1", 10), checkpoint("checkpoint-2", 12)
	start := eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: ref.SessionID, TurnID: turnID,
		Scope: eventstream.ScopeMain, OccurredAt: at.Add(9 * time.Second), Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting}}
	success := eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: ref.SessionID, TurnID: turnID, EventID: second.EventID,
		Scope: eventstream.ScopeMain, NoticeKind: eventstream.NoticeKindCompact, Notice: transcript.CompactNoticeLabel}

	m := newACPEventstreamRegressionModel(t, 120, 32)
	m.currentSessionID = ref.SessionID
	m.beginLiveTurn(SubmissionModeDefault, false, at)
	apply := func(env eventstream.Envelope) { m = applyACPEventstreamRegressionEnvelope(t, m, env) }
	assertFrame := func(rows int, compacting bool) {
		t.Helper()
		m.flushPendingViewportSync()
		frame := m.View().Content
		if strings.Count(frame, transcript.CompactNoticeLabel) != rows ||
			strings.Contains(frame, "Compacting context") != compacting || strings.Contains(frame, "CONTEXT CHECKPOINT") {
			t.Fatalf("want %d compact rows and compacting=%v:\n%s", rows, compacting, frame)
		}
	}
	apply(start)
	assertFrame(0, true)
	apply(first) // The transient success was dropped.
	assertFrame(1, false)
	start.OccurredAt = at.Add(11 * time.Second)
	apply(start)
	apply(first) // Retransmission cannot clear the next compaction.
	assertFrame(1, true)
	apply(second)
	apply(success)
	assertFrame(2, false)

	m.beginSessionHistory(sessionViewStartMsg{state: appserver.SessionState{SessionID: ref.SessionID}, replacement: true})
	builder := m.sessionHistory.model
	replayed := []eventstream.Envelope{first, first, start, second, success}
	builder.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: projectResumeReplayEvents(replayed), ReconnectReplay: true})
	m.commitSessionHistory()
	apply(success) // Catch-up can cross from replay into the paired notice.
	apply(second)
	assertFrame(2, false)
	folded := joinRenderedPlain(renderHistoricalTurn(requireMainACPTurnBlockForTest(t, m), historicalTestContext()))
	if strings.Count(folded, transcript.CompactNoticeLabel) != 2 {
		t.Fatalf("folded history lost compact rows:\n%s", folded)
	}
}
