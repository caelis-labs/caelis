package session

import (
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func pairCall(seq uint64, batch, occurrence, protocol string) *Event {
	call := model.ToolCall{ID: protocol, Name: "Tool", Args: "{}"}
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	return &Event{ID: batch, Seq: seq, Type: EventTypeToolCall, Message: &message, Scope: &EventScope{TurnID: "turn"}, Tool: &EventTool{ID: protocol, Name: "Tool", BatchID: batch, Execution: tool.InvocationContext{SessionID: "session", TurnID: "turn", ItemID: occurrence}}}
}
func pairResult(seq uint64, call *Event, text string) *Event {
	message := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(call.Tool.ID, call.Tool.Name, map[string]any{"value": text}, false))
	return &Event{ID: "result:" + call.ID, Seq: seq, Type: EventTypeToolResult, Scope: call.Scope, Message: &message, Tool: &EventTool{ID: call.Tool.ID, Name: call.Tool.Name, Execution: call.Tool.Execution}}
}
func TestToolFactsLateResultsUseOccurrenceWithReusedProviderID(t *testing.T) {
	first := pairCall(1, "batch-a", "occ-a", "same")
	second := pairCall(3, "batch-b", "occ-b", "same")
	userMessage := model.NewTextMessage(model.RoleUser, "later user")
	user := &Event{ID: "user", Seq: 2, Type: EventTypeUser, Message: &userMessage}
	lateA := pairResult(5, first, "A")
	lateB := pairResult(4, second, "B")
	events := []*Event{first, user, second, lateB, lateA}
	ordered, err := OrderToolFacts(events)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, event := range ordered {
		ids = append(ids, event.ID)
	}
	if !reflect.DeepEqual(ids, []string{"batch-a", "result:batch-a", "user", "batch-b", "result:batch-b"}) {
		t.Fatalf("order=%v", ids)
	}
	if events[4] != lateA || lateA.Seq != 5 {
		t.Fatal("read projection changed durable sequence")
	}
	// A tail below a committed checkpoint cannot recover an already covered call.
	tail, err := OrderToolFacts([]*Event{second, lateB, lateA})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 {
		t.Fatal("orphan result reintroduced covered call")
	}
}
func TestToolFactsLegacyHalfBatchAndAmbiguousID(t *testing.T) {
	first := pairCall(1, "legacy", "", "A")
	first.Tool.Execution = tool.InvocationContext{}
	first.Tool.BatchID = ""
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "A", Name: "Tool", Args: "{}"}, {ID: "B", Name: "Tool", Args: `{"large":9007199254740993123456789}`}}, "")
	first.Message = &message
	pairs, err := PairToolFacts([]*Event{first})
	if err != nil || len(pairs) != 2 {
		t.Fatalf("pairs=%v err=%v", pairs, err)
	}
	if pairs[0].Call.Tool.Execution.ItemID == pairs[1].Call.Tool.Execution.ItemID || pairs[1].Call.Tool.Input["large"].(interface{ String() string }).String() != "9007199254740993123456789" {
		t.Fatal("half batch identity/input lost")
	}
	second := pairCall(2, "legacy-b", "", "A")
	second.Tool.Execution = tool.InvocationContext{}
	second.Tool.BatchID = ""
	result := pairResult(3, first, "unknown legacy owner")
	result.Tool.Execution = tool.InvocationContext{}
	if _, err := PairToolFacts([]*Event{first, second, result}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous legacy ID guessed: %v", err)
	}
}

func TestLegacyGroupedResultsRejectConflictingFacts(t *testing.T) {
	first := pairCall(1, "batch", "", "a")
	batch := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "a", Name: "Tool", Args: "{}"}, {ID: "b", Name: "Tool", Args: "{}"}}, "")
	first.Message = &batch
	result := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("b", "Tool", nil, false), model.NewToolResultJSONPart("a", "Tool", nil, false))
	grouped := &Event{ID: "result", Seq: 2, Type: EventTypeToolResult, Scope: first.Scope, Message: &result}
	pairs, err := PairToolFacts([]*Event{first, grouped})
	if err != nil || len(pairs) != 2 {
		t.Fatalf("pairs=%v, err=%v", pairs, err)
	}
	other := pairResult(3, pairs[0].Call, "another effect")
	other.Tool.Outcome = ToolOutcomeUnknown
	other.Tool.ErrorCode = "execution_interrupted"
	if _, err := PairToolFacts([]*Event{first, grouped, other}); err == nil {
		t.Fatal("conflicting occurrence result accepted")
	}
	orphan := model.NewToolResultJSONPart("absent", "Tool", nil, false)
	result.Parts = append(result.Parts, orphan)
	if _, err := OrderToolFacts([]*Event{first, grouped}); err == nil {
		t.Fatal("orphan outcome included in original batch")
	}
}
