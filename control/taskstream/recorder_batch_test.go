package taskstream

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
)

func TestRecorderBatchesDiskWritesWithoutRewritingHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	defer recorder.Close(context.Background())
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "s", TaskID: "t", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	const count = 2048
	start := time.Now()
	for index := range count {
		if err := observer.ObserveTaskOutput(ctx, output.Event{Text: fmt.Sprintf("delta-%04d", index), Running: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: "s", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	calls, bytes := spool.WriteStats()
	if calls >= count/8 {
		t.Fatalf("too many writes: %d for %d records", calls, count)
	}
	t.Logf("%d deltas: %d file Write calls, %d bytes, elapsed %s", count, calls, bytes, time.Since(start))
	// A second turn appends its new record only; it never serializes the old history.
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "next turn", Running: true}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: "s", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	nextCalls, nextBytes := spool.WriteStats()
	if nextCalls != calls+1 || nextBytes-bytes > 1024 {
		t.Fatalf("history rewritten: calls +%d bytes +%d", nextCalls-calls, nextBytes-bytes)
	}
	if err := recorder.ReleaseTask(ctx, task.Ref{SessionID: "s", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	closedCalls, closedBytes := spool.WriteStats()
	if closedCalls != nextCalls || closedBytes != nextBytes {
		t.Fatal("sealing rewrote cache")
	}
}

func TestRecorderIdleTailBecomesVisibleWithinFlushWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	defer recorder.Close(context.Background())
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "s", TaskID: "t", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	key, _, err := spool.Resolve(ctx, streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("s", "t")})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := spool.Reader(ctx, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	start := time.Now()
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "one delta", Running: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 250*time.Millisecond {
		t.Fatalf("tail stalled: %s", elapsed)
	}
	t.Logf("single idle-tail event visible after %s (flush window %s)", elapsed, taskOutputFlushInterval)
}

func TestRecorderPacedDeltasKeepWriteFrequencyBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	defer recorder.Close(context.Background())
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "s", TaskID: "paced", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	start := time.Now()
	for range 200 {
		if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "delta", Running: true}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: "s", TaskID: "paced"}); err != nil {
		t.Fatal(err)
	}
	calls, bytes := spool.WriteStats()
	elapsed := time.Since(start)
	// Allow scheduling jitter and a header/final partial batch, while catching
	// a drain loop that writes each arriving delta separately.
	if calls > uint64(elapsed/taskOutputFlushInterval)*2+4 || calls < 2 {
		t.Fatalf("paced writes=%d over %s", calls, elapsed)
	}
	t.Logf("200 paced deltas: %d file writes, %d bytes over %s", calls, bytes, elapsed)
}

func TestRecorderUnencodableOutputInvalidatesCache(t *testing.T) {
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	defer recorder.Close(context.Background())
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{SessionID: "s", TaskID: "t", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	err := observer.ObserveTaskOutput(t.Context(), output.Event{Event: &session.Event{Meta: map[string]any{"invalid": make(chan int)}}})
	if err == nil {
		t.Fatal("unsupported output was silently accepted")
	}
	if err := recorder.Flush(t.Context(), task.Ref{SessionID: "s", TaskID: "t"}); err == nil {
		t.Fatal("cache with a missing record remained complete")
	}
}
