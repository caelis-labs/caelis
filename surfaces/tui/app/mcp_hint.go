package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const mcpStartupHintLifetime = 8 * time.Second

func isMCPStartupNotice(env eventstream.Envelope) bool {
	return eventstream.IsSessionNotice(env) && env.NoticeKind == eventstream.NoticeKindMCPStartupFailed
}

func (m *Model) showMCPStartupHint(env eventstream.Envelope) tea.Cmd {
	// Startup availability is secondary to current work and approval. The MCP
	// status remains queryable even when this short-lived hint is suppressed.
	if m.turnRunning() || m.activePrompt != nil {
		return nil
	}
	lifetime := mcpStartupHintLifetime
	if !env.OccurredAt.IsZero() {
		lifetime = time.Until(env.OccurredAt.Add(lifetime))
		if lifetime <= 0 {
			return nil
		}
		if lifetime > mcpStartupHintLifetime {
			lifetime = mcpStartupHintLifetime
		}
	}
	for _, entry := range m.hintEntries {
		if entry.text == env.Notice {
			return nil
		}
	}
	return m.showHint(env.Notice, hintOptions{priority: HintPriorityLow, clearOnMessage: true, clearAfter: lifetime})
}
