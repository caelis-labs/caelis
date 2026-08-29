package subagent

import (
	"errors"
	"testing"
)

func TestSpawnNotStartedMarkerPreservesCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("setup failed")
	marked := MarkSpawnNotStarted(cause)
	if !SpawnProvenNotStarted(marked) || !errors.Is(marked, cause) || marked.Error() != cause.Error() {
		t.Fatalf("marked error = %v, want positive proof preserving cause", marked)
	}
	if SpawnProvenNotStarted(cause) {
		t.Fatal("ordinary error unexpectedly proved Spawn was not started")
	}
	if joined := errors.Join(errors.New("cleanup context"), marked); !SpawnProvenNotStarted(joined) {
		t.Fatalf("joined error = %v, want marker discovery through the error tree", joined)
	}
	if MarkSpawnNotStarted(nil) != nil {
		t.Fatal("MarkSpawnNotStarted(nil) returned a non-nil error")
	}
}
