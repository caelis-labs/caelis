package subagent

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/history"
)

// A replay is consumed into a bounded display window while ACP streams it.
// No second raw transcript is staged on disk. The current input group stays
// mutable until its sender footer has been interpreted.
type historyProjection struct{ transcript history.Transcript }

// Bound aggregate replay memory across independent Sessions and providers.
var historyReplaySlots = make(chan struct{}, 8)

func (c *historyCollector) openProjection() { c.projection = &historyProjection{} }

func (c *historyCollector) projectEvents() {
	if c.projection == nil || c.err != nil {
		return
	}
	for _, event := range c.events {
		c.projection.transcript.Append(event)
	}
	c.events = nil
	c.bytes, c.inputStart = 0, 0
}

func (c *historyCollector) streamEvents(ctx context.Context, yield func(*session.Event) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushUserText()
	c.projectEvents()
	if c.err != nil {
		return c.err
	}
	for _, event := range c.projection.transcript.Events() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(event); err != nil {
			return err
		}
	}
	return nil
}

func (c *historyCollector) closeProjection() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.projection = nil
}
