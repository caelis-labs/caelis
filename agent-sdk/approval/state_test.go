package approval

import "testing"

func TestCurrentModeNormalizesSessionOverride(t *testing.T) {
	tests := []struct {
		name  string
		state map[string]any
		want  Mode
	}{
		{name: "approval mode key normalizes", state: map[string]any{StateCurrentApprovalMode: "auto_review"}, want: ModeAutoReview},
		{name: "unknown explicit mode defaults to auto-review", state: map[string]any{StateCurrentApprovalMode: "unknown"}, want: ModeAutoReview},
		{name: "manual override", state: map[string]any{StateCurrentApprovalMode: "manual"}, want: ModeManual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CurrentMode(tt.state); got != tt.want {
				t.Fatalf("CurrentMode(%#v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestCurrentModeOrDefaultUsesFallbackOnlyWithoutOverride(t *testing.T) {
	tests := []struct {
		name  string
		state map[string]any
		want  Mode
	}{
		{name: "empty state uses fallback", state: nil, want: ModeManual},
		{name: "explicit override beats fallback", state: map[string]any{StateCurrentApprovalMode: "manual"}, want: ModeManual},
		{name: "unknown explicit mode defaults to auto-review", state: map[string]any{StateCurrentApprovalMode: "unknown"}, want: ModeAutoReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CurrentModeOrDefault(tt.state, ModeManual); got != tt.want {
				t.Fatalf("CurrentModeOrDefault(%#v, manual) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}
