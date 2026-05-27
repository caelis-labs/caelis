package providers

import (
	"errors"
	"io"
	"testing"
	"time"
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
