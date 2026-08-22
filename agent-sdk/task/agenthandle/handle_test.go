package agenthandle

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"@Ava", "ava"},
		{"  @Kai  ", "kai"},
		{"self", "self"},
	}
	for _, tc := range tests {
		if got := Normalize(tc.in); got != tc.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeBase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Anthropic/Claude Agent", "anthropic-claude-agent"},
		{"!!!", ""},
		{"self", "self"},
	}
	for _, tc := range tests {
		if got := NormalizeBase(tc.in); got != tc.want {
			t.Fatalf("NormalizeBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalRequested(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "", want: ""},
		{in: " @Reviewer ", want: "reviewer"},
		{in: "coder-1", want: "coder-1"},
		{in: "parent", wantErr: "reserved"},
		{in: "rev iew", wantErr: "must start with a letter"},
		{in: "reviewer,helper", wantErr: "must start with a letter"},
		{in: strings.Repeat("a", MaxRequestedHandleLength+1), wantErr: "exceeds 32 characters"},
	}
	for _, tc := range tests {
		got, err := CanonicalRequested(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CanonicalRequested(%q) error = %v, want %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("CanonicalRequested(%q) = %q, %v, want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestAllocatePrefersPoolNames(t *testing.T) {
	if got := Allocate(nil, "codex"); !ContainsPoolName(got) {
		t.Fatalf("Allocate(nil, codex) = %q, want pool name", got)
	}
	if got := Allocate(map[string]struct{}{}, "Anthropic/Claude Agent"); !ContainsPoolName(got) {
		t.Fatalf("Allocate(empty, agent) = %q, want pool name", got)
	}
	if got := Allocate(map[string]struct{}{}, "!!!"); !ContainsPoolName(got) {
		t.Fatalf("Allocate(empty, invalid) = %q, want pool name", got)
	}
	if got := Allocate(map[string]struct{}{}, "self"); !ContainsPoolName(got) {
		t.Fatalf("Allocate(empty, self) = %q, want pool name", got)
	}
	usedSelfHandle := map[string]struct{}{"jeff": {}}
	if got := Allocate(usedSelfHandle, "self"); got == "jeff" || !ContainsPoolName(got) {
		t.Fatalf("Allocate(used, self) = %q, want unused pool name", got)
	}
}

func TestAllocateAvoidsUsedHandles(t *testing.T) {
	used := map[string]struct{}{}
	first := Allocate(used, "helper")
	if first == "" || !ContainsPoolName(first) {
		t.Fatalf("first handle = %q, want pool name", first)
	}
	used[first] = struct{}{}
	second := Allocate(used, "helper")
	if second == "" || !ContainsPoolName(second) || second == first {
		t.Fatalf("second handle = %q, want different pool name than %q", second, first)
	}
}

func TestAllocateFallsBackAfterPoolExhausted(t *testing.T) {
	used := map[string]struct{}{}
	for _, candidate := range namePool {
		used[Normalize(candidate)] = struct{}{}
	}
	got := Allocate(used, "anthropic/claude")
	wantBase := NormalizeBase("anthropic/claude")
	if got != wantBase {
		t.Fatalf("Allocate(exhausted pool) = %q, want %q", got, wantBase)
	}
}
