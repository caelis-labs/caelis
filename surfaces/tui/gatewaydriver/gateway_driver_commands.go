package gatewaydriver

import (
	"context"
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
	var ref session.SessionRef
	if active, ok := d.currentSession(); ok {
		ref = active.SessionRef
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
	return view, nil
}

func (d *GatewayDriver) setCurrentCommandSession(ctx context.Context, ref session.SessionRef) {
	if strings.TrimSpace(ref.SessionID) == "" {
		return
	}
	active := session.Session{
		SessionRef: ref,
		CWD:        strings.TrimSpace(d.stack.Workspace.CWD),
	}
	d.mu.Lock()
	d.session = active
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, active)
}
