package projection

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestJournalNeverProjectsClientUsageOrLifecycle(t *testing.T) {
	receipt := session.NewModelInvocationReceipt(model.Invocation{ID: "attempt", Provider: "test", Model: "test", Usage: model.Usage{TotalTokens: 4324}, Outcome: "completed"}, "chat")
	for name, event := range map[string]*session.Event{
		"invocation": receipt,
		"execution":  {Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Lifecycle: &session.EventLifecycle{Status: "completed"}, Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindTurn}},
		"pause":      {Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Lifecycle: &session.EventLifecycle{Status: "resolved"}, Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken}},
		"tool":       {Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Lifecycle: &session.EventLifecycle{Status: "failed"}, Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindToolExecution}},
		"watchdog":   {Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Lifecycle: &session.EventLifecycle{Status: "agent_watchdog_checkpoint"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ProjectSessionEventEnvelope(eventstream.Envelope{}, event); len(got) != 0 {
				t.Fatalf("journal projected: %#v", got)
			}
			if got := ProjectSessionEventLiveSupplementEnvelope(eventstream.Envelope{}, event, agent.PublishedAssistantMessage); len(got) != 0 {
				t.Fatalf("journal supplement projected: %#v", got)
			}
		})
	}
}
