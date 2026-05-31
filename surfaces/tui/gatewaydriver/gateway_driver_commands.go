package gatewaydriver

import (
	"context"
	"strings"

	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
)

func (d *GatewayDriver) CommandCatalog(ctx context.Context) (appviewmodel.CommandCatalogView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	view, ok, err := d.stack.CommandCatalog(ctx)
	if !ok || err != nil {
		return view, err
	}
	return view, nil
}

func (d *GatewayDriver) ExecuteCommand(ctx context.Context, opts CommandExecutionOptions) (CommandExecutionView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, finish := d.beginInterruptibleCommand(ctx)
	defer finish()
	var ref coresession.Ref
	if active, ok := d.currentSession(); ok {
		ref = coreRefFromPort(active.SessionRef)
	}
	parts, err := contentPartsFromSubmission(opts.Input, opts.Attachments, d.WorkspaceDir())
	if err != nil {
		return CommandExecutionView{}, err
	}
	view, err := d.stack.ExecuteCommand(ctx, ref, strings.TrimSpace(opts.Input), parts)
	if err != nil {
		return CommandExecutionView{}, err
	}
	if view.SessionRef != nil {
		d.setCurrentCommandSession(ctx, portRefFromCore(*view.SessionRef))
	}
	d.syncCurrentCommandSessionEvents(view.Events)
	return view, nil
}

func (d *GatewayDriver) setCurrentCommandSession(ctx context.Context, ref portsession.SessionRef) {
	if strings.TrimSpace(ref.SessionID) == "" {
		return
	}
	active := portsession.Session{
		SessionRef: ref,
		CWD:        strings.TrimSpace(d.stack.Workspace.CWD),
	}
	d.mu.Lock()
	d.session = active
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, active)
}

func (d *GatewayDriver) syncCurrentCommandSessionEvents(events []coresession.Event) {
	var controller coresession.ControllerBinding
	for _, event := range events {
		if event.Type != coresession.EventHandoff || event.Scope == nil {
			continue
		}
		next := event.Scope.Controller
		if next.Kind == "" && strings.TrimSpace(next.ID) == "" && strings.TrimSpace(next.AgentName) == "" {
			continue
		}
		controller = next
	}
	if controller.Kind == "" && strings.TrimSpace(controller.ID) == "" && strings.TrimSpace(controller.AgentName) == "" {
		return
	}
	d.mu.Lock()
	if d.hasSession {
		active := d.session
		active.Controller = portControllerFromCore(controller)
		d.session = active
	}
	d.mu.Unlock()
}
