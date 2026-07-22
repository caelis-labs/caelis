package taskstream

import (
	"context"
	"errors"
	"testing"
	"time"
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
