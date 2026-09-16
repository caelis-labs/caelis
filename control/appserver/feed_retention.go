package appserver

import (
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/streamspool"
)

// replaceRetainedPrefix repairs one lagging observer and resumes its exact
// stream at the same atomic durable/spool cut used by a fresh attachment.
// Other readers and the producer retain their healthy shared writer.
func (s *feedSubscription) replaceRetainedPrefix() error {
	b := s.broker
	if b.reader == nil {
		return streamspool.ErrExpired
	}
	if err := b.Prime(s.ctx); err != nil {
		return err
	}
	b.acceptMu.Lock()
	if b.writer == nil || b.spoolErr != nil {
		b.acceptMu.Unlock()
		return streamspool.ErrUnavailable
	}
	bounds, err := b.writer.Bounds(s.ctx)
	through := b.latestDurable
	key := b.key
	b.acceptMu.Unlock()
	if err != nil {
		return err
	}
	position := sessionBoundaryPosition(through, key, bounds.High)
	cursor, err := b.codec.EncodeSpool(b.ref.SessionID, sessionSpoolCursor{Key: key, Offset: bounds.High}, position)
	if err != nil {
		return err
	}
	s.history = feedHistoryWindow{}
	s.start, s.initialHigh = bounds.High, bounds.High
	s.replayThrough = &eventstream.DurableFeedPosition{Seq: through.Seq, ProjectionIndex: through.ProjectionIndex}
	s.canonicalAfter, s.lastDurable = through.Seq, through
	s.syncCursor = cursor
	s.refreshReplacementID()
	return s.deliverCanonicalReplacement()
}
