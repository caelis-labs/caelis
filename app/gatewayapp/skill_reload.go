package gatewayapp

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/promptassembly"
)

const skillInstructionsStateKey = "runtime.skill_instructions"

// Skill calls resolve live metadata independently. The rendered prefix is
// durable so a new Turn or a reactivated Runtime cannot advance it early.
func sessionSkillInstructions(sessions session.StateStore, ref session.SessionRef, resolve func(context.Context) (skill.Catalog, error)) agent.SessionInstructionsResolver {
	return func(ctx context.Context, summaryID string) (string, func(context.Context) error, error) {
		state, err := sessions.SnapshotState(ctx, ref)
		if err != nil {
			return "", nil, err
		}
		if text, pinned := pinnedSkillInstructions(state, summaryID); pinned {
			return text, nil, nil
		}
		catalog, err := resolve(ctx)
		if err != nil {
			return "", nil, err
		}
		text := promptassembly.SkillMetadata(catalog)
		return text, func(ctx context.Context) error {
			_, err := sessions.UpdateState(ctx, session.UpdateStateRequest{
				SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx),
				Update: func(state map[string]any) (map[string]any, error) {
					if state == nil {
						state = map[string]any{}
					}
					state[skillInstructionsStateKey] = map[string]any{"text": text, "summary_id": summaryID}
					return state, nil
				},
			})
			if session.IsCommitted(err) {
				return nil
			}
			return err
		}, nil
	}
}

func pinnedSkillInstructions(state map[string]any, summaryID string) (string, bool) {
	snapshot, ok := state[skillInstructionsStateKey].(map[string]any)
	if !ok {
		return "", false
	}
	text, _ := snapshot["text"].(string)
	previousID, _ := snapshot["summary_id"].(string)
	return text, summaryID == "" || summaryID == previousID
}
