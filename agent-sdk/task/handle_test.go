package task

import (
	"strings"
	"testing"
)

func TestNormalizeHandleVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"@Ava", "ava"},
		{"  @Kai  ", "kai"},
		{"self", "self"},
	}
	for _, tc := range tests {
		if got := NormalizeHandle(tc.in); got != tc.want {
			t.Fatalf("NormalizeHandle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeHandleBase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"Anthropic/Claude Agent", "anthropic-claude-agent"},
		{"!!!", ""},
		{"self", "self"},
	}
	for _, tc := range tests {
		if got := NormalizeHandleBase(tc.in); got != tc.want {
			t.Fatalf("NormalizeHandleBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalRequestedHandle(t *testing.T) {
	t.Parallel()
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
		got, err := CanonicalRequestedHandle(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CanonicalRequestedHandle(%q) error = %v, want %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("CanonicalRequestedHandle(%q) = %q, %v, want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestAllocateHandlePrefersPoolNames(t *testing.T) {
	t.Parallel()
	if got := AllocateHandle(nil, "codex"); !ContainsHandlePoolName(got) {
		t.Fatalf("AllocateHandle(nil, codex) = %q, want pool name", got)
	}
	if got := AllocateHandle(map[string]struct{}{}, "Anthropic/Claude Agent"); !ContainsHandlePoolName(got) {
		t.Fatalf("AllocateHandle(empty, agent) = %q, want pool name", got)
	}
	if got := AllocateHandle(map[string]struct{}{}, "!!!"); !ContainsHandlePoolName(got) {
		t.Fatalf("AllocateHandle(empty, invalid) = %q, want pool name", got)
	}
	if got := AllocateHandle(map[string]struct{}{}, "self"); !ContainsHandlePoolName(got) {
		t.Fatalf("AllocateHandle(empty, self) = %q, want pool name", got)
	}
	usedSelfHandle := map[string]struct{}{"jeff": {}}
	if got := AllocateHandle(usedSelfHandle, "self"); got == "jeff" || !ContainsHandlePoolName(got) {
		t.Fatalf("AllocateHandle(used, self) = %q, want unused pool name", got)
	}
}

func TestAllocateHandleAvoidsUsedHandles(t *testing.T) {
	t.Parallel()
	used := map[string]struct{}{}
	first := AllocateHandle(used, "helper")
	if first == "" || !ContainsHandlePoolName(first) {
		t.Fatalf("first handle = %q, want pool name", first)
	}
	used[first] = struct{}{}
	second := AllocateHandle(used, "helper")
	if second == "" || !ContainsHandlePoolName(second) || second == first {
		t.Fatalf("second handle = %q, want different pool name than %q", second, first)
	}
}

func TestAllocateHandleFallsBackAfterPoolExhausted(t *testing.T) {
	t.Parallel()
	used := map[string]struct{}{}
	for _, candidate := range handleNamePool {
		used[NormalizeHandle(candidate)] = struct{}{}
	}
	got := AllocateHandle(used, "anthropic/claude")
	wantBase := NormalizeHandleBase("anthropic/claude")
	if got != wantBase {
		t.Fatalf("AllocateHandle(exhausted pool) = %q, want %q", got, wantBase)
	}
}
