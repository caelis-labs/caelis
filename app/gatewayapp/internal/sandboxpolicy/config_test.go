package sandboxpolicy

import "testing"

func TestNormalizeBackendAcceptsWindowsElevatedAliases(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"windows", "windows-elevated", "windows_elevated", "windows elevated", "elevated"} {
		got, err := NormalizeBackend(input)
		if err != nil {
			t.Fatalf("NormalizeBackend(%q) error = %v", input, err)
		}
		if got != "windows-elevated" {
			t.Fatalf("NormalizeBackend(%q) = %q, want windows-elevated", input, got)
		}
	}
}
