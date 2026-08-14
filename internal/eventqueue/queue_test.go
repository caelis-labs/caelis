package eventqueue

import (
	"fmt"
	"testing"
	"time"
)

func TestQueuePreservesFIFO(t *testing.T) {
	t.Parallel()

	q := New[int]()
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

func TestQueuePopWaitsUntilPush(t *testing.T) {
	t.Parallel()

	q := New[string]()
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

func TestQueueClearDropsBufferedItems(t *testing.T) {
	t.Parallel()

	q := New[int]()
	q.Push(1)
	q.Push(2)
	q.Clear()
	q.Close()
	if _, ok := q.Pop(); ok {
		t.Fatal("Pop() ok = true after Clear and Close, want false")
	}
}

func TestQueuePopReleasesHeadWithoutShiftingBufferedTail(t *testing.T) {
	t.Parallel()

	const backlog = 4096
	values := make([]int, backlog)
	q := New[*int]()
	for i := range values {
		values[i] = i
		q.Push(&values[i])
	}

	q.mu.Lock()
	backing := q.items[:len(q.items):len(q.items)]
	first := backing[0]
	second := backing[1]
	q.mu.Unlock()

	got, ok := q.Pop()
	if !ok || got != first {
		t.Fatalf("Pop() = %p, %v; want first buffered item %p, true", got, ok, first)
	}
	if backing[0] == second {
		t.Fatal("Pop() shifted the buffered tail over the head; repeated backlog drains are quadratic")
	}
	if backing[0] != nil {
		t.Fatal("Pop() retained the consumed head; want the released slot cleared")
	}
	for want := 1; want < backlog; want++ {
		got, ok := q.Pop()
		if !ok || got != &values[want] {
			t.Fatalf("Pop(%d) = %p, %v; want %p, true", want, got, ok, &values[want])
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.head != 0 || len(q.items) != 0 {
		t.Fatalf("drained queue head/len = %d/%d, want 0/0", q.head, len(q.items))
	}
}

func BenchmarkQueueDrain(b *testing.B) {
	for _, size := range []int{1024, 4096, 16384} {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				q := New[int]()
				for i := range size {
					q.Push(i)
				}
				q.Close()
				for range size {
					if _, ok := q.Pop(); !ok {
						b.Fatalf("Pop() closed before draining %d items", size)
					}
				}
			}
		})
	}
}
