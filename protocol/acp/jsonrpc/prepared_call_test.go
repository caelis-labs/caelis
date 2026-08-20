package jsonrpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestPreparedCallDispatchCompletesShortWrites(t *testing.T) {
	t.Parallel()

	writer := &shortWriter{limit: 3}
	conn := New(nil, writer)
	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(context.Background()); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !bytes.Equal(writer.Bytes(), call.payload) {
		t.Fatalf("written payload = %q, want %q", writer.Bytes(), call.payload)
	}
	call.Abandon()
}

func TestPreparedCallPartialWriteIsUnknown(t *testing.T) {
	t.Parallel()

	conn := New(nil, partialErrorWriter{})
	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	err = call.Dispatch(context.Background())
	if err == nil || !DispatchMayHaveCommitted(err) {
		t.Fatalf("Dispatch() error = %v, want possibly committed partial write", err)
	}
	if pendingCallCount(conn) != 0 {
		t.Fatal("partial dispatch retained pending call")
	}
}

func TestPreparedCallZeroProgressIsUnknownAfterWriteStarts(t *testing.T) {
	t.Parallel()

	conn := New(nil, zeroWriter{})
	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	err = call.Dispatch(context.Background())
	if !errors.Is(err, io.ErrNoProgress) || !DispatchMayHaveCommitted(err) {
		t.Fatalf("Dispatch() error = %v committed=%v, want ambiguous started write", err, DispatchMayHaveCommitted(err))
	}
}

func TestPreparedCallWriterAdmissionCancellationNeverWritesLater(t *testing.T) {
	t.Parallel()

	writer := newBlockingWriter()
	conn := New(nil, writer)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- conn.Notify("first", map[string]any{"value": 1})
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("first write did not acquire writer")
	}

	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	dispatchDone := make(chan error, 1)
	go func() { dispatchDone <- call.Dispatch(ctx) }()
	cancel()
	select {
	case err := <-dispatchDone:
		if !errors.Is(err, context.Canceled) || DispatchMayHaveCommitted(err) {
			t.Fatalf("Dispatch() error = %v committed=%v", err, DispatchMayHaveCommitted(err))
		}
	case <-time.After(time.Second):
		t.Fatal("writer admission cancellation did not return")
	}
	if pendingCallCount(conn) != 0 {
		t.Fatal("cancelled writer admission retained pending call")
	}

	close(writer.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Notify() error = %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := writer.Calls(); got != 1 {
		t.Fatalf("writer calls = %d, want no delayed prompt write", got)
	}
}

func TestWriterArbiterGrantsQueuedWritersFIFO(t *testing.T) {
	t.Parallel()

	arbiter := newWriterArbiter()
	releaseFirst, err := arbiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type admission struct {
		name    string
		release func()
		err     error
	}
	admitted := make(chan admission, 2)
	queue := func(name string) {
		go func() {
			release, acquireErr := arbiter.acquire(context.Background())
			admitted <- admission{name: name, release: release, err: acquireErr}
		}()
	}
	waitForQueuedWriter := func(want int) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			arbiter.mu.Lock()
			got := len(arbiter.waiters)
			arbiter.mu.Unlock()
			if got == want {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("queued writers = %d, want %d", got, want)
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	queue("second")
	waitForQueuedWriter(1)
	queue("third")
	waitForQueuedWriter(2)

	releaseFirst()
	second := <-admitted
	if second.err != nil || second.name != "second" {
		t.Fatalf("first queued admission = %#v, want second", second)
	}
	select {
	case early := <-admitted:
		t.Fatalf("third writer admitted before second released: %#v", early)
	case <-time.After(20 * time.Millisecond):
	}
	second.release()
	third := <-admitted
	if third.err != nil || third.name != "third" {
		t.Fatalf("second queued admission = %#v, want third", third)
	}
	third.release()
}

func TestPreparedCallWaitOwnershipOutlivesDispatchCaller(t *testing.T) {
	t.Parallel()

	conn := New(nil, &bytes.Buffer{})
	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	if err := call.Dispatch(callerCtx); err != nil {
		t.Fatal(err)
	}
	cancelCaller()

	type response struct {
		StopReason string `json:"stopReason"`
	}
	result := make(chan struct {
		value response
		err   error
	}, 1)
	go func() {
		var value response
		err := call.Wait(context.Background(), &value)
		result <- struct {
			value response
			err   error
		}{value: value, err: err}
	}()
	conn.resolvePending(Message{ID: call.id, Result: map[string]any{"stopReason": "end_turn"}})
	got := <-result
	if got.err != nil || got.value.StopReason != "end_turn" {
		t.Fatalf("Wait() = %#v, %v", got.value, got.err)
	}
	if pendingCallCount(conn) != 0 {
		t.Fatal("completed wait retained pending call")
	}
}

func TestPreparedCallResponseObserverLinearizesBeforeWait(t *testing.T) {
	t.Parallel()

	conn := New(nil, &bytes.Buffer{})
	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	observerEntered := make(chan struct{})
	releaseObserver := make(chan struct{})
	if err := call.ObserveResponse(func(message Message) error {
		if message.Error == nil || message.Error.Code != -32000 {
			t.Errorf("observed response = %#v, want auth error", message)
		}
		close(observerEntered)
		// Production observers are non-blocking. This controlled pause proves
		// the response cannot become visible to Wait before the observer's
		// admission-state transition completes.
		<-releaseObserver
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- call.Wait(context.Background(), nil) }()
	resolveDone := make(chan struct{})
	go func() {
		conn.resolvePending(Message{ID: call.id, Error: &RPCError{Code: -32000, Message: "Authentication required"}})
		close(resolveDone)
	}()
	select {
	case <-observerEntered:
	case <-time.After(time.Second):
		t.Fatal("response observer did not run")
	}
	select {
	case err := <-waitDone:
		t.Fatalf("Wait() crossed response observer: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseObserver)
	<-resolveDone
	err = <-waitDone
	if code, ok := ErrorCode(err); !ok || code != -32000 {
		t.Fatalf("Wait() error = %v code=%d ok=%v", err, code, ok)
	}
}

func TestPreparedCallSuccessfulResponseDecodeFailureIsPossiblyCommitted(t *testing.T) {
	t.Parallel()

	conn := New(nil, &bytes.Buffer{})
	call, err := conn.PrepareCall("_session/steering", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	type response struct {
		Outcome string `json:"outcome"`
	}
	conn.resolvePending(Message{ID: call.id, Result: map[string]any{"outcome": 42}})
	var value response
	err = call.Wait(context.Background(), &value)
	var decodeErr *ResponseDecodeError
	if !errors.As(err, &decodeErr) || !DispatchMayHaveCommitted(err) {
		t.Fatalf("Wait() error = %v committed=%v, want typed ambiguous decode failure", err, DispatchMayHaveCommitted(err))
	}
	if pendingCallCount(conn) != 0 {
		t.Fatal("decode failure retained pending call")
	}
}

func TestConnCallCancellationAfterDispatchIsPossiblyCommitted(t *testing.T) {
	t.Parallel()

	conn := New(nil, &bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- conn.Call(ctx, "_session/steering", map[string]any{"sessionId": "s1"}, nil)
	}()
	deadline := time.After(time.Second)
	for pendingCallCount(conn) == 0 {
		select {
		case <-deadline:
			t.Fatal("call did not register pending response")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) || !DispatchMayHaveCommitted(err) {
		t.Fatalf("Call() error = %v committed=%v, want post-dispatch unknown", err, DispatchMayHaveCommitted(err))
	}
}

func TestPreparedCallStartedWriteCancellationAbortsAndIsUnknown(t *testing.T) {
	t.Parallel()

	writer := newBlockingWriter()
	conn := New(nil, writer)
	call, err := conn.PrepareCall("session/prompt", map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	aborted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- call.DispatchWithAbort(ctx, func() {
			close(writer.release)
			close(aborted)
		})
	}()
	select {
	case <-call.WriteStarted():
	case <-time.After(time.Second):
		t.Fatal("transport write did not start")
	}
	cancel()
	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("started write cancellation did not revoke transport")
	}
	err = <-done
	if !errors.Is(err, context.Canceled) || !DispatchMayHaveCommitted(err) {
		t.Fatalf("DispatchWithAbort() error = %v committed=%v, want cancelled unknown outcome", err, DispatchMayHaveCommitted(err))
	}
	if pendingCallCount(conn) != 0 {
		t.Fatal("ambiguous cancelled dispatch retained pending call")
	}
}

type shortWriter struct {
	bytes.Buffer
	limit int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.Buffer.Write(p)
}

type partialErrorWriter struct{}

func (partialErrorWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, errors.New("empty write")
	}
	return 1, errors.New("partial failure")
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls int
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

func (w *blockingWriter) Calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

func pendingCallCount(conn *Conn) int {
	if conn == nil {
		return 0
	}
	count := 0
	conn.pending.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
