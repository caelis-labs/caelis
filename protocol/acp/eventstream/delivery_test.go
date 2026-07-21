package eventstream

import (
	"strings"
	"testing"
)

func TestValidateEnvelopeDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     Envelope
		wantErr string
	}{
		{name: "unstamped transient", env: Envelope{}},
		{name: "explicit transient", env: Envelope{Delivery: &Delivery{Mode: DeliveryTransient}}},
		{
			name: "positioned canonical",
			env: Envelope{
				Delivery: &Delivery{Mode: DeliveryCanonical},
				Position: &FeedPosition{Durable: &DurableFeedPosition{Seq: 1}},
			},
		},
		{
			name: "zero durable sequence",
			env: Envelope{
				Delivery: &Delivery{Mode: DeliveryCanonical},
				Position: &FeedPosition{Durable: &DurableFeedPosition{}},
			},
			wantErr: "durable envelope position sequence must be greater than zero",
		},
		{
			name:    "unpositioned mirror",
			env:     Envelope{Delivery: &Delivery{Mode: DeliveryMirror}},
			wantErr: "durable envelope requires a durable position",
		},
		{
			name: "mixed durable position",
			env: Envelope{
				Delivery: &Delivery{Mode: DeliveryCanonical},
				Position: &FeedPosition{
					Durable:   &DurableFeedPosition{Seq: 1},
					Transient: &TransientFeedPosition{Generation: "generation-1", Sequence: 1},
				},
			},
			wantErr: "invalid durable position",
		},
		{
			name:    "unknown mode",
			env:     Envelope{Delivery: &Delivery{Mode: DeliveryMode("best_effort")}},
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
