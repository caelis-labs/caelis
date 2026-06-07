package providers

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestReadSSEJoinsMultilineDataAndStopsAtDone(t *testing.T) {
	input := "event: ignored\n" +
		"data: {\"a\":1,\n" +
		"data: \"b\":2}\n\n" +
		"data: [DONE]\n\n" +
		"data: {\"ignored\":true}\n\n"

	var got []string
	err := readSSE(strings.NewReader(input), func(data []byte) error {
		got = append(got, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("readSSE() error = %v", err)
	}
	if len(got) != 1 || got[0] != "{\"a\":1,\n\"b\":2}" {
		t.Fatalf("data = %#v, want joined multiline payload", got)
	}
}

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
		var retryable model.RetryableError
		if !errors.As(err, &retryable) || !retryable.Retryable() {
			t.Fatalf("timeout error %T is not retryable", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readSSEWithFirstEventTimeout() did not time out")
	}
}

func TestReadSSEWithFirstEventTimeoutAllowsSilenceAfterFirstEvent(t *testing.T) {
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
