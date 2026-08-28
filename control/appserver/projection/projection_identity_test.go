package projection

import "testing"

func TestFormatProjectionIDTrimsEventIDAndKeepsSiblingIndex(t *testing.T) {
	t.Parallel()

	if got, want := formatProjectionID(" assistant-final ", 2), "acp-projection:YXNzaXN0YW50LWZpbmFs:2"; got != want {
		t.Fatalf("formatProjectionID() = %q, want %q", got, want)
	}
}
