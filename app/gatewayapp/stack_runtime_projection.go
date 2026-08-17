package gatewayapp

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// runtimeProjection returns the process root's Runtime composition without
// exposing it outside gatewayapp. Stack keeps the composition as a named field
// so only deliberate Host methods cross the boundary; detached Session
// Runtimes use runtimeComposition directly.
func (s *Stack) runtimeProjection() *runtimeComposition {
	if s == nil {
		return nil
	}
	return &s.composition
}

func (s *Stack) Models() ModelService {
	return s.runtimeProjection().Models()
}

func (s *Stack) Agents() AgentService {
	return s.runtimeProjection().Agents()
}

func (s *Stack) Skills() SkillService {
	return s.runtimeProjection().Skills()
}

func (s *Stack) Plugins() PluginService {
	return s.runtimeProjection().Plugins()
}

// ConfigurationRevision returns the current Host configuration revision used
// to fence Control mutations.
func (s *Stack) ConfigurationRevision(ctx context.Context) (uint64, error) {
	return s.runtimeProjection().ConfigurationRevision(ctx)
}

func (s *Stack) StartSubagent(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
) (task.Snapshot, error) {
	return s.runtimeProjection().StartSubagent(ctx, ref, agent, prompt, source)
}

func (s *Stack) StartSubagentWithOptions(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (task.Snapshot, error) {
	return s.runtimeProjection().StartSubagentWithOptions(ctx, ref, agent, prompt, source, opts)
}

func (s *Stack) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yieldDuration time.Duration,
) (task.Snapshot, error) {
	return s.runtimeProjection().WaitSubagentTask(ctx, ref, taskID, yieldDuration)
}

func (s *Stack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	return s.runtimeProjection().CompactSession(ctx, ref)
}
