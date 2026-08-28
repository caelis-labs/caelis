package appserver

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestValidateEnvelopeDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     eventstream.Envelope
		wantErr string
	}{
		{name: "unstamped transient", env: eventstream.Envelope{}},
		{name: "explicit transient", env: eventstream.Envelope{Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}}},
		{
			name: "positioned canonical",
			env: eventstream.Envelope{
				Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
				Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 1}},
			},
		},
		{
			name: "zero durable sequence",
			env: eventstream.Envelope{
				Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
				Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{}},
			},
			wantErr: "durable envelope position sequence must be greater than zero",
		},
		{
			name:    "unpositioned mirror",
			env:     eventstream.Envelope{Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryMirror}},
			wantErr: "durable envelope requires a durable position",
		},
		{
			name: "mixed durable position",
			env: eventstream.Envelope{
				Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
				Position: &eventstream.FeedPosition{
					Durable:   &eventstream.DurableFeedPosition{Seq: 1},
					Transient: &eventstream.TransientFeedPosition{Generation: "generation-1", Sequence: 1},
				},
			},
			wantErr: "invalid durable position",
		},
		{
			name:    "unknown mode",
			env:     eventstream.Envelope{Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryMode("best_effort")}},
			wantErr: "unsupported delivery mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvelopeDelivery(tt.env)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateEnvelopeDelivery() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ValidateEnvelopeDelivery() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
