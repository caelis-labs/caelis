package configstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdmissionSnapshotSerializesCrossInstanceRevocation(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	saved, err := store.CompareAndSave(t.Context(), 0, AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	current, release, err := store.ReadForAdmission(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := New(root).CompareAndSave(ctx, current.ConfigurationRevision, current); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writer passed unconsumed admission boundary: %v", err)
	}
	release()
	release = nil
	if _, err := New(root).CompareAndSave(t.Context(), saved.ConfigurationRevision, saved); err != nil {
		t.Fatal(err)
	}
}
