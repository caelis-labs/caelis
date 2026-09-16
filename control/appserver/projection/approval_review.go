package projection

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// The existing pause decision is the authority, including decisions written by
// older Hosts. Replay projects only its display fields without rewriting it or
// exposing the execution journal and its private metadata to the Surface.
func projectApprovalReview(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
	token := session.ResolvedApprovalReview(event)
	if token == nil {
		return nil
	}
	status := "denied"
	if token.Approved {
		status = "approved"
	}
	var input map[string]any
	_ = json.Unmarshal(token.Input, &input)
	base.Kind = eventstream.KindApprovalReview
	base.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryMirror}
	base.TurnID = strings.TrimSpace(token.TurnID)
	base.Meta = nil
	base.ApprovalReview = &eventstream.ApprovalReview{
		ToolCallID: strings.TrimSpace(token.ToolCallID), ToolName: strings.TrimSpace(token.ToolName),
		RawInput: input, Status: status, Text: strings.TrimSpace(token.ReviewText),
	}
	return []eventstream.Envelope{base}
}
