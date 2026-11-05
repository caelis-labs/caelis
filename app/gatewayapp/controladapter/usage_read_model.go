package controladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// UsageReadModel is a Host-owned, disposable status projection. Events and
// legacy Session state remain authoritative. No projection work is added to
// writes: the first read rebuilds, then positive durable sequences catch up.
// Restart or eviction rebuilds once; neither can change accounting results.
type UsageReadModel struct {
	gate    chan struct{}
	entries map[session.SessionRef]*usageReadEntry
	used    map[session.SessionRef]uint64
	clock   uint64
}

const maxUsageReadSessions = 128
const maxUsageReadBytes = 64 << 20

// NewUsageReadModel creates a read model shared by a Host's status requests.
func NewUsageReadModel() *UsageReadModel {
	return &UsageReadModel{gate: make(chan struct{}, 1), entries: map[session.SessionRef]*usageReadEntry{}, used: map[session.SessionRef]uint64{}}
}

type usageReadEntry struct {
	created    time.Time
	revision   uint64
	stateUsage sessionTokenUsageBreakdown
	seq        uint64
	bytes      int
	facts      []*session.Event
	context    *runtime.ContextUsageProjection
	history    map[usageHistoryKey]sessionTokenUsageBreakdown
}

type usageHistoryKey struct {
	category, kind, epoch, controller, provider, model string
}

type sessionUsageView struct {
	active    session.Session
	events    []*session.Event
	context   *runtime.ContextUsageProjection
	breakdown sessionTokenUsageBreakdown
}

func (m *UsageReadModel) read(ctx context.Context, d *assembler, ref session.SessionRef, category string, needContext bool) (sessionUsageView, error) {
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return sessionUsageView{}, ctx.Err()
	}
	defer func() { <-m.gate }()
	reader := d.deps.Session.Store
	ref = session.NormalizeSessionRef(ref)
	old := m.entries[ref]
	paged, hasPages := reader.(session.PagedReader)
	checkpoints, hasCuts := reader.(session.EventCheckpointReader)
	var cut session.EventCheckpoint
	var state map[string]any
	var stateUsage sessionTokenUsageBreakdown
	var err error
	if hasCuts {
		// State-only mutations are revisioned. A stable revision brackets the
		// state read while the checkpoint binds metadata and the event cut.
		for {
			if cut, err = checkpoints.EventCheckpoint(ctx, ref); err != nil {
				return sessionUsageView{}, err
			}
			if old != nil && old.created.Equal(cut.Session.CreatedAt) && old.revision == cut.Session.Revision && old.seq <= cut.ThroughSeq {
				stateUsage = old.stateUsage
				break
			}
			if state, err = reader.SnapshotState(ctx, ref); err != nil {
				return sessionUsageView{}, err
			}
			verified, err := checkpoints.EventCheckpoint(ctx, ref)
			if err != nil {
				return sessionUsageView{}, err
			}
			if verified.Session.Revision == cut.Session.Revision && verified.Session.CreatedAt.Equal(cut.Session.CreatedAt) {
				stateUsage = sessionTokenUsageBreakdownFromState(state)
				break
			}
		}
	} else {
		if cut.Session, err = reader.Session(ctx, ref); err != nil {
			return sessionUsageView{}, err
		}
		if state, err = reader.SnapshotState(ctx, ref); err != nil {
			return sessionUsageView{}, err
		}
		stateUsage = sessionTokenUsageBreakdownFromState(state)
	}
	if !hasCuts || !hasPages || old != nil && (!old.created.Equal(cut.Session.CreatedAt) || cut.ThroughSeq < old.seq || needContext && old.context == nil) {
		old = nil
	}
	next := old
	if old == nil || old.seq != cut.ThroughSeq {
		next = &usageReadEntry{created: cut.Session.CreatedAt, history: map[usageHistoryKey]sessionTokenUsageBreakdown{}}
		if old != nil {
			next.seq, next.bytes = old.seq, old.bytes
			next.facts = append([]*session.Event(nil), old.facts...)
			next.history = old.history
		}
		contextOwned := old == nil
		if needContext {
			if old != nil {
				next.context = old.context
			} else {
				next.context = &runtime.ContextUsageProjection{}
			}
		}
		appendEvents := func(events []*session.Event) {
			canonical := false
			for _, event := range events {
				canonical = canonical || session.IsCanonicalHistoryEvent(event)
				if event != nil && event.ContextUsage != nil || session.UsageSnapshotFromSessionEvent(event) != nil || session.IsModelInvocationReceipt(event) {
					next.history = nil
				}
				fact := session.ProjectUsageEvent(event)
				if fact != nil {
					next.facts = append(next.facts, fact)
					data, _ := json.Marshal(fact)
					next.bytes += len(data)
				}
			}
			if next.context != nil && canonical {
				if !contextOwned {
					next.context = next.context.Clone()
					contextOwned = true
				}
				next.context.Append(events)
			}
		}
		if hasPages && hasCuts {
			for next.seq < cut.ThroughSeq {
				page, err := paged.EventsPage(ctx, session.EventPageRequest{SessionRef: ref, AfterSeq: next.seq, ThroughSeq: cut.ThroughSeq, Visibility: session.EventPageAllDurable})
				if err != nil {
					return sessionUsageView{}, err
				}
				if page.NextSeq <= next.seq || page.NextSeq > cut.ThroughSeq {
					return sessionUsageView{}, fmt.Errorf("read status usage: cursor did not advance within durable cut")
				}
				appendEvents(page.Events)
				next.seq = page.NextSeq
			}
		} else {
			// Stores without immutable forward cuts retain the complete legacy
			// read. Never cache an unversioned result as though it were current.
			events, err := sessionUsageEvents(ctx, reader, ref)
			if err != nil {
				return sessionUsageView{}, err
			}
			appendEvents(events)
		}
	}
	// Retain only the revisioned usage projection, never the complete Session
	// state. Unchanged metadata needs no second state read or verification cut.
	next.revision, next.stateUsage = cut.Session.Revision, stateUsage
	provider, model := d.currentContextUsageIdentity(ctx, cut.Session)
	key := usageHistoryKey{category, string(cut.Session.Controller.Kind), cut.Session.Controller.EpochID, cut.Session.Controller.ControllerID, provider, model}
	history, found := next.history[key]
	if !found {
		filtered := filterMainContextUsage(cut.Session, next.facts, provider, model)
		history = sessionTokenUsageBreakdownFromEvents(filtered, category)
		// Model/controller changes replace the small projection, without
		// rereading durable history or accumulating unbounded identity keys.
		next.history = map[usageHistoryKey]sessionTokenUsageBreakdown{key: history}
	}
	breakdown := history
	breakdown.ByModel = maps.Clone(history.ByModel)
	breakdown.addBreakdown(stateUsage)
	if err := ctx.Err(); err != nil {
		return sessionUsageView{}, err
	}
	if hasPages && hasCuts {
		m.entries[ref] = next
		m.clock++
		m.used[ref] = m.clock
		m.evict()
	}
	return sessionUsageView{active: cut.Session, events: next.facts, context: next.context, breakdown: breakdown}, nil
}

func (m *UsageReadModel) evict() {
	total := 0
	for _, entry := range m.entries {
		total += entry.bytes
		if entry.context != nil {
			total += entry.bytes
		}
	}
	for len(m.entries) > maxUsageReadSessions || total > maxUsageReadBytes {
		var oldest session.SessionRef
		var clock uint64
		for ref, used := range m.used {
			if clock == 0 || used < clock {
				oldest, clock = ref, used
			}
		}
		entry := m.entries[oldest]
		total -= entry.bytes
		if entry.context != nil {
			total -= entry.bytes
		}
		delete(m.entries, oldest)
		delete(m.used, oldest)
	}
}
