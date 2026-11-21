package runtime

import (
	"context"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/userdisplay"
)

// localInputContext keeps safe-point persistence and final admission closure
// with the producing Runtime, without exposing a Session writer to the Agent.
type localInputContext struct {
	agent.Context
	runner        *runner
	contextSource func() (agent.ContextSource, error)
	contextEdit   func(string, string, agent.ContextEdit, func(context.Context) (func(), error)) ([]*session.Event, error)
	commit        func(agent.Submission) ([]model.Message, error)
}

func (c *localInputContext) DrainFinalSubmissions() ([]agent.Submission, error) {
	taskScopeFromContext(c.Context).stopCodeMode()
	if !taskScopeFromContext(c.Context).wait(c.Context, c.runner.inputReadySignal()) {
		return c.runner.drainSubmissions(), nil
	}
	if scope := asyncScope(c.Context); scope != nil {
		if err := scope.drain(); err != nil {
			return nil, err
		}
	}
	return c.runner.drainFinalSubmissions(), nil
}

func buildSteeringInputEvents(active session.Session, turnID string, submission agent.Submission) ([]*session.Event, error) {
	if err := agent.ValidateSubmissionInputs(submission); err != nil {
		return nil, err
	}
	events, err := buildRunInputEvents(active, turnID, agent.RunRequest{
		InputKind: submission.Kind, Input: submission.Text,
		DisplayInput: userdisplay.ResolveDisplayInput(submission.DisplayInput, submission.Metadata),
		ContentParts: submission.ContentParts, InputActor: submission.Actor, Inputs: submission.Inputs,
	})
	for _, event := range events {
		// A steering admission is distinct from the initial turn-input identity.
		event.IdempotencyKey = ""
		preserveSteeringCorrelation(event, submission.Metadata)
	}
	return events, err
}

// preserveSteeringCorrelation carries Control's input receipt identity through
// the canonical append. Correlation is separate from the Session-owned message
// identity and the typed Actor/Scope; submission metadata cannot replace them.
func preserveSteeringCorrelation(event *session.Event, metadata map[string]any) {
	if event == nil {
		return
	}
	for _, key := range []string{"operation_id", "steering_operation_id"} {
		value, ok := metadata[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if event.Meta == nil {
			event.Meta = make(map[string]any, 2)
		}
		event.Meta[key] = value
	}
}

func (c *localInputContext) CommitSubmission(s agent.Submission) ([]model.Message, error) {
	return c.commit(s)
}

func (c *localInputContext) ContextEditSource() (agent.ContextSource, error) {
	return c.contextSource()
}
func (c *localInputContext) CommitContextEdit(logical, name string, edit agent.ContextEdit, validate func(context.Context) (func(), error)) ([]*session.Event, error) {
	return c.contextEdit(logical, name, edit, validate)
}
