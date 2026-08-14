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
	if !ok || first.Dropped != 0 || first.Item != 1 {
		t.Fatalf("first Pop() = %#v, %v; want item 1", first, ok)
	}
	second, ok := q.Pop()
	if !ok || second.Dropped != 0 || second.Item != 2 {
		t.Fatalf("second Pop() = %#v, %v; want item 2", second, ok)
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
		delivery, ok := q.Pop()
		if ok && delivery.Dropped == 0 {
			got <- delivery.Item
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

func TestEventQueueOverwritesOldestWithoutBlocking(t *testing.T) {
	t.Parallel()

	q := newEventQueueWithCapacity[int](2)
	if !q.Push(1) || !q.Push(2) {
		t.Fatal("initial Push() failed")
	}
	pushed := make(chan bool, 1)
	go func() { pushed <- q.Push(3) }()
	select {
	case ok := <-pushed:
		if !ok {
			t.Fatal("third Push() failed")
		}
	case <-time.After(time.Second):
		t.Fatal("third Push() blocked on a full queue")
	}
	if delivery, ok := q.Pop(); !ok || delivery.Item != 0 || delivery.Dropped != 1 {
		t.Fatalf("gap Pop() = %#v, %v; want one dropped", delivery, ok)
	}
	if delivery, ok := q.Pop(); !ok || delivery.Item != 2 || delivery.Dropped != 0 {
		t.Fatalf("first retained Pop() = %#v, %v; want item 2", delivery, ok)
	}
	if delivery, ok := q.Pop(); !ok || delivery.Item != 3 || delivery.Dropped != 0 {
		t.Fatalf("second retained Pop() = %#v, %v; want item 3", delivery, ok)
	}
	q.Close()
}

func TestEventQueueReportsDropsThatOccurAfterGapWhileDraining(t *testing.T) {
	t.Parallel()

	q := newEventQueueWithCapacity[int](2)
	q.Push(1)
	q.Push(2)
	q.Push(3)
	if delivery, ok := q.Pop(); !ok || delivery.Dropped != 1 {
		t.Fatalf("first gap Pop() = %#v, %v; want one dropped", delivery, ok)
	}

	// The retained [2, 3] suffix changes after the first gap was delivered.
	// Both later overwrites must be reported before the new [4, 5] suffix.
	q.Push(4)
	q.Push(5)
	if delivery, ok := q.Pop(); !ok || delivery.Dropped != 2 {
		t.Fatalf("second gap Pop() = %#v, %v; want two newly dropped", delivery, ok)
	}
	for _, want := range []int{4, 5} {
		if delivery, ok := q.Pop(); !ok || delivery.Dropped != 0 || delivery.Item != want {
			t.Fatalf("retained Pop() = %#v, %v; want item %d", delivery, ok, want)
		}
	}
	q.Close()
}

func TestEventQueueAbortDropsItemsAndRejectsPush(t *testing.T) {
	t.Parallel()

	q := newEventQueueWithCapacity[int](1)
	q.Push(1)
	q.Push(2)
	q.Abort()
	if q.Push(3) {
		t.Fatal("Push() succeeded after Abort")
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("Pop() returned an item after Abort")
	}
}
