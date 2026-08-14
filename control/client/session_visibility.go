package controlclient

import (
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func userVisibleSessionSummaries(summaries []session.SessionSummary) []session.SessionSummary {
	if len(summaries) == 0 {
		return nil
	}
	visible := make([]session.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if sessionvisibility.IsSystemManagedSummary(summary) {
			continue
		}
		visible = append(visible, summary)
	}
	return session.CloneSessionSummaries(visible)
}
