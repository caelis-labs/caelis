package kernel

import "testing"

func TestSessionRunningUsesOnlyLiveHandlesAndApprovals(t *testing.T) {
	for _, tt := range []struct {
		name     string
		handle   *turnHandle
		approval bool
		want     bool
	}{
		{name: "dormant"},
		{name: "active", handle: &turnHandle{}, want: true},
		{name: "finished", handle: &turnHandle{finished: true}},
		{name: "closed", handle: &turnHandle{closed: true}},
		{name: "approval without handle", approval: true, want: true},
		{name: "approval after handle finishes", handle: &turnHandle{finished: true}, approval: true, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := &approvalCoordinator{}
			if tt.approval {
				coordinator.active = []*pendingApproval{{}}
			}
			if tt.handle != nil {
				tt.handle.approvals = coordinator
			}
			// No Runtime or Store: querying either would panic.
			gateway := &Gateway{
				active:    map[string]*turnHandle{"session": tt.handle},
				approvals: map[string]*approvalCoordinator{"session": coordinator},
			}
			if got := gateway.SessionRunning("session"); got != tt.want {
				t.Fatalf("SessionRunning() = %v, want %v", got, tt.want)
			}
			if gateway.SessionRunning("other-session") {
				t.Fatal("activity leaked to another Session")
			}
		})
	}
}
