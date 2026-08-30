package taskstream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestSubscriptionFailureUnblocksAbandonedDelivery(t *testing.T) {
	sub := newSubscription(context.Background())
	if !sub.enqueue(Record{Cursor: "cursor-1"}) {
		t.Fatal("enqueue() = false, want queued delivery")
	}
	wantErr := errors.New("runtime stream failed")
	sub.finish(wantErr)

	deadline := time.After(time.Second)

waitForClose:
	for {
		select {
		case _, open := <-sub.Records():
			if !open {
				break waitForClose
			}
		case <-deadline:
			t.Fatal("failed subscription left delivery goroutine blocked")
		}
	}
	if !errors.Is(sub.Err(), wantErr) {
		t.Fatalf("Err() = %v, want %v", sub.Err(), wantErr)
	}
}

func TestLiveSubscriptionStillFailsFastForSlowConsumer(t *testing.T) {
	sub := newSubscription(context.Background())
	defer sub.Close()
	for index := 0; index < subscriberEventCap+2; index++ {
		if !sub.enqueue(Record{Cursor: "cursor"}) {
			break
		}
	}
	if !errors.Is(sub.Err(), ErrSlowConsumer) {
		t.Fatalf("Err() = %v, want %v", sub.Err(), ErrSlowConsumer)
	}
}

func TestLiveSubscriptionAbsorbsOrdinaryTUIRenderStall(t *testing.T) {
	const ordinaryBurstRecords = 256

	sub := newSubscription(context.Background())
	defer sub.Close()
	for index := 0; index < ordinaryBurstRecords; index++ {
		if !sub.enqueue(Record{Cursor: "cursor", Frame: &stream.Frame{Text: "build output\n"}}) {
			t.Fatalf("ordinary burst rejected record %d: %v", index, sub.Err())
		}
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("ordinary render stall closed subscription: %v", err)
	}
}

func TestInitialSubscriptionDeliversOneExactFinalLargerThanLiveByteCap(t *testing.T) {
	sub := newSubscription(context.Background())
	defer sub.Close()
	final := strings.Repeat("f", subscriberByteCap+1)
	if !sub.enqueueInitial(Record{Cursor: "cursor", Frame: &stream.Frame{Text: final}}) {
		t.Fatalf("enqueueInitial() = false: %v", sub.Err())
	}
	select {
	case record := <-sub.Records():
		if record.Frame == nil || record.Frame.Text != final {
			t.Fatalf("initial exact Final bytes = %d, want %d", len(record.Frame.Text), len(final))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out receiving oversized exact Final")
	}
	if sub.Err() != nil {
		t.Fatalf("subscription error = %v", sub.Err())
	}
}

func TestInitialCatchupWaitUnblocksWhenParentContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := newSubscription(ctx)
	done := make(chan bool, 1)
	go func() {
		accepted := true
		for index := 0; index < subscriberEventCap+2 && accepted; index++ {
			accepted = sub.enqueueInitial(Record{Cursor: "cursor"})
		}
		done <- accepted
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("blocked initial catch-up accepted every record after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not unblock initial catch-up")
	}
}

func TestInitialDrainBarrierUnblocksWhenParentContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := newSubscription(ctx)
	if !sub.enqueueInitial(Record{Cursor: "cursor"}) {
		t.Fatalf("enqueueInitial() = false: %v", sub.Err())
	}
	done := make(chan bool, 1)
	go func() {
		done <- sub.awaitInitialDrain()
	}()
	cancel()
	select {
	case drained := <-done:
		if drained {
			t.Fatal("initial drain barrier reported success after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not unblock initial drain barrier")
	}
}
