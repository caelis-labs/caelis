package runtime

import (
	"testing"
	"time"
)

func TestEventQueuePreservesFIFO(t *testing.T) {
	t.Parallel()

	q := newEventQueue[int]()
	q.Push(1)
	q.Push(2)
	q.Close()

	first, ok := q.Pop()
	if !ok || first != 1 {
		t.Fatalf("first Pop() = %d, %v; want 1, true", first, ok)
	}
	second, ok := q.Pop()
	if !ok || second != 2 {
		t.Fatalf("second Pop() = %d, %v; want 2, true", second, ok)
	}
	_, ok = q.Pop()
	if ok {
		t.Fatal("third Pop() ok = true, want false after close and drain")
	}
}

func TestEventQueuePopWaitsUntilPush(t *testing.T) {
	t.Parallel()

	q := newEventQueue[string]()
	got := make(chan string, 1)
	go func() {
		value, ok := q.Pop()
		if ok {
			got <- value
		}
	}()

	select {
	case value := <-got:
		t.Fatalf("Pop() returned early with %q", value)
	case <-time.After(20 * time.Millisecond):
	}
	q.Push("ready")
	select {
	case value := <-got:
		if value != "ready" {
			t.Fatalf("Pop() = %q, want ready", value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Pop after Push")
	}
}

func TestEventQueueClearDropsBufferedItems(t *testing.T) {
	t.Parallel()

	q := newEventQueue[int]()
	q.Push(1)
	q.Push(2)
	q.Clear()
	q.Close()
	if _, ok := q.Pop(); ok {
		t.Fatal("Pop() ok = true after Clear and Close, want false")
	}
}

func TestEventQueueAppliesBoundedBackpressure(t *testing.T) {
	t.Parallel()

	q := newEventQueueWithCapacity[int](2)
	if !q.Push(1) || !q.Push(2) {
		t.Fatal("initial Push() failed")
	}
	pushed := make(chan bool, 1)
	go func() { pushed <- q.Push(3) }()
	select {
	case <-pushed:
		t.Fatal("third Push() returned before capacity became available")
	case <-time.After(20 * time.Millisecond):
	}
	if value, ok := q.Pop(); !ok || value != 1 {
		t.Fatalf("Pop() = %d, %v; want 1, true", value, ok)
	}
	select {
	case ok := <-pushed:
		if !ok {
			t.Fatal("third Push() failed after capacity became available")
		}
	case <-time.After(time.Second):
		t.Fatal("third Push() remained blocked")
	}
	q.Close()
}

func TestEventQueueAbortDropsItemsAndUnblocksProducer(t *testing.T) {
	t.Parallel()

	q := newEventQueueWithCapacity[int](1)
	q.Push(1)
	pushed := make(chan bool, 1)
	go func() { pushed <- q.Push(2) }()
	time.Sleep(20 * time.Millisecond)
	q.Abort()
	select {
	case ok := <-pushed:
		if ok {
			t.Fatal("blocked Push() succeeded after Abort")
		}
	case <-time.After(time.Second):
		t.Fatal("Abort() did not unblock producer")
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("Pop() returned an item after Abort")
	}
}
