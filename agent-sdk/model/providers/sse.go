package providers

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

var errStopSSE = errors.New("providers: stop sse")

var errStreamFirstEventTimeout = errors.New("providers: stream first event timeout")

var errStreamIdleTimeout = errors.New("providers: stream idle timeout")

const defaultStreamFirstEventTimeout = 5 * time.Minute

type streamFirstEventTimeoutError struct {
	timeout time.Duration
}

func (e streamFirstEventTimeoutError) Error() string {
	if e.timeout > 0 {
		return fmt.Sprintf("%s after %s", errStreamFirstEventTimeout.Error(), e.timeout)
	}
	return errStreamFirstEventTimeout.Error()
}

func (e streamFirstEventTimeoutError) Unwrap() error {
	return errStreamFirstEventTimeout
}

func (e streamFirstEventTimeoutError) Retryable() bool {
	return true
}

func (e streamFirstEventTimeoutError) ErrorCode() errorcode.Code { return errorcode.Timeout }

func normalizeStreamFirstEventTimeout(timeout time.Duration) time.Duration {
	if timeout < 0 {
		return 0
	}
	if timeout == 0 {
		return defaultStreamFirstEventTimeout
	}
	return timeout
}

func newStreamFirstEventTimeoutError(timeout time.Duration) error {
	return streamFirstEventTimeoutError{timeout: timeout}
}

type streamIdleTimeoutError struct {
	timeout time.Duration
}

func (e streamIdleTimeoutError) Error() string {
	if e.timeout > 0 {
		return fmt.Sprintf("%s after %s", errStreamIdleTimeout.Error(), e.timeout)
	}
	return errStreamIdleTimeout.Error()
}

func (e streamIdleTimeoutError) Unwrap() error {
	return errStreamIdleTimeout
}

func (e streamIdleTimeoutError) Retryable() bool {
	return true
}

func (e streamIdleTimeoutError) ErrorCode() errorcode.Code { return errorcode.Timeout }

func newStreamIdleTimeoutError(timeout time.Duration) error {
	return streamIdleTimeoutError{timeout: timeout}
}

// readSSEWithFirstEventTimeout only bounds the initial wait for a model-visible
// data event. Once a stream starts, caller cancellation owns the lifetime.
func readSSEWithFirstEventTimeout(reader io.Reader, timeout time.Duration, onData func([]byte) error) error {
	if timeout <= 0 {
		return readSSE(reader, onData)
	}
	errCh := make(chan error, 1)
	firstEventCh := make(chan struct{}, 1)
	seenFirstEvent := false
	go func() {
		errCh <- readSSE(reader, func(data []byte) error {
			if !seenFirstEvent {
				seenFirstEvent = true
				select {
				case firstEventCh <- struct{}{}:
				default:
				}
			}
			return onData(data)
		})
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case err := <-errCh:
			return err
		case <-firstEventCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return <-errCh
		case <-timer.C:
			if closer, ok := reader.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			return newStreamFirstEventTimeoutError(timeout)
		}
	}
}

func readSSEWithEventTimeout(reader io.Reader, firstEventTimeout time.Duration, idleTimeout time.Duration, onData func([]byte) error) error {
	if firstEventTimeout <= 0 && idleTimeout <= 0 {
		return readSSE(reader, onData)
	}
	errCh := make(chan error, 1)
	eventCh := make(chan struct{}, 1)
	go func() {
		errCh <- readSSE(reader, func(data []byte) error {
			select {
			case eventCh <- struct{}{}:
			default:
			}
			return onData(data)
		})
	}()

	var timer *time.Timer
	var timerCh <-chan time.Time
	resetTimer := func(timeout time.Duration) {
		if timeout <= 0 {
			if timer != nil {
				timer.Stop()
			}
			timerCh = nil
			return
		}
		if timer == nil {
			timer = time.NewTimer(timeout)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		}
		timerCh = timer.C
	}
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
		}
	}
	defer stopTimer()

	seenEvent := false
	resetTimer(firstEventTimeout)
	for {
		select {
		case err := <-errCh:
			return err
		case <-eventCh:
			seenEvent = true
			resetTimer(idleTimeout)
		case <-timerCh:
			if closer, ok := reader.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			if !seenEvent {
				return newStreamFirstEventTimeoutError(firstEventTimeout)
			}
			return newStreamIdleTimeoutError(idleTimeout)
		}
	}
}

func readSSE(reader io.Reader, onData func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var dataLines [][]byte
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		chunk := strings.TrimSpace(string(payload))
		if chunk == "" {
			return nil
		}
		if chunk == "[DONE]" {
			return errStopSSE
		}
		return onData([]byte(chunk))
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				if errors.Is(err, errStopSSE) {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			dataLines = append(dataLines, []byte(data))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("providers: sse scanner: %w", err)
	}
	if err := flush(); err != nil && !errors.Is(err, errStopSSE) {
		return err
	}
	return nil
}
