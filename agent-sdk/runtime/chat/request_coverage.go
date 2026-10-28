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
	tracker := requestsource.FromContext(ctx)
	if tracker == nil {
		return
	}
	events := tracker.Events()
	// Compare the unmodified projection, including origins and tool messages.
	// History repair must not manufacture evidence for speculative coverage.
	projected := messagesFromEvents(ctx.Session(), compact.PromptEventsFromLatestCompact(events))
	if !reflect.DeepEqual(req.Messages, projected) {
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
