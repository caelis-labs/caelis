package providers

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

func TestReadSSEWithFirstEventTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- readSSEWithFirstEventTimeout(reader, 20*time.Millisecond, func([]byte) error {
			return nil
		})
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, errStreamFirstEventTimeout) {
			t.Fatalf("readSSEWithFirstEventTimeout() error = %v, want first event timeout", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readSSEWithFirstEventTimeout() did not time out")
	}
}

func TestReadSSEWithFirstEventTimeout_AllowsSilenceAfterFirstEvent(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	dataCh := make(chan string, 2)
	errCh := make(chan error, 1)
	go func() {
		errCh <- readSSEWithFirstEventTimeout(reader, 20*time.Millisecond, func(data []byte) error {
			dataCh <- string(data)
			return nil
		})
	}()

	if _, err := writer.Write([]byte("data: first\n\n")); err != nil {
		t.Fatalf("write first event: %v", err)
	}
	select {
	case got := <-dataCh:
		if got != "first" {
			t.Fatalf("first data = %q, want first", got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("first event was not observed")
	}

	time.Sleep(80 * time.Millisecond)
	select {
	case err := <-errCh:
		t.Fatalf("readSSEWithFirstEventTimeout() returned during post-first silence: %v", err)
	default:
	}

	if _, err := writer.Write([]byte("data: second\n\ndata: [DONE]\n\n")); err != nil {
		t.Fatalf("write second event: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("readSSEWithFirstEventTimeout() error = %v, want nil", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readSSEWithFirstEventTimeout() did not finish")
	}
	select {
	case got := <-dataCh:
		if got != "second" {
			t.Fatalf("second data = %q, want second", got)
		}
	default:
		t.Fatal("second event was not observed")
	}
}

func TestReadSSEWithEventTimeout_TimesOutAfterFirstEventSilence(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	dataCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- readSSEWithEventTimeout(reader, 250*time.Millisecond, 20*time.Millisecond, func(data []byte) error {
			dataCh <- string(data)
			return nil
		})
	}()

	if _, err := writer.Write([]byte("data: first\n\n")); err != nil {
		t.Fatalf("write first event: %v", err)
	}
	select {
	case got := <-dataCh:
		if got != "first" {
			t.Fatalf("first data = %q, want first", got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("first event was not observed")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, errStreamIdleTimeout) {
			t.Fatalf("readSSEWithEventTimeout() error = %v, want idle timeout", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readSSEWithEventTimeout() did not time out")
	}
}

func TestReadSSEWithEventTimeout_AllowsSilenceWhenIdleDisabled(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	dataCh := make(chan string, 2)
	errCh := make(chan error, 1)
	go func() {
		errCh <- readSSEWithEventTimeout(reader, 20*time.Millisecond, 0, func(data []byte) error {
			dataCh <- string(data)
			return nil
		})
	}()

	if _, err := writer.Write([]byte("data: first\n\n")); err != nil {
		t.Fatalf("write first event: %v", err)
	}
	select {
	case got := <-dataCh:
		if got != "first" {
			t.Fatalf("first data = %q, want first", got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("first event was not observed")
	}

	time.Sleep(80 * time.Millisecond)
	select {
	case err := <-errCh:
		t.Fatalf("readSSEWithEventTimeout() returned with idle disabled: %v", err)
	default:
	}

	if _, err := writer.Write([]byte("data: second\n\ndata: [DONE]\n\n")); err != nil {
		t.Fatalf("write second event: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("readSSEWithEventTimeout() error = %v, want nil", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readSSEWithEventTimeout() did not finish")
	}
}

func TestStreamFirstEventTimeoutErrorIsRetryable(t *testing.T) {
	t.Parallel()

	err := newStreamFirstEventTimeoutError(5 * time.Minute)
	if !errors.Is(err, errStreamFirstEventTimeout) {
		t.Fatalf("errors.Is(%v, errStreamFirstEventTimeout) = false", err)
	}
	var retryable model.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("error %T does not implement model.RetryableError", err)
	}
	if !retryable.Retryable() {
		t.Fatal("Retryable() = false, want true")
	}
}

func TestStreamIdleTimeoutErrorIsRetryable(t *testing.T) {
	t.Parallel()

	err := newStreamIdleTimeoutError(5 * time.Minute)
	if !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("errors.Is(%v, errStreamIdleTimeout) = false", err)
	}
	var retryable model.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("error %T does not implement model.RetryableError", err)
	}
	if !retryable.Retryable() {
		t.Fatal("Retryable() = false, want true")
	}
}

func TestNormalizeStreamFirstEventTimeoutDefault(t *testing.T) {
	t.Parallel()

	if got := normalizeStreamFirstEventTimeout(0); got != 5*time.Minute {
		t.Fatalf("normalizeStreamFirstEventTimeout(0) = %s, want 5m", got)
	}
	if got := normalizeStreamFirstEventTimeout(-1); got != 0 {
		t.Fatalf("normalizeStreamFirstEventTimeout(-1) = %s, want disabled zero", got)
	}
}
