package gatewayapp

import (
	"context"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianUserSource = "guardian_user_source"
const guardianTurnKey = "guardian_turn"

// guardianWindow appends projected dialogue without changing the existing prefix.
func guardianWindow(snapshot guardianConversationSnapshot, req kernel.ApprovalReviewRequest, output *model.OutputSpec, queryTools bool) ([]*session.Event, guardianPromptItems, error) {
	history := append([]*session.Event(nil), snapshot.Events...)
	items := guardianPromptItems{ParentCursor: snapshot.ParentCursor}
	for _, e := range snapshot.ParentEvents {
		if e == nil || e.Seq <= snapshot.ParentCursor.EventSeq {
			continue
		}
		if e.Seq > items.ParentCursor.EventSeq {
			items.ParentCursor = guardianParentCanonicalCursor{EventID: e.ID, EventSeq: e.Seq}
		}
		history = append(history, e)
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
	beforeRetention := history
	history, fits := guardianFitHistory(history, req.Model, items.Text, output, queryTools)
	items.MandatoryInputTooLarge = !fits
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
	// Limits count bytes, with boundaries advanced only over complete UTF-8.
	first, last := limit/2, len(text)-limit/2
	for first > 0 && !utf8.RuneStart(text[first]) {
		first--
	}
	for last < len(text) && !utf8.RuneStart(text[last]) {
		last++
	}
	return text[:first] + fmt.Sprintf("\n[folded %d bytes; omitted content is unavailable]\n", last-first) + text[last:]
}

func guardianTrimUsers(events []*session.Event, budget int) []*session.Event {
	return guardianTrimUserHistory(events, budget, func(event *session.Event) int {
		return len(session.EventText(event))
	})
}

func guardianTrimUserHistory(events []*session.Event, budget int, measure func(*session.Event) int) []*session.Event {
	events = append([]*session.Event(nil), events...)
	total := 0
	for _, event := range events {
		total += measure(event)
	}
	for total > budget {
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
		total += measure(one) - measure(old)
		events[largest] = one
	}
	if total <= budget || len(events) <= 2 {
		return events
	}
	firstRetained := 1
	omission := session.CloneEvent(events[firstRetained])
	omission.Text = "[omitted older user messages; their constraints and authorizations are unavailable]"
	message := model.NewTextMessage(model.RoleUser, omission.Text)
	omission.Message = &message
	for total+measure(omission) > budget && firstRetained < len(events)-1 {
		total -= measure(events[firstRetained])
		firstRetained++
	}
	return append([]*session.Event{events[0], omission}, events[firstRetained:]...)
}
func guardianIsUser(e *session.Event) bool {
	return guardianUserKey(e) != ""
}

func guardianUserKey(event *session.Event) string {
	if event == nil {
		return ""
	}
	key, _ := event.Meta[guardianUserSource].(string)
	return key
}
func guardianTurn(e *session.Event) string {
	if e == nil {
		return ""
	}
	s, _ := e.Meta[guardianTurnKey].(string)
	return s
}

// The Harness trims completed turns before Run. An overflowing active review
// is unavailable; Guardian does not summarize or reconstruct its own history.
type guardianTurnCompactor struct{}

func (guardianTurnCompactor) Prepare(_ context.Context, req compact.Request) (compact.Result, error) {
	return compact.Result{PromptEvents: session.CloneEvents(req.Events)}, nil
}

func (guardianTurnCompactor) CompactOnOverflow(_ context.Context, _ compact.Request, err error) (compact.Result, error) {
	return compact.Result{}, fmt.Errorf("guardian active turn exceeded context budget: %w", err)
}
