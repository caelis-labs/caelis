package eventstream

import (
	"fmt"
	"strings"
)

// DurableFeedPosition identifies one projected Envelope within one durable
// Session event sequence.
type DurableFeedPosition struct {
	Seq             uint64 `json:"seq"`
	ProjectionIndex uint32 `json:"projection_index"`
}

// TransientFeedPosition identifies one process-local Envelope relative to the
// latest durable anchor observed by the broker.
type TransientFeedPosition struct {
	Anchor     DurableFeedPosition `json:"anchor"`
	Generation string              `json:"generation"`
	Sequence   uint64              `json:"sequence"`
}

// FeedPosition is one exclusive durable or transient position.
type FeedPosition struct {
	Durable   *DurableFeedPosition   `json:"durable,omitempty"`
	Transient *TransientFeedPosition `json:"transient,omitempty"`
}

// Validate checks that one feed position selects exactly one valid lane.
func (p FeedPosition) Validate() error {
	switch {
	case p.Durable != nil && p.Transient != nil:
		return fmt.Errorf("eventstream: feed position has multiple lanes")
	case p.Durable != nil:
		return nil
	case p.Transient != nil:
		if strings.TrimSpace(p.Transient.Generation) == "" || p.Transient.Sequence == 0 {
			return fmt.Errorf("eventstream: transient feed position is incomplete")
		}
		return nil
	default:
		return fmt.Errorf("eventstream: feed position is empty")
	}
}

// CloneFeedPosition returns an isolated position copy.
func CloneFeedPosition(in *FeedPosition) *FeedPosition {
	if in == nil {
		return nil
	}
	out := *in
	if in.Durable != nil {
		durable := *in.Durable
		out.Durable = &durable
	}
	if in.Transient != nil {
		transient := *in.Transient
		out.Transient = &transient
	}
	return &out
}
