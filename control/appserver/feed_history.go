package appserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/history"
	"github.com/caelis-labs/caelis/control/streamspool"
)

type feedHistoryWindow struct {
	after  uint64
	before string
	finite bool
}

func (b *FeedBroker) historyWindow(ctx context.Context, source string, key streamspool.Key, high uint64, turns int) (feedHistoryWindow, error) {
	if turns == 0 || source == "canonical" && b.reader == nil {
		return feedHistoryWindow{}, nil
	}
	e := b.histories.Get(source)
	e.Lock()
	defer e.Unlock()
	if source == "canonical" {
		if metadata, ok := b.reader.(session.EventMetadataReader); ok {
			if err := b.indexHistoryMetadata(ctx, metadata, &e.Index, high); err != nil {
				return feedHistoryWindow{}, err
			}
		} else {
			for e.Index.High < high {
				page, err := b.reader.EventsPage(ctx, session.EventPageRequest{SessionRef: b.ref, AfterSeq: e.Index.High, ThroughSeq: high, Visibility: session.EventPageClientReplay})
				if err != nil {
					return feedHistoryWindow{}, err
				}
				for _, event := range page.Events {
					if event == nil || event.ChildOrigin != nil || event.Seq == 0 {
						continue
					}
					if event.Scope != nil {
						e.Index.Observe(event.Seq-1, event.Scope.TurnID)
					} else if session.ResolvedApprovalReview(event) != nil {
						base := acpprojector.EnvelopeBaseFromSessionEvent(b.ref, event, acpprojector.SessionEventTransport{})
						if base.Scope == eventstream.ScopeMain {
							e.Index.Observe(event.Seq-1, base.TurnID)
						}
					}
				}
				if page.NextSeq <= e.Index.High {
					e.Index.High = high
					break
				}
				e.Index.High = page.NextSeq
			}
		}
	} else if e.Index.High < high {
		reader, err := b.spool.Reader(ctx, key, streamspool.Offset(e.Index.High))
		if err != nil {
			return feedHistoryWindow{}, err
		}
		defer reader.Close()
		for e.Index.High < high {
			raw, err := reader.Next(ctx)
			if err != nil {
				return feedHistoryWindow{}, err
			}
			var envelope eventstream.Envelope
			if err := json.Unmarshal(raw.Payload, &envelope); err != nil {
				return feedHistoryWindow{}, err
			}
			if envelope.Scope == "" || envelope.Scope == eventstream.ScopeMain {
				e.Index.Observe(uint64(raw.Offset), envelope.TurnID)
			}
			e.Index.High = uint64(raw.Offset) + 1
		}
	}
	after := e.Index.Start(high, turns)
	return feedHistoryWindow{after: after, before: history.Encode(b.codec.secret, history.Position{SessionID: b.ref.SessionID, Source: source, Before: after})}, nil
}

func (b *FeedBroker) subscribeEarlier(ctx context.Context, req SubscribeRequest) (SubscribeResult, error) {
	p, err := history.Decode(b.codec.secret, req.HistoryBefore, b.ref.SessionID, "")
	if err != nil {
		return SubscribeResult{}, err
	}
	turns := req.HistoryTurns
	if turns == 0 {
		turns = history.DefaultTurns
	}
	b.acceptMu.Lock()
	key := b.key
	b.acceptMu.Unlock()
	if p.Source != "canonical" {
		if p.Source != sessionSpoolGeneration(key) {
			return SubscribeResult{}, history.Stale()
		}
		bounds, anchor, err := b.exactBounds(ctx, sessionSpoolCursor{Key: key, Offset: streamspool.Offset(p.Before)})
		if err != nil || !bounds.OriginComplete || bounds.Low != 0 {
			return SubscribeResult{}, history.Stale()
		}
		window, err := b.historyWindow(ctx, p.Source, key, p.Before, turns)
		if err != nil {
			return SubscribeResult{}, err
		}
		window.finite = true
		pos := sessionBoundaryPosition(anchor, key, streamspool.Offset(p.Before))
		cursor, err := b.codec.EncodeSpool(b.ref.SessionID, sessionSpoolCursor{Key: key, Offset: streamspool.Offset(p.Before)}, pos)
		if err != nil {
			return SubscribeResult{}, err
		}
		sub := b.startSubscription(ctx, streamspool.Offset(window.after), streamspool.Offset(p.Before), nil, 0, eventstream.DurableFeedPosition{}, "", "", window)
		return SubscribeResult{Subscription: sub, BoundaryCursor: cursor, BoundaryPosition: &pos}, nil
	}
	if b.reader == nil {
		return SubscribeResult{}, fmt.Errorf("Session history reader unavailable")
	}
	window, err := b.historyWindow(ctx, p.Source, key, p.Before, turns)
	if err != nil {
		return SubscribeResult{}, err
	}
	window.finite = true
	through := eventstream.DurableFeedPosition{Seq: p.Before, ProjectionIndex: ^uint32(0)}
	pos := eventstream.FeedPosition{Durable: &through}
	cursor, err := b.codec.Encode(b.ref.SessionID, pos)
	if err != nil {
		return SubscribeResult{}, err
	}
	sub := b.startSubscription(ctx, 0, 0, &through, p.Before, through, "", "", window)
	return SubscribeResult{Subscription: sub, BoundaryCursor: cursor, BoundaryPosition: &pos}, nil
}

func (b *FeedBroker) indexHistoryMetadata(ctx context.Context, reader session.EventMetadataReader, index *history.Index, high uint64) error {
	for index.High < high {
		page, err := reader.EventMetadataPage(ctx, session.EventPageRequest{SessionRef: b.ref, AfterSeq: index.High, ThroughSeq: high, Limit: 2000, Visibility: session.EventPageClientReplay})
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			if event.Child || event.Seq == 0 {
				continue
			}
			if event.HasScope {
				index.Observe(event.Seq-1, event.TurnID)
			} else if event.ReviewedApproval {
				// Legacy reviewed decisions keep provenance in their pause token. Only
				// those records need payload reads; the projector still owns routing.
				events, err := b.reader.EventsPage(ctx, session.EventPageRequest{SessionRef: b.ref, AfterSeq: event.Seq - 1, ThroughSeq: event.Seq, Limit: 1, Visibility: session.EventPageClientReplay})
				if err != nil {
					return err
				}
				for _, record := range events.Events {
					base := acpprojector.EnvelopeBaseFromSessionEvent(b.ref, record, acpprojector.SessionEventTransport{})
					if base.Scope == eventstream.ScopeMain {
						index.Observe(record.Seq-1, base.TurnID)
					}
				}
			}
		}
		if page.NextSeq <= index.High {
			index.High = high
			break
		}
		index.High = page.NextSeq
	}
	return nil
}

// The publisher already owns each exact spool record. Index its identity once
// while accepting it; a reader only scans if an index was evicted or has a gap.
func (b *FeedBroker) indexAcceptedHistory(envelope eventstream.Envelope, offset uint64) {
	entry := b.histories.Get(sessionSpoolGeneration(b.key))
	entry.Lock()
	defer entry.Unlock()
	if entry.Index.High != offset {
		return
	}
	if envelope.Scope == "" || envelope.Scope == eventstream.ScopeMain {
		entry.Index.Observe(offset, envelope.TurnID)
	}
	entry.Index.High = offset + 1
}
