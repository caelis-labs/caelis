package session

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestPageEventsClientReplayIncludesCanonicalAndMirrorOnly(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	assistant := model.NewTextMessage(model.RoleAssistant, "done")
	events := []*Event{
		{ID: "canonical-1", Seq: 1, Type: EventTypeUser, Visibility: VisibilityCanonical, Message: &user},
		{ID: "mirror-2", Seq: 2, Type: EventTypeAssistant, Visibility: VisibilityMirror, Protocol: &EventProtocol{Method: ProtocolMethodSessionUpdate, Update: &ProtocolUpdate{SessionUpdate: string(ProtocolUpdateTypeAgentMessage), Content: ProtocolTextContent("child")}}},
		{ID: "journal-3", Seq: 3, Type: EventTypeLifecycle, Visibility: VisibilityJournal, Lifecycle: &EventLifecycle{Status: "prepared"}},
		{ID: "canonical-4", Seq: 4, Type: EventTypeAssistant, Visibility: VisibilityCanonical, Message: &assistant},
	}

	first := PageEvents(events, EventPageRequest{Limit: 2, Visibility: EventPageClientReplay})
	if len(first.Events) != 2 || first.Events[0].ID != "canonical-1" || first.Events[1].ID != "mirror-2" || !first.HasMore || first.NextSeq != 3 {
		t.Fatalf("first page = %#v, want canonical+mirror through skipped journal seq 3", first)
	}
	second := PageEvents(events, EventPageRequest{AfterSeq: first.NextSeq, Limit: 2, Visibility: EventPageClientReplay})
	if len(second.Events) != 1 || second.Events[0].ID != "canonical-4" || second.HasMore || second.NextSeq != 4 {
		t.Fatalf("second page = %#v, want canonical-4", second)
	}

	filtered := FilterClientReplayEvents(events)
	if len(filtered) != 3 || filtered[0].ID != "canonical-1" || filtered[1].ID != "mirror-2" || filtered[2].ID != "canonical-4" {
		t.Fatalf("FilterClientReplayEvents() = %#v", filtered)
	}
}

func TestValidateEventChildOriginRequiresDurableRelation(t *testing.T) {
	valid := EventChildOrigin{
		Scope:         EventChildScopeSubagent,
		ScopeID:       "task-1",
		TaskID:        "task-1",
		SourceEventID: "child-session:7",
		ParentTool:    EventParentTool{CallID: "spawn-1", Name: "Spawn"},
	}
	if err := ValidateEventChildOrigin(valid); err != nil {
		t.Fatalf("ValidateEventChildOrigin(valid) error = %v", err)
	}
	invalid := valid
	invalid.SourceEventID = ""
	if err := ValidateEventChildOrigin(invalid); err == nil {
		t.Fatal("ValidateEventChildOrigin() error = nil, want missing source identity failure")
	}
}
