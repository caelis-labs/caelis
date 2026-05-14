package providers

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadSSEWithInactivityTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- readSSEWithInactivity(reader, 20*time.Millisecond, func([]byte) error {
			return nil
		})
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, errStreamInactivityTimeout) && !strings.Contains(err.Error(), "inactivity") {
			t.Fatalf("readSSEWithInactivity() error = %v, want inactivity timeout", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("readSSEWithInactivity() did not time out")
	}
}
