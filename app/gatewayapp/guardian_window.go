package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianUserSource = "guardian_user_source"
const guardianTurnKey = "guardian_turn"

// guardianWindow prepares an append-only input sequence. Source users have a
// separate budget and survive removal of the surrounding completed turns.
func guardianWindow(snapshot guardianConversationSnapshot, req kernel.ApprovalReviewRequest, output *model.OutputSpec) ([]*session.Event, guardianPromptItems, error) {
	history := session.CloneEvents(snapshot.Events)
	items := guardianPromptItems{ParentCursor: snapshot.ParentCursor}
	for _, e := range snapshot.ParentEvents {
		if e == nil || e.Seq <= snapshot.ParentCursor.EventSeq || !session.IsCanonicalHistoryEvent(e) {
			continue
		}
		if e.Seq > items.ParentCursor.EventSeq {
			items.ParentCursor = guardianParentCanonicalCursor{EventID: e.ID, EventSeq: e.Seq}
		}
		if one := guardianProjectEvent(e); one != nil {
			history = append(history, one)
		}
	}
	if snapshot.SourceCursor.EventSeq > items.ParentCursor.EventSeq {
		items.ParentCursor = snapshot.SourceCursor
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
	userBudget := min(6000, max(1024, limit*3/10)) * 3
	// Structured messages contain JSON framing as well as prose. The exact
	// token check below remains authoritative; the byte cap bounds retention.
	turnBudget := min(8000, max(1024, limit*3/10)) * 6
	beforeRetention := append([]*session.Event(nil), history...)
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
	items.ContextTrimmed = !reflect.DeepEqual(beforeRetention, history)
	return history, items, nil
}

func guardianEvidenceEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, text)
	return &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "guardian_evidence"}, Message: &message, Text: text, Meta: map[string]any{}}
}
func guardianFold(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	n := limit / 2
	first, last := 0, len(text)
	for range n {
		if first >= last {
			return text
		}
		_, size := utf8.DecodeRuneInString(text[first:last])
		first += size
		if first >= last {
			return text
		}
		_, size = utf8.DecodeLastRuneInString(text[first:last])
		last -= size
	}
	if first >= last {
		return text
	}
	return text[:first] + fmt.Sprintf("\n[folded %d bytes; source event can be read with ReadEvents]\n", last-first) + text[last:]
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
		one := session.CloneEvent(old)
		text := guardianFold(session.EventText(old), 2048)
		message := model.NewTextMessage(model.RoleUser, text)
		one.Message, one.Text = &message, text
		one.Meta = session.CloneState(old.Meta)
		events[largest] = one
	}
	for total() > budget {
		dropped := false
		first, last := -1, -1
		for i, e := range events {
			if guardianIsUser(e) {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		for i, e := range events {
			if guardianIsUser(e) && i != first && i != last {
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
				// Budget the model input, not duplicated Event.Text, metadata,
				// IDs and journal provenance that never enter the model prefix.
				message, ok := session.ModelMessageOf(e)
				if !ok {
					message = model.NewTextMessage(model.RoleUser, session.EventText(e))
				}
				raw, _ := json.Marshal(message)
				n += len(raw)
			}
		}
		return n
	}
	if size() <= budget {
		return events
	}
	// A low-water target buys several append-only turns after each trim.
	// Never split the active turn or move user-source messages. Turn count is
	// not a budget: many short approvals can share the same cache epoch.
	for {
		groups := map[string]bool{}
		for _, e := range events {
			if key := guardianTurn(e); key != "" && key != current {
				groups[key] = true
			}
		}
		if size() <= budget*6/10 || (len(groups) <= 1 && size() <= budget) {
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
