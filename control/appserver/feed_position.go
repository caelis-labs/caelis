package appserver

import "github.com/caelis-labs/caelis/protocol/acp/eventstream"

func durableAnchor(position eventstream.FeedPosition) eventstream.DurableFeedPosition {
	if position.Durable != nil {
		return *position.Durable
	}
	if position.Transient != nil {
		return position.Transient.Anchor
	}
	return eventstream.DurableFeedPosition{}
}

func compareDurablePosition(left, right eventstream.DurableFeedPosition) int {
	switch {
	case left.Seq < right.Seq:
		return -1
	case left.Seq > right.Seq:
		return 1
	case left.ProjectionIndex < right.ProjectionIndex:
		return -1
	case left.ProjectionIndex > right.ProjectionIndex:
		return 1
	default:
		return 0
	}
}
