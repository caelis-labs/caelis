package chat

import (
	"reflect"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func bindRequestSource(ctx agent.Context, req *model.Request) {
	// Only this projection can certify the observed Session history.
	req.Source = nil
	tracker := requestsource.FromContext(ctx)
	if tracker == nil {
		return
	}
	events := tracker.Events()
	// Use the same normalized projection as request history, including origins.
	// The fingerprint below still binds the complete observed source events.
	projected, err := messagesFromEventsChecked(ctx.Session(), compact.PromptEventsFromLatestCompact(events))
	if err != nil || !reflect.DeepEqual(req.Messages, projected) {
		return
	}
	through := session.LastEventSeq(events)
	if through == 0 {
		return
	}
	prefixHash, err := requestsource.Fingerprint(events)
	if err != nil {
		return
	}
	messagesHash, err := requestsource.Fingerprint(req.Messages)
	if err != nil {
		return
	}
	req.Source = &model.RequestSource{
		SourceThroughSeq: through, SourcePrefixHash: prefixHash, MessagesHash: messagesHash,
	}
}
