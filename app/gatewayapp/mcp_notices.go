package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// The Session feed is observation only. Initialization failures never append a
// canonical Session event, change Turn outcome, or enter model context.
func (s *runtimeComposition) mcpFailureNotifier() func(mcp.ServerFailure) {
	if s.activation == nil || s.authorities.controlFeeds == nil {
		return nil
	}
	ref := s.activation.sessionRef
	feed, err := s.authorities.controlFeeds.Session(ref)
	if err != nil || feed == nil {
		return nil
	}
	return func(failure mcp.ServerFailure) {
		_ = feed.Publish(eventstream.Envelope{
			Kind: eventstream.KindNotice, SessionID: ref.SessionID, Scope: eventstream.ScopeMain,
			OccurredAt: time.Now(), Notice: mcpFailureNotice(failure),
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		})
	}
}

func mcpFailureNotice(failure mcp.ServerFailure) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, failure.Name)
	if runes := []rune(name); len(runes) > 80 {
		name = string(runes[:80]) + "…"
	}
	if errors.Is(failure.Err, context.DeadlineExceeded) {
		return fmt.Sprintf("MCP server %q did not initialize within 30 seconds. Its tools are unavailable; the conversation can continue.", name)
	}
	return fmt.Sprintf("MCP server %q could not initialize. Its tools are unavailable; check its configuration and connection. The conversation can continue.", name)
}
