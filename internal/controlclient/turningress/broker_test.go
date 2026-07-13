package turningress

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestTurnIdentityMatchesKnownIDs(t *testing.T) {
	t.Parallel()

	identity := turnIdentity{handleID: "handle-1", runID: "run-1", turnID: "turn-1"}
	for _, tt := range []struct {
		name string
		env  eventstream.Envelope
		want bool
	}{
		{
			name: "all identifiers match",
			env:  eventstream.Envelope{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
			want: true,
		},
		{
			name: "source omits identifiers",
			env:  eventstream.Envelope{},
			want: true,
		},
		{
			name: "foreign handle",
			env:  eventstream.Envelope{HandleID: "other", RunID: "run-1", TurnID: "turn-1"},
			want: false,
		},
		{
			name: "foreign run",
			env:  eventstream.Envelope{HandleID: "handle-1", RunID: "other", TurnID: "turn-1"},
			want: false,
		},
		{
			name: "foreign turn",
			env:  eventstream.Envelope{HandleID: "handle-1", RunID: "run-1", TurnID: "other"},
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := identity.matches(tt.env); got != tt.want {
				t.Fatalf("identity.matches(%#v) = %t, want %t", tt.env, got, tt.want)
			}
		})
	}
}
