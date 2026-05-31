package gatewaydriver

import (
	"context"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (d *GatewayDriver) ListTasks(ctx context.Context, opts TaskListOptions) (TaskListView, error) {
	var ref session.SessionRef
	if active, ok := d.currentSession(); ok {
		ref = active.SessionRef
	}
	return d.stack.ListTasks(ctx, ref, opts)
}

func (d *GatewayDriver) TailTask(ctx context.Context, opts TaskOutputOptions) (TaskOutputView, error) {
	return d.stack.TailTask(ctx, opts)
}

func (d *GatewayDriver) StartTask(ctx context.Context, opts TaskStartOptions) (TaskOutputView, error) {
	return d.stack.StartTask(ctx, opts)
}

func (d *GatewayDriver) WaitTask(ctx context.Context, opts TaskWaitOptions) (TaskOutputView, error) {
	return d.stack.WaitTask(ctx, opts)
}

func (d *GatewayDriver) WriteTask(ctx context.Context, opts TaskWriteOptions) (TaskOutputView, error) {
	return d.stack.WriteTask(ctx, opts)
}

func (d *GatewayDriver) CancelTask(ctx context.Context, opts TaskOutputOptions) (TaskOutputView, error) {
	return d.stack.CancelTask(ctx, opts)
}

func (d *GatewayDriver) ReleaseTask(ctx context.Context, opts TaskOutputOptions) error {
	return d.stack.ReleaseTask(ctx, opts)
}
