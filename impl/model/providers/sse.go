package providers

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var errStopSSE = errors.New("providers: stop sse")
var errStreamInactivityTimeout = errors.New("providers: stream inactivity timeout")

const defaultStreamInactivityTimeout = 90 * time.Second

func readSSEWithInactivity(reader io.Reader, timeout time.Duration, onData func([]byte) error) error {
	if timeout <= 0 {
		return readSSE(reader, onData)
	}
	errCh := make(chan error, 1)
	activityCh := make(chan struct{}, 1)
	go func() {
		errCh <- readSSE(reader, func(data []byte) error {
			select {
			case activityCh <- struct{}{}:
			default:
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
		case <-activityCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			if closer, ok := reader.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			return errStreamInactivityTimeout
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
