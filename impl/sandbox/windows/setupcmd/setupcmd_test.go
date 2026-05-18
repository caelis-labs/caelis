package setupcmd

import "testing"

func TestNormalizeArgsAcceptsInternalSentinel(t *testing.T) {
	got := normalizeArgs([]string{internalSetupHelperCommand, "payload"})
	if len(got) != 1 || got[0] != "payload" {
		t.Fatalf("normalizeArgs() = %#v, want payload only", got)
	}
}

func TestNormalizeArgsLeavesDirectPayload(t *testing.T) {
	got := normalizeArgs([]string{"payload"})
	if len(got) != 1 || got[0] != "payload" {
		t.Fatalf("normalizeArgs() = %#v, want direct payload", got)
	}
}
