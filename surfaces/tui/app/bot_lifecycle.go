package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// setBotRunStatus projects the selected conversation's Control lifecycle into
// Bot chrome. Only canonical lifecycle and reconnect snapshots supersede the
// previous reply, not provisional command activity. sessionViewMessage fences
// their asynchronous delivery by view generation.
func (m *Model) setBotRunStatus(sessionID, status string) {
	if m == nil || m.bot == nil || !m.bot.hasActive {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || sessionID != m.botActiveSessionID() || sessionID != strings.TrimSpace(m.currentSessionID) {
		return
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status == eventstream.LifecycleStateCancelled || status == "canceled" {
		status = eventstream.LifecycleStateInterrupted
	}
	m.bot.runStatus = status
}
