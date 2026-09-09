package subagent

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
)

func (r *Runner) withCollaborationPromptSlice(run *childRun, prompt []json.RawMessage) []json.RawMessage {
	if r == nil || run == nil {
		return prompt
	}
	run.mu.RLock()
	slice := collaboration.PromptSlice{
		Handle:         strings.TrimPrefix(strings.TrimSpace(run.spawn.Handle), "@"),
		Role:           strings.TrimSpace(string(run.spawn.Role)),
		MailboxPolling: !run.supportsSteering,
	}
	run.mu.RUnlock()
	if slice.Role == "" {
		slice.Role = string(session.ParticipantRoleDelegated)
	}
	return acputil.PrefixTextBlock(prompt, collaboration.RenderPromptSlice(slice))
}
