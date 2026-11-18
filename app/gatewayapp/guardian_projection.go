package gatewayapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const guardianAssistantContext = "guardian_assistant_context"

// guardianProjection consumes canonical events once. This bounded cache is
// disposable; its source sequence is independent of completed review history.
type guardianProjection struct {
	mu     sync.Mutex
	after  uint64
	cursor guardianParentCanonicalCursor
	events []*session.Event
}

func (p *guardianProjection) read(ctx context.Context, service session.Service, ref session.SessionRef) ([]*session.Event, guardianParentCanonicalCursor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reader, ok := service.(session.PagedReader)
	checkpoints, checkpointOK := service.(session.EventCheckpointReader)
	if !ok || !checkpointOK {
		return nil, p.cursor, fmt.Errorf("guardian requires canonical event pagination and checkpoints")
	}
	cut, err := checkpoints.EventCheckpoint(ctx, ref)
	if err != nil {
		return nil, p.cursor, err
	}
	if cut.ThroughSeq < p.after {
		p.after, p.cursor, p.events = 0, guardianParentCanonicalCursor{}, nil
	}
	for p.after < cut.ThroughSeq {
		page, err := reader.EventsPage(ctx, session.EventPageRequest{SessionRef: ref, AfterSeq: p.after, ThroughSeq: cut.ThroughSeq, Limit: 64, Visibility: session.EventPageCanonical})
		if err != nil {
			return nil, p.cursor, err
		}
		for _, event := range page.Events {
			if event == nil || event.Seq <= p.after {
				continue
			}
			p.cursor = guardianParentCanonicalCursor{EventID: event.ID, EventSeq: event.Seq}
			if projected := guardianProjectEvent(event); projected != nil {
				p.events = append(p.events, projected)
				// Retention follows source order, not the caller's page size or
				// approval cadence, so a reopened source rebuilds the same cut.
				p.events = guardianBoundSourceDialogue(p.events)
			}
		}
		if !page.HasMore {
			p.after = cut.ThroughSeq
			break
		}
		if page.NextSeq <= p.after {
			return nil, p.cursor, fmt.Errorf("guardian event page did not advance")
		}
		p.after = page.NextSeq
	}
	return session.CloneEvents(p.events), p.cursor, nil
}

// guardianProjectEvent projects public prose, including prose saved with a
// compound assistant tool call, without copying tool or reasoning parts.
func guardianProjectEvent(e *session.Event) *session.Event {
	if e == nil || !session.IsCanonicalHistoryEvent(e) {
		return nil
	}
	var one *session.Event
	switch session.EventTypeOf(e) {
	case session.EventTypeUser:
		if e.Actor.Kind != session.ActorKindUser && e.Actor.Kind != "" {
			return nil
		}
		text := guardianVisibleText(e)
		if evidence := session.OriginalInputMessage(e); evidence != nil {
			text = evidence.TextContent()
		}
		one = guardianEvidenceEvent(fmt.Sprintf("User message [seq=%d]:\n%s", e.Seq, text))
		one.Meta[guardianUserSource] = fmt.Sprintf("%s:%d", e.SessionID, e.Seq)
	case session.EventTypeAssistant, session.EventTypeToolCall:
		text := guardianAssistantText(e)
		if text == "" {
			return nil
		}
		label := fmt.Sprintf("Assistant message [seq=%d, context only]:\n", e.Seq)
		one = guardianEvidenceEvent(label + text)
		if guardianMessageTokens(one) > guardianAssistantEntryTokens {
			one = guardianEvidenceEvent(label + "[message omitted; its scope cannot be inferred]")
		}
		message := model.NewTextMessage(model.RoleAssistant, one.Text)
		one.Type, one.Message = session.EventTypeAssistant, &message
		one.Meta[guardianAssistantContext] = true
	default:
		return nil
	}
	one.ID, one.Seq, one.SessionID = e.ID, e.Seq, e.SessionID
	return one
}

func guardianAssistantText(event *session.Event) string {
	if (event.Actor.Kind != "" && event.Actor.Kind != session.ActorKindController) ||
		(event.Scope != nil && event.Scope.Participant.ID != "") ||
		session.IsAgentCommunicationProtocol(event) ||
		session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeAgentThought) {
		return ""
	}
	if event.Message != nil {
		if event.Message.Role == model.RoleAssistant {
			return strings.TrimSpace(event.Message.TextContent())
		}
		return ""
	}
	if session.EventTypeOf(event) == session.EventTypeAssistant {
		return strings.TrimSpace(guardianVisibleText(event))
	}
	return ""
}

func guardianIsAssistantContext(event *session.Event) bool {
	return event != nil && event.Meta[guardianAssistantContext] == true
}

func guardianBoundSourceDialogue(events []*session.Event) []*session.Event {
	users, assistants, _ := guardianSplitHistory(events)
	retainedUsers := guardianTrimUsers(users, 16*1024*1024)
	if tokens, _ := guardianHistoryUsage(assistants); len(assistants) > guardianAssistantHighEntries || tokens > guardianAssistantHighTokens {
		assistants = guardianRecentAssistants(assistants, guardianAssistantKeepEntries, guardianAssistantKeepTokens)
	} else if len(events) == len(retainedUsers)+len(assistants) && slices.Equal(users, retainedUsers) {
		return events
	}
	return guardianRetainedHistory(events, retainedUsers, assistants)
}
