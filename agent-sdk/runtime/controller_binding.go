package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) controllerBindings(ctx context.Context, ref session.SessionRef) (session.ControllerBinding, session.ControllerBinding, error) {
	current, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.ControllerBinding{}, session.ControllerBinding{}, err
	}
	durable := session.CloneControllerBinding(current.Controller)
	live := session.CloneControllerBinding(durable)
	if provider, ok := r.controllers.(controller.BindingProvider); ok {
		binding, found, err := provider.ActiveControllerBinding(ctx, ref)
		if err != nil {
			return durable, live, err
		}
		if !found {
			return durable, live, fmt.Errorf("agent-sdk/runtime: active controller binding is unavailable")
		}
		if strings.TrimSpace(binding.EpochID) != strings.TrimSpace(durable.EpochID) {
			return durable, live, fmt.Errorf("agent-sdk/runtime: live controller epoch %q does not match durable epoch %q", strings.TrimSpace(binding.EpochID), strings.TrimSpace(durable.EpochID))
		}
		live = session.CloneControllerBinding(binding)
	}
	return durable, live, nil
}

// commitControllerBinding publishes a replacement's identity before it can
// invoke Host tools, without advancing the backend's delivered checkpoint.
func (r *Runtime) commitControllerBinding(ctx context.Context, ref session.SessionRef) error {
	durable, live, err := r.controllerBindings(ctx, ref)
	if err != nil {
		return err
	}
	if live.RemoteSessionID == durable.RemoteSessionID {
		return nil
	}
	_, err = r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Binding: live,
	})
	return err
}
