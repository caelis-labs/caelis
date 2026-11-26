package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"reflect"
	"strings"
	"testing"
)

func TestStructuredDiffCloneAndGenericRestore(t *testing.T) {
	t.Parallel()
	oldPath := " old.txt "
	changes := []ProtocolDiffChange{{Operation: "move", Path: " new.txt ", OldPath: &oldPath, Meta: map[string]any{"nested": map[string]any{"value": "original"}}}}
	input := []ProtocolToolCallContent{{Type: "diff", Changes: &changes, Patch: &ProtocolDiffPatch{Meta: map[string]any{"nested": map[string]any{"value": "original"}}, Format: "git_patch", Text: "-old\r\n+new\r\n"}, Meta: map[string]any{"nested": map[string]any{"value": "original"}}}}
	cloned := CloneProtocolToolCallContent(input)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	restored := ProtocolToolCallContentOf(&ProtocolUpdate{Content: generic})
	if !reflect.DeepEqual(restored, input) {
		t.Fatalf("generic restore = %#v, want %#v", restored, input)
	}
	changes[0].Path = "mutated"
	oldPath = "mutated"
	changes[0].Meta["nested"].(map[string]any)["value"] = "mutated"
	input[0].Patch.Text = "mutated"
	input[0].Patch.Meta["nested"].(map[string]any)["value"] = "mutated"
	input[0].Meta["nested"].(map[string]any)["value"] = "mutated"
	if (*cloned[0].Changes)[0].Path != " new.txt " || *(*cloned[0].Changes)[0].OldPath != " old.txt " || cloned[0].Patch.Meta["nested"].(map[string]any)["value"] != "original" || cloned[0].Patch.Text != "-old\r\n+new\r\n" || (*cloned[0].Changes)[0].Meta["nested"].(map[string]any)["value"] != "original" || cloned[0].Meta["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("clone shares structured diff state: %#v", cloned)
	}
}

func TestDiffPatchAnnotationsSurviveNativeAndProtocolCanonicalRoundTrips(t *testing.T) {
	patch := &ProtocolDiffPatch{Format: "git_patch", Text: "exact patch", Meta: map[string]any{"large": json.Number("9007199254740993"), "huge": json.Number("1e400")}}
	for _, native := range []bool{false, true} {
		event := Event{Type: EventTypeToolResult}
		if native {
			event.Tool = &EventTool{ID: "tool", Content: []EventToolContent{{Type: "diff", Patch: patch}}}
		} else {
			event.Protocol = &EventProtocol{Update: &ProtocolUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Content: []ProtocolToolCallContent{{Type: "diff", Patch: patch}}}}
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var restored Event
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		var got *ProtocolDiffPatch
		if native {
			got = restored.Tool.Content[0].Patch
		} else {
			got = ProtocolToolCallContentOf(restored.Protocol.Update)[0].Patch
		}
		if !reflect.DeepEqual(got, patch) {
			t.Fatalf("native=%v: patch %#v, want %#v", native, got, patch)
		}
	}
}

func sharedMediaEvent(t testing.TB, size int) Event {
	image := tool.Image{Data: []byte(strings.Repeat("abc", size/4)), MIMEType: "image/png", Name: "screen"}
	message := model.Message{Role: model.RoleTool, Parts: []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "call", Name: "screen", Content: tool.ModelParts([]tool.ContentBlock{image})}}}}
	return Event{Schema: EventSchemaVersion, Type: EventTypeToolResult, Message: &message, Tool: &EventTool{ID: "call", Name: "screen", Result: &tool.Result{Content: []tool.ContentBlock{image}}}}
}
func TestEventMediaSharesPayloadAndCompleteResult(t *testing.T) {
	original := sharedMediaEvent(t, 64<<10)
	before, _ := json.Marshal(original)
	raw, err := MarshalEventJSON(&original)
	if err != nil {
		t.Fatal(err)
	}
	data := original.Message.ToolResults()[0].Content[0].Media.Source.Data
	if bytes.Count(raw, []byte(data)) != 1 {
		t.Fatal("media not shared")
	}
	var got Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, got) {
		t.Fatal("full result or model view changed")
	}
	after, _ := json.Marshal(original)
	if !bytes.Equal(before, after) {
		t.Fatal("encoding mutated source")
	}
	for _, name := range []string{"missing", "digest", "unused", "future"} {
		t.Run(name, func(t *testing.T) {
			var object map[string]json.RawMessage
			_ = json.Unmarshal(raw, &object)
			switch name {
			case "missing":
				delete(object, "media")
			case "digest":
				object["media"] = json.RawMessage(`{"wrong":"payload"}`)
			case "unused":
				object["media"] = json.RawMessage(fmt.Sprintf(`{"%s":"unused"}`, eventMediaID("unused")))
			case "future":
				object["schema"] = json.RawMessage(`999`)
			}
			bad, _ := json.Marshal(object)
			if _, err := MigrateEventJSON(bad); err != nil {
				return
			}
			var event Event
			if err := json.Unmarshal(bad, &event); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}
func TestRetiredJournalMediaIsConsumedBeforeIgnoringExecution(t *testing.T) {
	event := sharedMediaEvent(t, 8192)
	raw, err := MarshalEventJSON(&event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(raw, &object)
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(object["tool"], &payload)
	object["journal"] = json.RawMessage(fmt.Sprintf(`{"schema":1,"kind":"tool_execution","tool_execution":{"schema":1,"result":%s}}`, payload["result"]))
	delete(object, "tool")
	object["schema"] = json.RawMessage(`2`)
	raw, _ = json.Marshal(object)
	migrated, err := MigrateEventJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(migrated, &got); err != nil {
		t.Fatal(err)
	}
	if got.Tool != nil || got.Approval != nil || !reflect.DeepEqual(got.Message, event.Message) {
		t.Fatal("legacy message media changed")
	}
	again, _ := json.Marshal(got)
	if bytes.Contains(again, []byte(`"journal"`)) {
		t.Fatal("retired execution was rewritten")
	}
	// A reference only in the retired receipt must also be consumed.
	delete(object, "message")
	raw, _ = json.Marshal(object)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// The old body is opaque once the live fields have no table to resolve.
	delete(object, "media")
	raw, _ = json.Marshal(object)
	if err := json.Unmarshal(raw, &got); err != nil || got.Tool != nil {
		t.Fatalf("discarded media became a live dependency: %v", err)
	}
}

func TestLegacyToolReceiptRestoresEventFields(t *testing.T) {
	var event EventTool
	const old = `{"id":"original","name":"lookup","result":{"id":"other","turn_complete":true,"is_error":true,"metadata":{"caelis.mcp.discovery.v1":true,"application_call":{"result_format":"content-v1"}},"content":[{"type":"text","text":"old"}]}}`
	if err := json.Unmarshal([]byte(old), &event); err != nil {
		t.Fatal(err)
	}
	if event.ID != "original" || event.Outcome != ToolOutcomeFailed || event.Discovery || !event.ResultIncludesReceipt || event.Result.Content[0] != tool.Text("old") {
		t.Fatalf("restored event = %+v", event)
	}
	raw, err := json.Marshal(event.Result)
	if err != nil || string(raw) != `{"content":[{"type":"text","text":"old"}]}` {
		t.Fatalf("restored result = %s, %v", raw, err)
	}
}

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

	full := PageEvents(events, EventPageRequest{Limit: 10, Visibility: EventPageClientReplay})
	if len(full.Events) != 3 || full.Events[0].ID != "canonical-1" || full.Events[1].ID != "mirror-2" || full.Events[2].ID != "canonical-4" {
		t.Fatalf("client replay page = %#v, want canonical+mirror only", full)
	}
}

func TestClientReplaySkipsUnprojectedAgentContext(t *testing.T) {
	t.Parallel()

	event := &Event{
		ID: "agent-context", Seq: 2, Type: EventTypeContext,
		Visibility: VisibilityCanonical,
		Actor:      ActorRef{Kind: ActorKindParticipant, ID: "reviewer"},
		Text:       "Subagent @reviewer is completed.",
	}
	if IsClientReplayEvent(event) {
		t.Fatalf("IsClientReplayEvent(%#v) = true, want model-only Context excluded", event)
	}
	if got := PageEvents([]*Event{event}, EventPageRequest{Limit: 10, Visibility: EventPageClientReplay}); len(got.Events) != 0 {
		t.Fatalf("client replay page = %#v, want model-only Context excluded", got.Events)
	}

	event.Protocol = &EventProtocol{
		Method: ProtocolMethodSessionUpdate,
		Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
			Content:       ProtocolTextContent("projected context"),
		},
	}
	if !IsClientReplayEvent(event) {
		t.Fatalf("IsClientReplayEvent(%#v) = false, want explicit protocol Context retained", event)
	}
}

func TestValidateEventChildOriginRequiresDurableRelation(t *testing.T) {
	valid := EventChildOrigin{
		Scope:         EventChildScopeSubagent,
		ScopeID:       "task-1",
		TaskID:        "task-1",
		SourceEventID: "child-session:7",
		ParentTool:    EventParentTool{CallID: "spawn-1", Name: "StartThread"},
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

func TestEventToolReadsLegacyFailureIntoOutcome(t *testing.T) {
	const raw = `{"result":{"is_error":true,"content":[{"kind":"text","text":{"text":"failed"}}]}}`
	var decoded EventTool
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome != ToolOutcomeFailed || len(decoded.Result.Content) != 1 {
		t.Fatalf("legacy failure lost: %+v", decoded)
	}
	const knownEffect = `{"outcome":"succeeded","result":{"is_error":true}}`
	if err := json.Unmarshal([]byte(knownEffect), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome != ToolOutcomeSucceeded {
		t.Fatalf("legacy result overrode outcome: %+v", decoded)
	}
}

func TestEventToolUnmarshalPreservesOnlyInputNumbers(t *testing.T) {
	t.Parallel()
	var event Event
	if err := json.Unmarshal([]byte(`{"type":"tool_result","tool":{"input":{"decimal":9007199254740991.1,"integer":900719925474099312345678901234567890,"exponent":1e1000},"output":{"value":12.5}},"notice":{"meta":{"value":12.5}},"_meta":{"value":900719925474099312345678901234567890}}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.Tool == nil {
		t.Fatal("missing decoded tool payload")
	}
	for key, want := range map[string]json.Number{
		"decimal":  "9007199254740991.1",
		"integer":  "900719925474099312345678901234567890",
		"exponent": "1e1000",
	} {
		if got, ok := event.Tool.Input[key].(json.Number); !ok || got != want {
			t.Fatalf("tool input[%q] = %#v, want exact JSON number %s", key, event.Tool.Input[key], want)
		}
	}
	if got, ok := event.Tool.Output["value"].(float64); !ok || got != 12.5 {
		t.Fatalf("tool output value = %#v, want float64(12.5)", event.Tool.Output["value"])
	}
	if got, ok := event.Notice.Meta["value"].(float64); !ok || got != 12.5 {
		t.Fatalf("notice metadata value = %#v, want float64(12.5)", event.Notice.Meta["value"])
	}
	if got := event.Meta["value"]; got != json.Number("900719925474099312345678901234567890") {
		t.Fatalf("durable metadata value = %#v, want existing json.Number behavior", got)
	}
}

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
