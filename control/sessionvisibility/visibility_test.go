package sessionvisibility

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestSystemManagedSessionClassification(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		metadata map[string]any
		want     bool
	}{
		{name: "nil metadata"},
		{name: "blank marker", metadata: map[string]any{MetadataSystemManagedAgent: "  "}},
		{name: "wrong marker type", metadata: map[string]any{MetadataSystemManagedAgent: true}},
		{name: "subagent", metadata: map[string]any{MetadataSystemManagedAgent: "subagent"}, want: true},
		{name: "guardian", metadata: map[string]any{MetadataSystemManagedAgent: "guardian"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSystemManagedMetadata(test.metadata); got != test.want {
				t.Fatalf("IsSystemManagedMetadata() = %v, want %v", got, test.want)
			}
			if got := IsSystemManagedSession(session.Session{Metadata: test.metadata}); got != test.want {
				t.Fatalf("IsSystemManagedSession() = %v, want %v", got, test.want)
			}
			if got := IsSystemManagedSummary(session.SessionSummary{Metadata: test.metadata}); got != test.want {
				t.Fatalf("IsSystemManagedSummary() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsSpawnedSubagentSessionRequiresExactManagedClass(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		metadata map[string]any
		want     bool
	}{
		{name: "subagent", metadata: map[string]any{MetadataSystemManagedAgent: "subagent"}, want: true},
		{name: "subagent normalized", metadata: map[string]any{MetadataSystemManagedAgent: " SubAgent "}, want: true},
		{name: "guardian", metadata: map[string]any{MetadataSystemManagedAgent: "guardian"}},
		{name: "ordinary", metadata: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSpawnedSubagentSession(session.Session{Metadata: test.metadata}); got != test.want {
				t.Fatalf("IsSpawnedSubagentSession() = %v, want %v", got, test.want)
			}
		})
	}
}
