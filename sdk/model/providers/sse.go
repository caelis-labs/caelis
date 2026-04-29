package providers

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errStopSSE = errors.New("providers: stop sse")

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
