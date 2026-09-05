package appserver

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// feedNarrativeKey records content actually accepted into this broker's live
// spool. It retains identity only, until canonical catch-up or Turn completion;
// it is neither payload history nor authority for fresh canonical replay.
type feedNarrativeKey struct {
	scope         eventstream.Scope
	scopeID       string
	participantID string
	turnID        string
	messageID     string
	content       agent.PublishedContent
}

func narrativeFeedKey(envelope eventstream.Envelope) (feedNarrativeKey, bool) {
	chunk, ok := envelope.Update.(eventstream.ContentChunk)
	if !ok || strings.TrimSpace(chunk.MessageID) == "" {
		return feedNarrativeKey{}, false
	}
	var content agent.PublishedContent
	switch chunk.SessionUpdate {
	case eventstream.UpdateAgentMessage:
		content = agent.PublishedAssistantMessage
	case eventstream.UpdateAgentThought:
		content = agent.PublishedAssistantThought
	default:
		return feedNarrativeKey{}, false
	}
	key := narrativeFeedOwner(envelope)
	key.messageID, key.content = strings.TrimSpace(chunk.MessageID), content
	return key, true
}

func narrativeFeedOwner(envelope eventstream.Envelope) feedNarrativeKey {
	key := feedNarrativeKey{
		scope: envelope.Scope, participantID: strings.TrimSpace(envelope.ParticipantID),
		turnID: strings.TrimSpace(envelope.TurnID),
	}
	if key.scope == "" {
		key.scope = eventstream.ScopeMain
	}
	// Main ScopeID may name either the Session or the Turn. Its typed TurnID
	// owns correlation; participant/Task scopes also retain their own address.
	if key.scope != eventstream.ScopeMain {
		key.scopeID = strings.TrimSpace(envelope.ScopeID)
	}
	return key
}

// observeLiveNarrativeLocked runs only after a successful spool append.
func (b *FeedBroker) observeLiveNarrativeLocked(envelope eventstream.Envelope) {
	if envelope.Kind == eventstream.KindLifecycle && envelope.Lifecycle != nil &&
		eventstream.IsTerminalLifecycleState(envelope.Lifecycle.State) && envelope.ApprovalRequestID == "" {
		owner := narrativeFeedOwner(envelope)
		for key := range b.liveNarratives {
			if key.scope == owner.scope && key.scopeID == owner.scopeID &&
				key.participantID == owner.participantID && key.turnID == owner.turnID {
				delete(b.liveNarratives, key)
			}
		}
		return
	}
	if isDurableFeedEnvelope(envelope) {
		return
	}
	if key, ok := narrativeFeedKey(envelope); ok {
		if b.liveNarratives == nil {
			b.liveNarratives = make(map[feedNarrativeKey]struct{})
		}
		b.liveNarratives[key] = struct{}{}
	}
}

// publishCanonicalCatchup fills unpublished durable positions without appending
// a second copy of content already owned by the exact live trace. primeMu is
// held by the caller; acceptMu protects the delivery bookkeeping.
func (b *FeedBroker) publishCanonicalCatchup(ctx context.Context, event *session.Event) error {
	base := projection.EnvelopeBaseFromSessionEvent(b.ref, event, projection.SessionEventTransport{})
	complete := projection.ProjectSessionEventEnvelope(base, event)
	var published agent.PublishedContent
	b.acceptMu.Lock()
	if base.Final {
		for _, envelope := range complete {
			if key, ok := narrativeFeedKey(envelope); ok {
				if _, accepted := b.liveNarratives[key]; accepted {
					published |= key.content
				}
			}
		}
	}
	b.acceptMu.Unlock()
	for _, envelope := range projection.WithoutPublishedContent(complete, published) {
		if !isDurableFeedEnvelope(envelope) {
			continue
		}
		if err := b.publishAccepted(ctx, envelope); err != nil {
			return err
		}
	}
	b.acceptMu.Lock()
	defer b.acceptMu.Unlock()
	if b.closed || b.sealed {
		return errors.New("controlclient: feed broker is closed")
	}
	for _, envelope := range complete {
		// Even an entirely suppressed final commits its durable boundary. Fresh
		// replay must still include that canonical value if the trace expires.
		if isDurableFeedEnvelope(envelope) && compareDurablePosition(*envelope.Position.Durable, b.latestDurable) > 0 {
			b.acceptDurableLocked(envelope)
		}
		if key, ok := narrativeFeedKey(envelope); ok && base.Final {
			delete(b.liveNarratives, key)
		}
	}
	return nil
}
