package agentsdk

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// NormalizeChildEndpointRef returns a detached endpoint address.
func NormalizeChildEndpointRef(in ChildEndpointRef) ChildEndpointRef {
	return ChildEndpointRef{
		ParticipantID: strings.TrimSpace(in.ParticipantID),
		SessionID:     strings.TrimSpace(in.SessionID),
		EndpointKey:   strings.TrimSpace(in.EndpointKey),
		Role:          in.Role,
		Placement:     placement.Normalize(in.Placement),
	}
}

// CloneChildInputRequest returns a detached normalized request.
func CloneChildInputRequest(in ChildInputRequest) ChildInputRequest {
	out := in
	out.Target = NormalizeChildEndpointRef(in.Target)
	out.Source = session.CloneActorRef(in.Source)
	out.ActivityID = strings.TrimSpace(in.ActivityID)
	out.Input = strings.TrimSpace(in.Input)
	out.DisplayInput = strings.TrimSpace(in.DisplayInput)
	out.ContentParts = append([]model.ContentPart(nil), in.ContentParts...)
	return out
}

// CloneChildInputCommand returns a detached normalized topology command.
func CloneChildInputCommand(in ChildInputCommand) ChildInputCommand {
	out := in
	out.Target = strings.TrimSpace(in.Target)
	out.Source = session.CloneActorRef(in.Source)
	out.Input = strings.TrimSpace(in.Input)
	out.DisplayInput = strings.TrimSpace(in.DisplayInput)
	out.ContentParts = append([]model.ContentPart(nil), in.ContentParts...)
	return out
}
