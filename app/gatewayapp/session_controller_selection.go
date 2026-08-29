package gatewayapp

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// initialSessionController freezes an ACP-backed Host default into a dormant
// durable binding. The first Turn activates or resumes the external endpoint;
// provider-backed defaults continue to let Runtime install the kernel owner.
func (s *runtimeComposition) initialSessionController(ctx context.Context) (session.ControllerBinding, error) {
	if s == nil || s.process == nil {
		return session.ControllerBinding{}, nil
	}
	runtimeCfg := s.runtimeProcessSnapshot().runtime
	profileID := modelprofile.NormalizeID(runtimeCfg.ModelProfileID)
	profile, ok := s.cachedModelProfile(profileID)
	if !ok {
		snapshot, err := s.placementSnapshot(ctx)
		if err != nil {
			return session.ControllerBinding{}, err
		}
		profile, ok = modelprofile.Lookup(snapshot.placement.Profiles, profileID)
	}
	if !ok || profile.Kind() != modelprofile.BackendACP {
		return session.ControllerBinding{}, nil
	}
	frozen, err := s.resolveModelProfilePlacement(ctx, profile.ID, runtimeCfg.ModelProfileEffort)
	if err != nil {
		return session.ControllerBinding{}, fmt.Errorf("gatewayapp: resolve initial ACP controller: %w", err)
	}
	return dormantACPControllerBinding(frozen, "default_profile", time.Now()), nil
}

func dormantACPControllerBinding(frozen placement.Placement, source string, now time.Time) session.ControllerBinding {
	frozen = placement.Normalize(frozen)
	return session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: strings.TrimSpace(frozen.Agent),
		AgentName:    strings.TrimSpace(frozen.Agent),
		Label:        strings.TrimSpace(frozen.Agent),
		Placement:    frozen,
		EpochID:      "control-acp-" + strings.ToLower(rand.Text()),
		AttachedAt:   now,
		Source:       strings.TrimSpace(source),
	}
}

func initialKernelControllerBinding(source string) session.ControllerBinding {
	return session.ControllerBinding{
		Kind:         session.ControllerKindKernel,
		ControllerID: "sdk-kernel",
		AgentName:    "local",
		Label:        "SDK Kernel",
		EpochID:      "control-kernel-" + strings.ToLower(rand.Text()),
		AttachedAt:   time.Now(),
		Source:       strings.TrimSpace(source),
	}
}
