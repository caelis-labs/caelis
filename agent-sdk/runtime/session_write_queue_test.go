package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestSessionWriteQueuePreservesArrivalOrder(t *testing.T) {
	t.Parallel()

	var queue sessionWriteQueue
	ref := session.SessionRef{SessionID: "fifo-session"}
	firstRelease, err := queue.acquire(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	first := sessionWriteTailForTest(&queue, ref)

	secondAcquired := make(chan func(), 1)
	go func() {
		release, acquireErr := queue.acquire(context.Background(), ref)
		if acquireErr == nil {
			secondAcquired <- release
		}
	}()
	second := waitForSessionWriteTailChange(t, &queue, ref, first)

	thirdAcquired := make(chan func(), 1)
	go func() {
		release, acquireErr := queue.acquire(context.Background(), ref)
		if acquireErr == nil {
			thirdAcquired <- release
		}
	}()
	waitForSessionWriteTailChange(t, &queue, ref, second)

	firstRelease()
	secondRelease := receiveSessionWriteAdmission(t, secondAcquired, "second")
	select {
	case release := <-thirdAcquired:
		release()
		t.Fatal("third writer acquired before second writer released")
	default:
	}
	secondRelease()
	thirdRelease := receiveSessionWriteAdmission(t, thirdAcquired, "third")
	thirdRelease()
}

func TestSessionWriteQueueCancellationPassesAdmissionToSuccessor(t *testing.T) {
	t.Parallel()

	var queue sessionWriteQueue
	ref := session.SessionRef{SessionID: "cancelled-fifo-session"}
	firstRelease, err := queue.acquire(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	first := sessionWriteTailForTest(&queue, ref)

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, acquireErr := queue.acquire(ctx, ref)
		secondDone <- acquireErr
	}()
	second := waitForSessionWriteTailChange(t, &queue, ref, first)

	thirdAcquired := make(chan func(), 1)
	go func() {
		release, acquireErr := queue.acquire(context.Background(), ref)
		if acquireErr == nil {
			thirdAcquired <- release
		}
	}()
	waitForSessionWriteTailChange(t, &queue, ref, second)
	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	select {
	case release := <-thirdAcquired:
		release()
		t.Fatal("successor acquired before the first writer released")
	default:
	}
	firstRelease()
	thirdRelease := receiveSessionWriteAdmission(t, thirdAcquired, "successor after cancellation")
	thirdRelease()
}

func TestSessionWriteQueueCancellationUnlinksBeforePredecessorCompletes(t *testing.T) {
	t.Parallel()

	var queue sessionWriteQueue
	ref := session.SessionRef{SessionID: "cancelled-tail-session"}
	firstRelease, err := queue.acquire(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	first := sessionWriteTailForTest(&queue, ref)

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, acquireErr := queue.acquire(ctx, ref)
		secondDone <- acquireErr
	}()
	waitForSessionWriteTailChange(t, &queue, ref, first)

	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	if tail := sessionWriteTailForTest(&queue, ref); tail != first {
		t.Fatalf("tail after cancellation = %p, want predecessor %p", tail, first)
	}

	firstRelease()
	if tail := sessionWriteTailForTest(&queue, ref); tail != nil {
		t.Fatalf("tail after predecessor release = %p, want nil", tail)
	}
}

func sessionWriteTailForTest(queue *sessionWriteQueue, ref session.SessionRef) *sessionWriteTicket {
	key := session.NormalizeSessionRef(ref).SessionID
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.tails[key]
}

func waitForSessionWriteTailChange(
	t *testing.T,
	queue *sessionWriteQueue,
	ref session.SessionRef,
	previous *sessionWriteTicket,
) *sessionWriteTicket {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current := sessionWriteTailForTest(queue, ref)
		if current != nil && current != previous {
			return current
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for Session writer to join FIFO")
	return nil
}

func receiveSessionWriteAdmission(t *testing.T, admitted <-chan func(), label string) func() {
	t.Helper()
	select {
	case release := <-admitted:
		return release
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s admission", label)
		return nil
	}
}
