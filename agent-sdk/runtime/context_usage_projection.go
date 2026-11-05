package runtime

import (
	"maps"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ContextUsageProjection retains context metadata and exact local token
// estimates for an append-only canonical history. It uses the same checkpoint,
// provider baseline and fallback calculation as ComputeUsageSnapshotForModel,
// without retaining transcript payloads. Rebuild it when its source is replaced.
// The owner serializes Append and Snapshot; the projection owns no durable truth.
type ContextUsageProjection struct {
	events []*session.Event
	tokens map[uint64]int
	seq    uint64
}

// Clone returns an independently appendable projection. Published snapshots
// remain immutable while another reader catches up to a newer durable cut.
func (p *ContextUsageProjection) Clone() *ContextUsageProjection {
	if p == nil {
		return &ContextUsageProjection{}
	}
	return &ContextUsageProjection{events: append([]*session.Event(nil), p.events...), tokens: maps.Clone(p.tokens), seq: p.seq}
}

// Append consumes a durable source prefix once. Repeated source sequences are
// ignored; callers must supply positive, monotonically increasing durable Seq.
func (p *ContextUsageProjection) Append(events []*session.Event) {
	if p.tokens == nil {
		p.tokens = map[uint64]int{}
	}
	for _, event := range events {
		if event == nil || event.Seq <= p.seq {
			continue
		}
		p.seq = event.Seq
		if !session.IsCanonicalHistoryEvent(event) {
			continue
		}
		p.tokens[event.Seq] = estimatePromptEventTokens(event)
		p.events = append(p.events, session.ProjectUsageEvent(event))
	}
}

// Snapshot resolves the current model's context gauge from derived metadata.
// Changing model/window/prefix inputs reuses the source estimates, not a stale
// result for the previously selected model.
func (p *ContextUsageProjection) Snapshot(contextWindow int, cfg CompactionConfig, provider, modelName string) compact.UsageSnapshot {
	return computeUsageSnapshotForModel(p.events, nil, contextWindow, cfg, provider, modelName, func(event *session.Event) int {
		if event != nil && event.Seq > 0 {
			return p.tokens[event.Seq]
		}
		// Compaction injects a small replacement event without a source Seq.
		return estimatePromptEventTokens(event)
	})
}
