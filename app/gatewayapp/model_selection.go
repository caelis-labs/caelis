package gatewayapp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// modelSelectionChoices keeps picker defaults on the same configured profile
// and durable Session identities used to resolve model commands.
func (s *runtimeComposition) modelSelectionChoices(ctx context.Context, ref session.SessionRef, profiles modelprofile.Configuration, choices []ModelChoice, state map[string]any) ([]ModelChoice, error) {
	runtime := s.runtimeProcessSnapshot().runtime
	currentID := firstNonEmpty(runtime.ModelProfileID, profiles.DefaultProfileID)
	effort := firstNonEmpty(runtime.ModelProfileEffort, runtime.Model.ReasoningEffort, profiles.DefaultEffort)
	fast := runtime.ModelFastMode
	if ref.SessionID != "" {
		active, err := s.sessions.Session(ctx, ref)
		if err != nil {
			return nil, err
		}
		if active.Controller.Kind == session.ControllerKindACP {
			currentID = active.Controller.Placement.ProfileID
			effort = active.Controller.Placement.ReasoningEffort
			fast = false
		} else {
			if selected := kernel.CurrentModelAlias(state); selected != "" {
				currentID = selected
				effort = ""
				fast = false
			}
			if selected := kernel.CurrentReasoningEffort(state); selected != "" {
				effort = selected
			}
			if selected, ok := kernel.CurrentModelFastMode(state); ok {
				fast = selected
			}
		}
	}
	for i := range choices {
		choice := &choices[i]
		if profile, ok := modelprofile.Lookup(profiles, choice.ProfileID); ok {
			choice.ReasoningLevels = modelProfileEfforts(profile)
			choice.ReasoningEffort = profile.Effort.DefaultEffort
		}
		choice.Current = currentID != "" && (strings.EqualFold(currentID, choice.ID) || strings.EqualFold(currentID, choice.ProfileID))
		choice.FastMode = choice.Current && fast && choice.FastSupported
		if choice.Current {
			if effort != "" {
				choice.ReasoningEffort = effort
			}
		}
	}
	return choices, nil
}
