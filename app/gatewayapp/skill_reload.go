package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/promptassembly"
)

const skillInstructionsStateKey = "runtime.skill_instructions"

// Skill calls resolve live metadata independently. The rendered prefix is
// durable so a new Turn or a reactivated Runtime cannot advance it early.
func sessionSkillInstructions(sessions session.Service, ref session.SessionRef, resolve func(context.Context) (skill.Catalog, error)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, summaryID string) (string, error) {
		state, err := sessions.SnapshotState(ctx, ref)
		if err != nil {
			return "", err
		}
		if text, pinned := pinnedSkillInstructions(state, summaryID); pinned {
			return text, nil
		}
		catalog, err := resolve(ctx)
		if err != nil {
			return "", err
		}
		text := promptassembly.SkillMetadata(catalog)
		_, err = sessions.UpdateState(ctx, session.UpdateStateRequest{
			SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx),
			Update: func(state map[string]any) (map[string]any, error) {
				if prior, pinned := pinnedSkillInstructions(state, summaryID); pinned {
					text = prior
					return state, nil
				}
				if state == nil {
					state = map[string]any{}
				}
				state[skillInstructionsStateKey] = map[string]any{"text": text, "summary_id": summaryID}
				return state, nil
			},
		})
		if session.IsCommitted(err) {
			err = nil
		}
		return text, err
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
