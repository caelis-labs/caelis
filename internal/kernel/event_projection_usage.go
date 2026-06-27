package kernel

import (
	gatewayapi "github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func usageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
	return gatewayapi.UsageSnapshotFromSessionEvent(event)
}

// UsageSnapshotFromSessionEvent projects provider token usage from a durable
// session event into the canonical gateway usage contract.
func UsageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
	return gatewayapi.UsageSnapshotFromSessionEvent(event)
}
