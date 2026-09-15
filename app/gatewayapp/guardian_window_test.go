package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func guardianSource(seq uint64, kind session.EventType, text string) *session.Event {
	e := &session.Event{ID: fmt.Sprint(seq), Seq: seq, SessionID: "parent", Type: kind, Visibility: session.VisibilityCanonical}
	if kind == session.EventTypeUser {
		e.Actor = session.ActorRef{Kind: session.ActorKindUser}
		m := model.NewTextMessage(model.RoleUser, text)
		e.Message = &m
		e.Text = text
	} else {
		e.Actor = session.ActorRef{Kind: session.ActorKindController}
		e.Tool = &session.EventTool{ID: fmt.Sprintf("call-%d", seq), Name: "RunCommand", Input: map[string]any{"command": text}}
	}
	return e
}
func guardianWindowRequest(t *testing.T, key string) kernel.ApprovalReviewRequest {
	_, active := newApprovalReviewerTestSession(t, t.Context())
	req := approvalReviewerTestRequest(active, &approvalReviewerFakeModel{contextWindowTokens: 128000}, "inspect", map[string]any{"command": "exact current"})
	req.ReviewID = key
	return req
}
func TestGuardianWindowAppendsAndDoesNotRepeatOperations(t *testing.T) {
	req := guardianWindowRequest(t, "T1")
	source := []*session.Event{guardianSource(1, session.EventTypeUser, "A")}
	for i := uint64(2); i <= 6; i++ {
		source = append(source, guardianSource(i, session.EventTypeToolCall, fmt.Sprint(i)))
	}
	out, items, err := guardianWindow(guardianConversationSnapshot{ParentEvents: source}, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := guardianEventsText(out)
	for _, id := range []string{"call-2", "call-3", "call-4", "call-5", "call-6"} {
		if strings.Count(text, id) != 1 {
			t.Fatalf("missing or repeated %s", id)
		}
	}
	snapshot := guardianConversationSnapshot{Events: out, ParentCursor: items.ParentCursor, Version: 1, ParentEvents: append(source, guardianSource(7, session.EventTypeUser, "B steering"), guardianSource(8, session.EventTypeToolCall, "new"))}
	req.ReviewID = "T2"
	next, _, err := guardianWindow(snapshot, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, next[:len(out)]) {
		t.Fatal("unchanged source prefix was rebuilt")
	}
	if strings.Count(guardianEventsText(next), "call-6") != 1 || !strings.Contains(guardianEventsText(next), "call-8") {
		t.Fatal("delta repeated or lost an operation")
	}
	trimmed := guardianDropOldestTurn(next, "T2")
	if strings.Contains(guardianEventsText(trimmed), "call-2") || !strings.Contains(guardianEventsText(trimmed), "B steering") || !strings.Contains(guardianEventsText(trimmed), "A") {
		t.Fatal("turn trim lost independent users")
	}
	snapshot.Events = trimmed
	snapshot.ParentCursor = guardianParentCanonicalCursor{EventID: "8", EventSeq: 8}
	snapshot.Version = 2
	again, _, err := guardianWindow(snapshot, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(guardianEventsText(again), "call-2") {
		t.Fatal("trim rewound source cursor")
	}
}
func guardianEventsText(events []*session.Event) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(session.EventText(e))
		b.WriteByte('\n')
	}
	return b.String()
}
func TestGuardianUserBudgetFoldsLargestBeforeDroppingOldMessages(t *testing.T) {
	var events []*session.Event
	for i, text := range []string{"A short authorization", "B start" + strings.Repeat("long pasted data", 1000) + "B end", "C steering"} {
		e := guardianEvidenceEvent(text)
		e.Meta[guardianUserSource] = fmt.Sprint(i)
		events = append(events, e)
	}
	original := session.CloneEvents(events)
	got := guardianTrimUsers(events, 3000)
	if len(got) != 3 || !strings.Contains(session.EventText(got[1]), "folded") || !strings.Contains(session.EventText(got[1]), "B start") || !strings.Contains(session.EventText(got[1]), "B end") {
		t.Fatal("must fold long text before dropping normal users")
	}
	if !reflect.DeepEqual(got[0], original[0]) || !reflect.DeepEqual(got[2], original[2]) {
		t.Fatal("short users changed")
	}
	stable := session.CloneEvents(got)
	got = guardianTrimUsers(got, 3000)
	if !reflect.DeepEqual(stable, got) {
		t.Fatal("budgeted users drift between approvals")
	}
}
func TestGuardianProviderPrefixAndStableSchema(t *testing.T) {
	ctx := context.Background()
	service, active := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, active, session.EventTypeUser, model.RoleUser, "A user authorization")
	llm := &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`, `{"option_id":"allow_once"}`, `{"option_id":"allow_once"}`}}
	reviewer := newGuardianApprovalApprover(service)
	req := approvalReviewerTestRequest(active, llm, "one", map[string]any{"command": "one"})
	if _, err := reviewer.Decide(ctx, req); err != nil {
		t.Fatal(err)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, active, session.EventTypeUser, model.RoleUser, "B steering")
	req.Approval.Reason = "two"
	req.ReviewID = "review-two"
	if _, err := reviewer.Decide(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Approval.Options[0].ID = "different-id"
	spec1, _ := guardianOutputSpecForModel(llm, req.Approval)
	req.Approval.Options[0].ID = "allow_once"
	spec2, _ := guardianOutputSpecForModel(llm, req.Approval)
	if !reflect.DeepEqual(spec1, spec2) {
		t.Fatal("request options changed schema prefix")
	}
	requests := llm.Requests()
	if len(requests) != 2 {
		t.Fatalf("calls=%d; simple decisions must not query or summarize", len(requests))
	}
	a, b := requests[0], requests[1]
	if !reflect.DeepEqual(a.Instructions, b.Instructions) || !reflect.DeepEqual(a.Tools, b.Tools) || !reflect.DeepEqual(a.Output, b.Output) {
		t.Fatal("fixed prefix changed")
	}
	if !reflect.DeepEqual(a.Messages, b.Messages[:len(a.Messages)]) {
		aa, _ := json.Marshal(a.Messages)
		bb, _ := json.Marshal(b.Messages)
		t.Fatalf("message prefix changed:\n%s\n%s", aa, bb)
	}
	if len(a.Tools) != 3 {
		t.Fatalf("tools=%d", len(a.Tools))
	}
	text := ""
	for _, m := range b.Messages {
		text += m.TextContent() + "\n"
	}
	if strings.Index(text, "B steering") < strings.Index(text, `{"option_id":"allow_once"}`) {
		t.Fatal("steering moved before completed turn")
	}
}
func TestGuardianPolicyOptionalEvidenceAndNetwork(t *testing.T) {
	for _, text := range []string{"Additional retrieval and evidence gathering are optional", "Do not search Session transcripts or reconstruct task history", "Only temporary directories are writable", "network policy is inherited from the main Agent"} {
		if !strings.Contains(guardianPolicyPrompt(), text) {
			t.Fatalf("policy missing %q", text)
		}
	}
}

func TestGuardianTurnBudgetTrimsWithHeadroom(t *testing.T) {
	var events []*session.Event
	for i := 0; i < 8; i++ {
		e := guardianEvidenceEvent(strings.Repeat("x", 1000))
		e.Meta[guardianTurnKey] = fmt.Sprint(i)
		events = append(events, e)
	}
	got := guardianTrimTurns(events, 7000, "active")
	if len(got) > 4 {
		t.Fatalf("trim retained %d turns", len(got))
	}
	before := session.CloneEvents(got)
	added := guardianEvidenceEvent("small next turn")
	added.Meta[guardianTurnKey] = "active"
	next := guardianTrimTurns(append(got, added), 7000, "next")
	if !reflect.DeepEqual(before, next[:len(before)]) {
		t.Fatal("first append after trimming broke prefix again")
	}
}

func TestGuardianForkJoinsWholeTurnsOnceInModelOrder(t *testing.T) {
	m := newGuardianConversationManager()
	source := []*session.Event{guardianSource(1, session.EventTypeUser, "A")}
	first := guardianConversationForkRef{Key: "step", Index: 0, CallCount: 2}
	second := first
	second.Index = 1
	base, err := m.fork("parent", first, source)
	if err != nil {
		t.Fatal(err)
	}
	prefix := guardianEvidenceEvent("A")
	prefix.Meta[guardianUserSource] = "1"
	commit := func(ref guardianConversationForkRef) {
		t.Helper()
		user := guardianUserEvent(session.Session{}, fmt.Sprintf("approval-%d", ref.Index))
		assistant := &session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, `{"option_id":"allow_once"}`))}
		toolEvent := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: fmt.Sprintf("query-%d", ref.Index)}, Message: ptrMessage(model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(fmt.Sprintf("query-%d", ref.Index), "Read", map[string]any{"evidence": "x"}, false)))}
		ok, _, err := m.commitValidated(guardianConversationCommit{SessionID: "parent", ExpectedVersion: base.Version, Fork: ref, TurnID: fmt.Sprint(ref.Index), ParentCursor: guardianParentCanonicalCursor{EventID: "1", EventSeq: 1}, PrefixEvents: []*session.Event{prefix}, User: user, Assistant: assistant, ContextEvents: []*session.Event{user, {Type: session.EventTypeAssistant, Message: ptrMessage(model.NewMessage(model.RoleAssistant, model.NewToolUsePart(fmt.Sprintf("query-%d", ref.Index), "Read", json.RawMessage(`{"path":"history"}`))))}, toolEvent, assistant}})
		if err != nil || !ok {
			t.Fatalf("commit=%v err=%v", ok, err)
		}
	}
	commit(second)
	pinned, err := m.fork("parent", first, source)
	if err != nil || !reflect.DeepEqual(base, pinned) {
		t.Fatal("sibling modified pinned prefix")
	}
	commit(first)
	joined, err := m.snapshot("parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(joined.Events) != 9 || joined.Version != 2 || guardianTurn(joined.Events[1]) != "0" || guardianTurn(joined.Events[5]) != "1" {
		t.Fatalf("wrong fork join: %#v", joined)
	}
	trimmed := guardianDropOldestTurn(joined.Events, "next")
	if len(trimmed) != 5 || !guardianIsUser(trimmed[0]) || guardianTurn(trimmed[1]) != "1" {
		t.Fatal("trim split a tool turn")
	}
}

func TestGuardianExactActionPreservesOpaqueACPContent(t *testing.T) {
	req := guardianWindowRequest(t, "opaque")
	req.Approval.RawInput = nil
	req.RuntimeRequest.Call.Input = nil
	req.Approval.ToolTitle = "Overwrite other.txt"
	req.Approval.Content = []session.ProtocolToolCallContent{{Type: "content", Content: session.ProtocolTextContent("Set other.txt to empty")}}
	req.Approval.RawOutput = map[string]any{"diagnostic": "Operation not permitted"}
	got, large, err := guardianPlannedActionJSON(req)
	if err != nil || large {
		t.Fatalf("action: %v large=%v", err, large)
	}
	for _, want := range []string{"Overwrite other.txt", "Set other.txt to empty", "Operation not permitted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing exact action content %q: %s", want, got)
		}
	}
}

func TestGuardianReusedCallIDsRemainDistinctSourceOperations(t *testing.T) {
	req := guardianWindowRequest(t, "current")
	first := guardianSource(1, session.EventTypeToolCall, "first")
	next := guardianSource(2, session.EventTypeToolCall, "second")
	next.Tool.ID = first.Tool.ID
	initial, items, err := guardianWindow(guardianConversationSnapshot{ParentEvents: []*session.Event{first}}, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := guardianWindow(guardianConversationSnapshot{Events: initial, ParentCursor: items.ParentCursor, ParentEvents: []*session.Event{first, next}}, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := guardianEventsText(out); !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Fatalf("reused ID lost operation: %s", text)
	}
	// Approval history never changes how canonical calls are projected.
	pending := guardianEvidenceEvent("previous approval")
	out, _, err = guardianWindow(guardianConversationSnapshot{Events: []*session.Event{pending}, ParentEvents: []*session.Event{first, next}}, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := guardianEventsText(out); !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Fatalf("approval changed source projection: %s", text)
	}
}
