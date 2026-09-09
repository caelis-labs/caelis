package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianUserSource = "guardian_user_source"
const guardianTurnKey = "guardian_turn"
const guardianCallKey = "guardian_source_call"
const guardianPendingCallKey = "guardian_pending_source_call"
const guardianInitialCalls = 3

// guardianWindow prepares an append-only input sequence. Source users have a
// separate budget and survive removal of the surrounding completed turns.
func guardianWindow(snapshot guardianConversationSnapshot, req kernel.ApprovalReviewRequest, output *model.OutputSpec) ([]*session.Event, guardianPromptItems, error) {
	history := session.CloneEvents(snapshot.Events)
	items := guardianPromptItems{ParentCursor: snapshot.ParentCursor}
	// Source sequence owns identity. A call ID may be reused by an endpoint.
	// Only consume one not-yet-recorded call for each previous approval; retained
	// operation previews must not suppress later calls with the same ID.
	pending := map[string][]*session.Event{}
	for _, e := range history {
		if e != nil && e.Meta[guardianPendingCallKey] == true {
			if id, ok := e.Meta[guardianCallKey].(string); ok {
				pending[id] = append(pending[id], e)
			}
		}
	}
	skipped := map[uint64]bool{}
	currentSeq := guardianCurrentSourceCall(snapshot, req)
	for _, e := range snapshot.ParentEvents {
		if e == nil || e.Seq <= snapshot.ParentCursor.EventSeq || !session.IsCanonicalHistoryEvent(e) || session.EventTypeOf(e) != session.EventTypeToolCall || e.Tool == nil {
			continue
		}
		key := e.SessionID + ":" + e.Tool.ID
		if previous := pending[key]; len(previous) > 0 {
			delete(previous[0].Meta, guardianPendingCallKey)
			pending[key] = previous[1:]
			skipped[e.Seq] = true
		}
	}
	if currentSeq != 0 {
		skipped[currentSeq] = true
	}
	var calls []*session.Event
	for _, e := range snapshot.ParentEvents {
		if e == nil || !session.IsCanonicalHistoryEvent(e) {
			continue
		}
		if e.Seq <= snapshot.ParentCursor.EventSeq {
			continue
		}
		if e.Seq > items.ParentCursor.EventSeq {
			items.ParentCursor = guardianParentCanonicalCursor{EventID: e.ID, EventSeq: e.Seq}
		}
		if session.EventTypeOf(e) == session.EventTypeToolCall && session.IsMainInvocationVisibleEvent(e) && e.Tool != nil && !skipped[e.Seq] {
			calls = append(calls, e)
		}
	}
	firstCall := uint64(0)
	if len(calls) > guardianInitialCalls {
		firstCall = calls[len(calls)-guardianInitialCalls].Seq
	}
	for _, e := range snapshot.ParentEvents {
		if e == nil || e.Seq <= snapshot.ParentCursor.EventSeq || !session.IsCanonicalHistoryEvent(e) {
			continue
		}
		if session.EventTypeOf(e) == session.EventTypeUser && (e.Actor.Kind == session.ActorKindUser || e.Actor.Kind == "") {
			text := guardianVisibleText(e)
			if text == "" {
				continue
			}
			one := guardianEvidenceEvent(fmt.Sprintf("User message [session=%s seq=%d]:\n%s", e.SessionID, e.Seq, text))
			one.Meta[guardianUserSource] = fmt.Sprintf("%s:%d", e.SessionID, e.Seq)
			history = append(history, one)
		} else if session.EventTypeOf(e) == session.EventTypeToolCall && session.IsMainInvocationVisibleEvent(e) && e.Seq >= firstCall && e.Tool != nil && !skipped[e.Seq] {
			input, _ := json.Marshal(e.Tool.Input)
			text := fmt.Sprintf("Operation [session=%s seq=%d tool_call_id=%s tool=%s]\n%s", e.SessionID, e.Seq, e.Tool.ID, e.Tool.Name, guardianFold(string(input), 2048))
			one := guardianEvidenceEvent(text)
			one.Meta[guardianTurnKey] = req.ReviewID
			one.Meta[guardianCallKey] = e.SessionID + ":" + e.Tool.ID
			history = append(history, one)
		}
	}
	action, oversized, err := guardianPlannedActionJSON(req)
	if err != nil {
		return nil, items, err
	}
	options, hasOptions, optionsOversized, err := guardianApprovalOptionsJSON(req.Approval)
	if err != nil {
		return nil, items, err
	}
	if !hasOptions {
		return nil, items, fmt.Errorf("guardian requires normalized options")
	}
	items.Text = "Approval request:\n" + action + "\nOptions:\n" + options
	if oversized || optionsOversized {
		items.MandatoryInputTooLarge = true
		return history, items, nil
	}
	cfg := guardianCompactionConfig(req.Model, output)
	limit := sdkruntime.EvaluateModelRequestBudget(req.Model, guardianModelRequest(nil, items.Text, output), cfg).Usage.EffectiveInputBudget
	if limit <= 0 {
		limit = 24000
	}
	// Reserve room for exact input, schema and evidence gathered in this turn.
	userBudget := min(32000, max(1024, limit*4/10)) * 3
	turnBudget := min(24000, max(1024, limit*3/10)) * 3
	history = guardianTrimUsers(history, userBudget)
	history = guardianTrimTurns(history, turnBudget, req.ReviewID)
	for len(history) > 0 && sdkruntime.EvaluateModelRequestBudget(req.Model, guardianModelRequest(history, items.Text, output), cfg).Usage.TotalTokens > limit*8/10 {
		next := guardianDropOldestTurn(history, req.ReviewID)
		if len(next) == len(history) {
			break
		}
		history = next
	}
	if sdkruntime.EvaluateModelRequestBudget(req.Model, guardianModelRequest(history, items.Text, output), cfg).Usage.TotalTokens > limit {
		items.MandatoryInputTooLarge = true
	}
	return history, items, nil
}

func guardianEvidenceEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, text)
	return &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "guardian_evidence"}, Message: &message, Text: text, Meta: map[string]any{}}
}
func guardianFold(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	n := limit / 2
	return string(runes[:n]) + fmt.Sprintf("\n[folded %d characters; original remains in Session JSONL]\n", len(runes)-2*n) + string(runes[len(runes)-n:])
}
func guardianTrimUsers(events []*session.Event, budget int) []*session.Event {
	total := func() int {
		n := 0
		for _, e := range events {
			if guardianIsUser(e) {
				n += len(session.EventText(e))
			}
		}
		return n
	}
	for total() > budget {
		largest := -1
		for i, e := range events {
			if guardianIsUser(e) && len([]rune(session.EventText(e))) > 4096 && (largest < 0 || len(session.EventText(e)) > len(session.EventText(events[largest]))) {
				largest = i
			}
		}
		if largest < 0 {
			break
		}
		old := events[largest]
		one := guardianEvidenceEvent(guardianFold(session.EventText(old), 2048))
		one.Meta = session.CloneState(old.Meta)
		events[largest] = one
	}
	for total() > budget {
		dropped := false
		for i, e := range events {
			if guardianIsUser(e) {
				events = append(events[:i:i], events[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			break
		}
	}
	return events
}
func guardianIsUser(e *session.Event) bool {
	return e != nil && e.Meta != nil && e.Meta[guardianUserSource] != nil
}
func guardianTurn(e *session.Event) string {
	if e == nil {
		return ""
	}
	s, _ := e.Meta[guardianTurnKey].(string)
	return s
}
func guardianDropOldestTurn(events []*session.Event, current string) []*session.Event {
	oldest := ""
	for _, e := range events {
		if key := guardianTurn(e); key != "" && key != current {
			oldest = key
			break
		}
	}
	if oldest == "" {
		return events
	}
	out := make([]*session.Event, 0, len(events))
	for _, e := range events {
		if guardianIsUser(e) || guardianTurn(e) != oldest {
			out = append(out, e)
		}
	}
	return out
}
func guardianTrimTurns(events []*session.Event, budget int, current string) []*session.Event {
	size := func() int {
		n := 0
		for _, e := range events {
			if !guardianIsUser(e) {
				raw, _ := json.Marshal(e)
				n += len(raw)
			}
		}
		return n
	}
	if size() <= budget {
		return events
	}
	// A low-water target buys several append-only turns after each trim.
	// Retain at most three completed turns, or fewer when large tool evidence
	// needs more room. Never split the active turn or move user-source messages.
	for {
		groups := map[string]bool{}
		for _, e := range events {
			if key := guardianTurn(e); key != "" && key != current {
				groups[key] = true
			}
		}
		if len(groups) <= 3 && size() <= budget*6/10 {
			return events
		}
		next := guardianDropOldestTurn(events, current)
		if len(next) == len(events) {
			return events
		}
		events = next
	}
}

// Guardian never summarizes its private dialogue using another model call.
// The owner trims completed turns before Run; an overflowing active turn fails.
type guardianTurnCompactor struct{}

func (guardianTurnCompactor) Prepare(_ context.Context, req compact.Request) (compact.Result, error) {
	return compact.Result{PromptEvents: session.CloneEvents(req.Events)}, nil
}
func (guardianTurnCompactor) CompactOnOverflow(_ context.Context, _ compact.Request, err error) (compact.Result, error) {
	return compact.Result{}, fmt.Errorf("guardian active turn exceeded context budget: %w", err)
}

func guardianApprovalCallKey(req kernel.ApprovalReviewRequest) string {
	id := req.RuntimeRequest.Call.ID
	if req.Approval != nil && req.Approval.ToolCallID != "" {
		id = req.Approval.ToolCallID
	}
	if id == "" {
		return ""
	}
	source := req.SessionRef.SessionID
	if origin := req.RuntimeRequest.Origin; origin != nil && origin.SessionID != "" {
		source = origin.SessionID
	}
	return source + ":" + id
}

// guardianCurrentSourceCall finds the current request in the unconsumed source
// interval. Providers normally use unique IDs; choosing the latest occurrence
// also preserves preceding calls when an endpoint reuses an ID.
func guardianCurrentSourceCall(snapshot guardianConversationSnapshot, req kernel.ApprovalReviewRequest) uint64 {
	key := guardianApprovalCallKey(req)
	var seq uint64
	for _, e := range snapshot.ParentEvents {
		if e != nil && e.Seq > snapshot.ParentCursor.EventSeq && session.IsCanonicalHistoryEvent(e) && session.EventTypeOf(e) == session.EventTypeToolCall && e.Tool != nil && e.SessionID+":"+e.Tool.ID == key {
			seq = e.Seq
		}
	}
	return seq
}
