package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

func TestRetentionReclaimsAcrossSessionsWithSlowReaders(t *testing.T) {
	store := newTestStore(t, Config{MaxBytes: 64 << 10, MaxStreamBytes: 12 << 10, SegmentBytes: 2 << 10, PartitionAllocationCharge: 128, SegmentAllocationCharge: 64})
	const sessions, children = 8, 16
	writers := make([]streamspool.Writer, 0, sessions*children)
	for s := range sessions {
		for child := range children {
			writers = append(writers, registerTestWriter(t, store, fmt.Sprint(s), fmt.Sprint(child), true))
		}
	}
	slow := writers[0]
	if _, err := slow.Append(t.Context(), 1, time.Now(), make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Reader(t.Context(), slow.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// Allocate a descriptor too: an unread lease and an open file must both
	// release physical disk space under pressure.
	if _, err := reader.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := slow.Append(t.Context(), 1, time.Now(), make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, len(writers))
	for _, w := range writers[1:] {
		wg.Go(func() {
			for range 32 {
				if _, err := w.Append(t.Context(), 1, time.Now(), make([]byte, 1024)); err != nil {
					errorsCh <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if _, err := reader.Next(t.Context()); !errors.Is(err, streamspool.ErrExpired) {
		t.Fatalf("slow reader = %v", err)
	}
	// An idle producer whose whole cache was reclaimed remains writable.
	if _, err := slow.Append(t.Context(), 1, time.Now(), []byte("approval and terminal still arrive")); err != nil {
		t.Fatal(err)
	}
	bounds, err := slow.Bounds(t.Context())
	if err != nil || bounds.State != streamspool.StateOpen || bounds.High != 3 {
		t.Fatalf("resumed producer bounds=%+v err=%v", bounds, err)
	}
	store.mu.Lock()
	used, physical := store.usedBytes, store.physicalParts
	store.mu.Unlock()
	if used > store.cfg.MaxBytes || physical >= len(writers) {
		t.Fatalf("accounting: bytes=%d physical=%d", used, physical)
	}
}

func TestRetentionReaderContinuesAtRetainedBoundary(t *testing.T) {
	store := newTestStore(t, Config{MaxBytes: 4096, MaxStreamBytes: 4096, SegmentBytes: 512, PartitionAllocationCharge: 128, SegmentAllocationCharge: 64})
	w := registerTestWriter(t, store, "session", "task", true)
	r, err := store.Reader(t.Context(), w.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for n := range 200 {
		text := fmt.Sprintf("%03d%s", n, string(make([]byte, 128)))
		if _, err := w.Append(t.Context(), 1, time.Now(), []byte(text)); err != nil {
			t.Fatal(err)
		}
		got, err := r.Next(t.Context())
		if err != nil || string(got.Payload) != text || got.Offset != streamspool.Offset(n) {
			t.Fatalf("record=%+v err=%v", got, err)
		}
	}
}

func TestRetentionConcurrentTerminalRemovalDoesNotPoisonLiveWriter(t *testing.T) {
	store := newTestStore(t, Config{MaxBytes: 8 << 10, MaxStreamBytes: 4 << 10, SegmentBytes: 512, PartitionAllocationCharge: 128, SegmentAllocationCharge: 64})
	live := registerTestWriter(t, store, "live", "task", true)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 9)
	for worker := range 8 {
		wg.Go(func() {
			for n := range 32 {
				logical := streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("ephemeral", fmt.Sprint(worker, "-", n))}
				w, err := store.Register(t.Context(), logical, streamspool.WriterOptions{OriginComplete: true})
				if err == nil {
					_, err = w.Append(t.Context(), 1, time.Now(), make([]byte, 512))
				}
				if err == nil {
					err = w.Seal(t.Context())
				}
				if err == nil {
					err = store.Remove(t.Context(), w.Key())
				}
				if err != nil && !errors.Is(err, streamspool.ErrNotFound) {
					errorsCh <- err
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 256 {
			if _, err := live.Append(t.Context(), 1, time.Now(), make([]byte, 512)); err != nil {
				errorsCh <- err
				return
			}
		}
	})
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
}

// The small-budget tests exercise the same boundaries on every run. This
// opt-in also checks the production allocation sizes and actual file footprint.
func TestRetentionDefaultBudgetAcrossSessions(t *testing.T) {
	if os.Getenv("CAELIS_SPOOL_STRESS") != "1" {
		t.Skip("set CAELIS_SPOOL_STRESS=1 for the 1.5 GiB file-spool workload")
	}
	store := newTestStore(t, Config{})
	const sessions, children, records = 8, 16, 12
	writers := make([]streamspool.Writer, 0, sessions*children)
	for s := range sessions {
		for child := range children {
			writers = append(writers, registerTestWriter(t, store, fmt.Sprint(s), fmt.Sprint(child), true))
		}
	}
	payload := make([]byte, 1<<20)
	var wg sync.WaitGroup
	errorsCh := make(chan error, len(writers))
	for _, w := range writers {
		wg.Go(func() {
			for range records {
				if _, err := w.Append(t.Context(), 1, time.Now(), payload); err != nil {
					errorsCh <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	reclaimed := 0
	for _, w := range writers {
		bounds, err := w.Bounds(t.Context())
		if err != nil || bounds.State != streamspool.StateOpen || bounds.High != records {
			t.Fatalf("producer lost after pressure: %+v, %v", bounds, err)
		}
		if bounds.Low == 0 {
			continue
		}
		reclaimed++
		const latest = "approval and final remain observable"
		offset, err := w.Append(t.Context(), 1, time.Now(), []byte(latest))
		if err != nil {
			t.Fatal(err)
		}
		r, err := store.Reader(t.Context(), w.Key(), offset)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Next(t.Context())
		r.Close()
		if err != nil || string(got.Payload) != latest {
			t.Fatalf("reclaimed producer tail: %v", err)
		}
	}
	var physical int64
	if err := filepath.WalkDir(store.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			physical += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	used := store.usedBytes
	store.mu.Unlock()
	if reclaimed == 0 || used > store.cfg.MaxBytes || physical > used {
		t.Fatalf("reclaimed=%d accounted=%d physical=%d", reclaimed, used, physical)
	}
	t.Logf("%d Sessions, %d children, %d MiB appended; %d windows reclaimed; physical=%d, accounted=%d, limit=%d bytes", sessions, len(writers), sessions*children*records, reclaimed, physical, used, store.cfg.MaxBytes)
}
