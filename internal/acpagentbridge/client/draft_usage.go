package client

import (
	"encoding/json"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

// The draft patches cost independently of the context gauge. Materialize it
// here so the shared live and durable owners keep receiving complete snapshots.
func (d *draftClient) decodeUsage(sessionID string, raw json.RawMessage) (UsageUpdate, error) {
	var usage v2.SessionUsageUpdate
	if err := json.Unmarshal(raw, &usage); err != nil {
		return UsageUpdate{}, err
	}
	update, err := draftConvert[UsageUpdate](usage)
	if err != nil {
		return UsageUpdate{}, err
	}
	var cost json.RawMessage
	if usage.CostState() == v2.NullableFieldValue {
		cost, err = json.Marshal(usage.Cost)
		if err != nil {
			return UsageUpdate{}, err
		}
	}
	d.mu.Lock()
	session := d.sessionLocked(sessionID)
	if usage.CostState() != v2.NullableFieldAbsent {
		if err := d.reserveCacheLocked(session, len(cost), session.costCacheBytes); err != nil {
			d.mu.Unlock()
			return UsageUpdate{}, err
		}
		session.cost, session.costCacheBytes = cost, len(cost)
	}
	cost = session.cost
	d.mu.Unlock()
	// Decoding a fresh value also detaches opaque metadata from the cache.
	if len(cost) != 0 {
		if err := json.Unmarshal(cost, &update.Cost); err != nil {
			return UsageUpdate{}, err
		}
	}
	return update, nil
}
