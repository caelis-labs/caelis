package gateway

import "testing"

func TestCurrentSessionModeNormalizesCompatibilityValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  string
	}{
		{name: "empty defaults to auto-review", state: map[string]any{}, want: string(ApprovalModeAutoReview)},
		{name: "approval mode key normalizes", state: map[string]any{StateCurrentApprovalMode: "auto_review"}, want: string(ApprovalModeAutoReview)},
		{name: "unknown explicit mode defaults to auto-review", state: map[string]any{StateCurrentApprovalMode: "unknown"}, want: string(ApprovalModeAutoReview)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CurrentSessionMode(tt.state); got != tt.want {
				t.Fatalf("CurrentSessionMode(%#v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestCurrentSessionModeOrDefaultNormalizesFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		state    map[string]any
		fallback string
		want     string
	}{
		{name: "fallback normalizes", state: map[string]any{}, fallback: "auto_review", want: string(ApprovalModeAutoReview)},
		{name: "explicit override beats fallback", state: map[string]any{StateCurrentApprovalMode: "manual"}, fallback: "auto_review", want: string(ApprovalModeManual)},
		{name: "unknown explicit mode defaults to auto-review", state: map[string]any{StateCurrentApprovalMode: "unknown"}, fallback: "manual", want: string(ApprovalModeAutoReview)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CurrentSessionModeOrDefault(tt.state, tt.fallback); got != tt.want {
				t.Fatalf("CurrentSessionModeOrDefault(%#v, %q) = %q, want %q", tt.state, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestUnsupportedLegacyStateKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  string
	}{
		{name: "none", state: map[string]any{StateCurrentApprovalMode: "manual"}, want: ""},
		{name: "legacy session mode", state: map[string]any{"gateway.current_session_mode": "manual"}, want: "gateway.current_session_mode"},
		{name: "legacy sandbox mode", state: map[string]any{"gateway.current_sandbox_mode": "workspace-write"}, want: "gateway.current_sandbox_mode"},
		{name: "empty legacy value ignored", state: map[string]any{"gateway.current_session_mode": " "}, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := UnsupportedLegacyStateKey(tt.state); got != tt.want {
				t.Fatalf("UnsupportedLegacyStateKey(%#v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestCurrentPolicyProfileNormalizesCompatibilityValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state map[string]any
		want  string
	}{
		{name: "workspace write alias normalizes", state: map[string]any{StateCurrentPolicyProfile: "workspace_write"}, want: "workspace-write"},
		{name: "legacy approval profile clears", state: map[string]any{StateCurrentPolicyProfile: "auto-review"}, want: ""},
		{name: "custom profile preserved", state: map[string]any{StateCurrentPolicyProfile: "strict-local"}, want: "strict-local"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CurrentPolicyProfile(tt.state); got != tt.want {
				t.Fatalf("CurrentPolicyProfile(%#v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}
