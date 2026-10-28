package appserveradapter

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
)

func TestInterruptAdmissionSettlesBeforeCancelReturns(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		interrupted bool
	}{
		{name: "cancelled", err: context.Canceled, interrupted: true},
		{name: "failed", err: errors.New("admission failed")},
		{name: "unknown", err: appserver.NewOutcomeError(appserver.OutcomeUnknown, context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newSessionClientAdapterForTest(t, &sessionClientAdapterTestClient{}, &sessionClientAdapterTestParticipantClient{}, "s", "cli-tui")
			t.Cleanup(func() { _ = adapter.Close() })
			id, ctx := adapter.markTurnAdmission(t.Context())
			admission := adapter.admissions[id]
			cancel := admission.cancel
			// Force the admission worker to finish before Interrupt reads its
			// completion state, instead of relying on goroutine scheduling.
			admission.cancel = func() {
				adapter.activeMu.Lock()
				admission.cancel = cancel
				adapter.activeMu.Unlock()
				cancel()
				adapter.finishTurnAdmission(id, tc.err)
			}
			err := adapter.InterruptSession(t.Context(), "s")
			if tc.interrupted {
				if err != nil {
					t.Fatalf("successful interruption reported its own cancellation: %v", err)
				}
			} else if !errors.Is(err, tc.err) {
				t.Fatalf("Interrupt = %v, want admission error %v", err, tc.err)
			}
			if ctx.Err() != context.Canceled || len(adapter.admissions) != 0 {
				t.Fatal("interruption did not finish the matching admission")
			}
		})
	}
}
