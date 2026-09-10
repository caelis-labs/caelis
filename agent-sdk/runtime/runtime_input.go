package runtime

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// localInputContext keeps safe-point persistence and final admission closure
// with the producing Runtime, without exposing a Session writer to the Agent.
type localInputContext struct {
	agent.Context
	runner *runner
	commit func([]agent.AgentCommunicationInput) ([]model.Message, error)
}

func (c *localInputContext) CommitAgentInputBatch(inputs []agent.AgentCommunicationInput) ([]model.Message, error) {
	return c.commit(inputs)
}

func (c *localInputContext) DrainFinalSubmissions() []agent.Submission {
	return c.runner.drainFinalSubmissions()
}

func buildSteeringInputEvents(active session.Session, turnID string, submission agent.Submission) ([]*session.Event, error) {
	if err := agent.ValidateSubmissionInputs(submission); err != nil {
		return nil, err
	}
	events, err := buildRunInputEvents(active, turnID, agent.RunRequest{
		InputKind: submission.Kind, Input: submission.Text, DisplayInput: submission.DisplayInput,
		ContentParts: submission.ContentParts, InputActor: submission.Actor, Inputs: submission.Inputs,
	})
	for _, event := range events {
		// A steering admission is distinct from the initial turn-input identity.
		event.IdempotencyKey = ""
	}
	return events, err
}
