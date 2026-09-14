package subagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync/atomic"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Provider history staging is private to one session/load attempt. Only the
// contiguous input group remains mutable for legacy sender-footer attribution.
// Completed groups and output events spill without a total in-memory replay.
const historyStagingLimit = 256 << 20
const historyStagingGlobalLimit = 1 << 30

var historyStagingBytes atomic.Int64

type historyStaging struct {
	bytes  int64
	file   *os.File
	buffer *bufio.Writer
}

func (c *historyCollector) openStaging() {
	file, err := os.CreateTemp("", "caelis-child-history-*.jsonl")
	if err != nil {
		c.err = err
		return
	}
	c.staging = &historyStaging{file: file, buffer: bufio.NewWriterSize(file, 64<<10)}
}

// stageEvents runs under the collector mutex while replay is in progress.
func (c *historyCollector) stageEvents() {
	if c.staging == nil || c.err != nil {
		return
	}
	encoder := json.NewEncoder(c.staging)
	for _, event := range c.events {
		if err := encoder.Encode(event); err != nil {
			c.err = err
			return
		}
	}
	c.events = nil
	c.bytes, c.inputStart = 0, 0
}

func (c *historyCollector) streamEvents(ctx context.Context, yield func(*session.Event) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushUserText()
	c.stageEvents()
	if c.err != nil {
		return c.err
	}
	if err := c.staging.buffer.Flush(); err != nil {
		return err
	}
	if _, err := c.staging.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	decoder := json.NewDecoder(c.staging.file)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event session.Event
		if err := decoder.Decode(&event); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if err := yield(&event); err != nil {
			return err
		}
	}
}

func (c *historyCollector) closeStaging() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.staging != nil {
		historyStagingBytes.Add(-c.staging.bytes)
		_ = c.staging.file.Close()
		_ = os.Remove(c.staging.file.Name())
		c.staging = nil
	}
}

func (s *historyStaging) Write(p []byte) (int, error) {
	size := int64(len(p))
	if s.bytes+size > historyStagingLimit {
		return 0, errorcode.New(errorcode.ResourceExhausted, "Child history exceeds staging disk budget")
	}
	if historyStagingBytes.Add(size) > historyStagingGlobalLimit {
		historyStagingBytes.Add(-size)
		return 0, errorcode.New(errorcode.ResourceExhausted, "Concurrent child history exceeds staging disk budget")
	}
	n, err := s.buffer.Write(p)
	historyStagingBytes.Add(int64(n) - size)
	s.bytes += int64(n)
	return n, err
}
