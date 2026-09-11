package runtime

import (
	"context"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SubmitUserChildInput admits an embedding-authorized user prompt to an exact
// existing child binding. It shares child admission and output ownership with
// Agent messaging, without claiming the parent Session's foreground Turn.
// The embedding must authenticate and authorize the user before calling this.
func (r *Runtime) SubmitUserChildInput(ctx context.Context, ref session.SessionRef, expected session.ParticipantBinding, userID, input string, parts []model.ContentPart) (agent.ChildInputResult, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return agent.ChildInputResult{}, errorcode.New(errorcode.FailedPrecondition, "Child input is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	userID, input = strings.TrimSpace(userID), strings.TrimSpace(input)
	if userID == "" || (input == "" && len(parts) == 0) {
		return agent.ChildInputResult{}, errorcode.New(errorcode.InvalidArgument, "User identity and input are required")
	}
	r.participantMu.Lock()
	defer r.participantMu.Unlock()
	ref = session.NormalizeSessionRef(ref)
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	_, binding, err := resolveExactParticipantInputSource(active, expected)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	if binding.Kind != session.ParticipantKindSubagent {
		return agent.ChildInputResult{}, errorcode.New(errorcode.InvalidArgument, "Target is not a delegated child")
	}
	user := session.ActorRef{Kind: session.ActorKindUser, ID: userID, Name: "user"}
	return r.submitChildInputLocked(ctx, ref, active, agent.ChildInputCommand{
		Target: binding.ID, Source: user, UserInput: true, Input: input, ContentParts: parts,
	}, user, nil, nil)
}
