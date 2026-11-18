package gatewayapp

import (
	"context"
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
	if kind == session.EventTypeUser || kind == session.EventTypeAssistant {
		actor, role := session.ActorKindUser, model.RoleUser
		if kind == session.EventTypeAssistant {
			actor, role = session.ActorKindController, model.RoleAssistant
		}
		e.Actor = session.ActorRef{Kind: actor}
		m := model.NewTextMessage(role, text)
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

func guardianWindowProjection(events ...*session.Event) (projected []*session.Event, cursor guardianParentCanonicalCursor) {
	for _, event := range events {
		cursor = guardianParentCanonicalCursor{EventID: event.ID, EventSeq: event.Seq}
		if item := guardianProjectEvent(event); item != nil {
			projected = append(projected, item)
		}
	}
	return projected, cursor
}

func TestGuardianWindowAppendsUsersAndNeverProjectsOperations(t *testing.T) {
	req := guardianWindowRequest(t, "T1")
	source := []*session.Event{guardianSource(1, session.EventTypeUser, "A")}
	for i := uint64(2); i <= 6; i++ {
		source = append(source, guardianSource(i, session.EventTypeToolCall, fmt.Sprint(i)))
	}
	projected, cursor := guardianWindowProjection(source...)
	out, items, err := guardianWindow(guardianConversationSnapshot{ParentEvents: projected, SourceCursor: cursor}, req, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !guardianIsUser(out[0]) || items.ParentCursor.EventSeq != 6 {
		t.Fatalf("want one projected user and consumed source cut, got %q, %+v", guardianEventsText(out), items)
	}
	projected, cursor = guardianWindowProjection(append(source, guardianSource(7, session.EventTypeUser, "B steering"), guardianSource(8, session.EventTypeToolCall, "new"))...)
	snapshot := guardianConversationSnapshot{Events: out, ParentCursor: items.ParentCursor, Version: 1, ParentEvents: projected, SourceCursor: cursor}
	req.ReviewID = "T2"
	next, _, err := guardianWindow(snapshot, req, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, next[:len(out)]) {
		t.Fatal("unchanged source prefix was rebuilt")
	}
	if len(next) != 2 || !strings.Contains(session.EventText(next[1]), "B steering") {
		t.Fatalf("user delta repeated or lost: %q", guardianEventsText(next))
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
	appendApprovalReviewerTextEvent(t, ctx, service, active, session.EventTypeAssistant, model.RoleAssistant, "Can I inspect the next file?")
	appendApprovalReviewerTextEvent(t, ctx, service, active, session.EventTypeUser, model.RoleUser, "B steering")
	req.Approval.Reason = "two"
	req.ReviewID = "review-two"
	if _, err := reviewer.Decide(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Approval.Reason, req.ReviewID = "three", "review-three"
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
	if len(requests) != 3 {
		t.Fatalf("calls=%d; simple decisions must not query or summarize", len(requests))
	}
	a, b := requests[0], requests[2]
	if !reflect.DeepEqual(a.Instructions, b.Instructions) || !reflect.DeepEqual(a.Tools, b.Tools) || !reflect.DeepEqual(a.Output, b.Output) {
		t.Fatal("fixed prefix changed")
	}
	if len(b.Messages) != 8 || !reflect.DeepEqual(a.Messages, b.Messages[:len(a.Messages)]) ||
		b.Messages[3].Role != model.RoleAssistant || !strings.Contains(b.Messages[3].TextContent(), "Can I inspect the next file?") ||
		!strings.Contains(b.Messages[4].TextContent(), "B steering") ||
		!reflect.DeepEqual(requests[1].Messages, b.Messages[:len(requests[1].Messages)]) {
		t.Fatal("adding a new user message changed the cached prefix or the approval order")
	}
	if len(a.Tools) != 3 {
		t.Fatalf("tools=%d", len(a.Tools))
	}
}
func TestGuardianPolicyOptionalEvidenceAndNetwork(t *testing.T) {
	for _, text := range []string{"Additional retrieval and evidence gathering are optional", "Do not search Session transcripts or reconstruct task history", "Only temporary directories are writable", "network policy is inherited from the main Agent"} {
		if !strings.Contains(guardianPolicyPrompt(), text) {
			t.Fatalf("policy missing %q", text)
		}
	}
}

func guardianReviewFixtures(turns int, payload string) []*session.Event {
	var events []*session.Event
	for i := range turns {
		request := guardianEvidenceEvent(fmt.Sprintf("review-%d %s", i, payload))
		answer := guardianEvidenceEvent(`{"option_id":"allow_once"}`)
		answer.Type = session.EventTypeAssistant
		answer.Message = ptrMessage(model.NewTextMessage(model.RoleAssistant, answer.Text))
		request.Meta[guardianTurnKey], answer.Meta[guardianTurnKey] = fmt.Sprint(i), fmt.Sprint(i)
		events = append(events, request, answer)
	}
	return events
}

func TestGuardianHistoryRotatesOnCountAndTokenBudget(t *testing.T) {
	req := guardianWindowRequest(t, "review")
	user := guardianProjectEvent(guardianSource(1, session.EventTypeUser, strings.Repeat("User paste text ", 10000)))
	events := guardianReviewFixtures(guardianReviewHighTurns, "")
	below, fits := guardianFitHistory(append([]*session.Event{user}, events[:len(events)-2]...), req.Model, "current", nil, true)
	if !fits || len(below) != 1+(guardianReviewHighTurns-1)*2 || !reflect.DeepEqual(below[0], user) {
		t.Fatal("users or reviews changed before the rotation")
	}
	rotated, fits := guardianFitHistory(append([]*session.Event{user}, events...), req.Model, "current", nil, true)
	if !fits || len(rotated) != 1+guardianReviewKeepTurns*2 ||
		!strings.Contains(session.EventText(rotated[0]), "folded") ||
		!reflect.DeepEqual(rotated[1:], events[len(events)-guardianReviewKeepTurns*2:]) {
		t.Fatal("count trigger must trim users and keep exactly the newest complete pairs")
	}
	large := guardianReviewFixtures(10, strings.Repeat("x", 20000))
	rotated, fits = guardianFitHistory(large, req.Model, "current", nil, true)
	tokens, turns := guardianHistoryUsage(rotated)
	if !fits || turns == 0 || turns >= guardianReviewKeepTurns || tokens > guardianReviewTokens ||
		!reflect.DeepEqual(rotated, large[len(large)-len(rotated):]) {
		t.Fatalf("token trigger retained %d turns and %d tokens", turns, tokens)
	}
}

func TestGuardianRotationCoalescesOnlyNewlyAdjacentUsers(t *testing.T) {
	req := guardianWindowRequest(t, "review")
	early := guardianProjectEvent(guardianSource(1, session.EventTypeUser, "first user"))
	middle := guardianProjectEvent(guardianSource(2, session.EventTypeUser, "middle user"))
	recent := guardianProjectEvent(guardianSource(3, session.EventTypeUser, "recent user"))
	pairs := guardianReviewFixtures(guardianReviewHighTurns, "")
	start := len(pairs) - guardianReviewKeepTurns*2
	events := append([]*session.Event{early}, pairs[:start]...)
	events = append(events, middle)
	events = append(events, pairs[start:start+2]...)
	events = append(events, recent)
	events = append(events, pairs[start+2:]...)
	result, fits := guardianFitHistory(events, req.Model, "current", nil, true)
	if !fits || len(result) != 3+guardianReviewKeepTurns*2 || result[0] != early ||
		result[1] != middle || result[2] != pairs[start] || result[4] != recent {
		t.Fatal("rotation moved a user message past a retained review or failed to join adjacent users")
	}
}

func TestGuardianAssistantRotationKeepsRecentOrderedDialogue(t *testing.T) {
	req := guardianWindowRequest(t, "review")
	initial := guardianProjectEvent(guardianSource(1, session.EventTypeUser, "Inspect first."))
	events := []*session.Event{initial}
	for index := range guardianAssistantHighEntries + 1 {
		events = append(events, guardianProjectEvent(guardianSource(uint64(index+2), session.EventTypeAssistant, fmt.Sprintf("proposal %d", index))))
	}
	reply := guardianProjectEvent(guardianSource(uint64(len(events)+1), session.EventTypeUser, "Go ahead."))
	events = append(events, reply)
	result, fits := guardianFitHistory(events, req.Model, "current", nil, true)
	if !fits || len(result) != guardianAssistantKeepEntries+2 || result[0] != initial ||
		result[len(result)-2] != events[len(events)-2] || result[len(result)-1] != reply {
		t.Fatal("assistant rotation lost user authority or changed the newest proposal/reply order")
	}
}

func TestGuardianDialogueCursorKeepsPrefixAcrossTwoRotations(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Review the change.")
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	llm := &approvalReviewerFakeModel{contextWindowTokens: 128_000}
	var metrics []guardianReviewMetrics
	reviewer.observeReview = func(entry guardianReviewMetrics) { metrics = append(metrics, entry) }
	firstRotation := guardianReviewHighTurns
	secondRotation := firstRotation + guardianReviewHighTurns - guardianReviewKeepTurns
	proposals := map[int]string{1: "First main-session proposal.", firstRotation + 3: "Second main-session proposal."}
	for index := range secondRotation + 2 {
		if proposal := proposals[index]; proposal != "" {
			appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeAssistant, model.RoleAssistant, proposal)
		}
		req := approvalReviewerTestRequest(active, llm, fmt.Sprintf("ticket %04d", index), map[string]any{"command": "git status"})
		req.ReviewID = fmt.Sprintf("review-%d", index)
		if result, err := reviewer.Decide(t.Context(), req); err != nil || !result.Approved {
			t.Fatalf("review %d = %+v, %v", index, result, err)
		}
	}
	requests := llm.Requests()
	if len(requests) != secondRotation+2 || len(metrics) != len(requests) {
		t.Fatalf("got %d requests and %d review metrics", len(requests), len(metrics))
	}
	for index, request := range requests {
		rotate := index == firstRotation || index == secondRotation
		if metrics[index].ContextTrimmed != rotate {
			t.Fatalf("review %d: rotation = %t, want %t", index, metrics[index].ContextTrimmed, rotate)
		}
		if index > 0 && !rotate {
			before := requests[index-1]
			if len(request.Messages) <= len(before.Messages) || !reflect.DeepEqual(before.Messages, request.Messages[:len(before.Messages)]) ||
				!reflect.DeepEqual(before.Instructions, request.Instructions) || !reflect.DeepEqual(before.Tools, request.Tools) ||
				!reflect.DeepEqual(before.Output, request.Output) {
				t.Fatalf("review %d changed a stable request prefix", index)
			}
		}
		tickets := 0
		for _, message := range request.Messages {
			if strings.HasPrefix(message.TextContent(), "Approval request:\n") {
				tickets++
			}
		}
		if rotate && tickets != guardianReviewKeepTurns+1 {
			t.Fatalf("review %d: %d tickets after rotation, want %d", index, tickets, guardianReviewKeepTurns+1)
		}
		for appendedAt, proposal := range proposals {
			position, occurrences := -1, 0
			for at, message := range request.Messages {
				if message.Role == model.RoleAssistant && strings.Contains(message.TextContent(), proposal) {
					position, occurrences = at, occurrences+1
				}
			}
			want := 0
			if index >= appendedAt {
				want = 1
			}
			if occurrences != want {
				t.Fatalf("review %d: proposal from review %d appears %d times, want %d", index, appendedAt, occurrences, want)
			}
			if index == appendedAt && position+1 != len(request.Messages)-1 {
				t.Fatalf("review %d: new proposal did not append immediately before its current ticket", index)
			}
		}
	}
}

func TestGuardianSmallWindowUsesOnlyBudgetedUsersAndCurrentTicket(t *testing.T) {
	req := guardianWindowRequest(t, "small")
	req.Model = &approvalReviewerFakeModel{contextWindowTokens: 16 * 1024}
	source := guardianSource(1, session.EventTypeUser, "Inspect only. "+strings.Repeat("large paste ", 10000)+" Never delete.")
	proposal := guardianSource(2, session.EventTypeAssistant, "May I list the directory?")
	reply := guardianSource(3, session.EventTypeUser, "Go ahead.")
	projected, cursor := guardianWindowProjection(source, proposal, reply)
	history, items, err := guardianWindow(guardianConversationSnapshot{Events: guardianReviewFixtures(4, "old exact ticket"), ParentEvents: projected, SourceCursor: cursor}, req, nil, true)
	text := guardianEventsText(history)
	if err != nil || items.MandatoryInputTooLarge || !items.ContextTrimmed || len(history) != 3 || !guardianIsAssistantContext(history[1]) ||
		!strings.Contains(text, "Inspect only.") || !strings.Contains(text, "Never delete.") ||
		!strings.Contains(text, "folded") || !strings.Contains(items.Text, "exact current") {
		t.Fatalf("small window did not preserve action and user boundaries: events=%d, items=%+v, error=%v", len(history), items, err)
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
		ok, _, err := m.commitValidated(guardianConversationCommit{SessionID: "parent", ExpectedVersion: base.Version, Fork: ref, TurnID: fmt.Sprint(ref.Index), ParentCursor: guardianParentCanonicalCursor{EventID: "1", EventSeq: 1}, PrefixEvents: []*session.Event{prefix}, User: user, Assistant: assistant})
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
	if len(joined.Events) != 5 || joined.Version != 2 || guardianTurn(joined.Events[1]) != "0" || guardianTurn(joined.Events[3]) != "1" {
		t.Fatalf("wrong fork join: %#v", joined)
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
