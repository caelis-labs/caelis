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
