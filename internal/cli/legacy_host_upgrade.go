package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"golang.org/x/mod/semver"
)

// Pre-v0.72 released Hosts cannot close admission atomically while idle. This
// transition reads actual Runtime work, then uses their public forced shutdown.
// A task admitted between these operations can be interrupted. Remove this
// entire compatibility boundary when the minimum managed Host is v0.72.0.
func shutdownLegacyIdleHost(ctx context.Context, remote *httpclient.Client, record controlserver.DiscoveryRecord, info appserver.ServerInfo) error {
	v := "v" + strings.TrimPrefix(info.DistributionVersion, "v")
	if info.BuildKind != "release" || !semver.IsValid(v) || semver.Compare(v, "v0.72.0") >= 0 {
		return errors.New("cli: Host does not provide idle replacement capability")
	}
	status, err := remote.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		return err
	}
	r := status.Runtime
	if r.Running || r.ActiveJobs > 0 || len(r.ActiveSessions) > 0 {
		return servicelifecycle.ErrHostBusy
	}
	// Legacy /status is scoped to the default Runtime, not all Sessions. The
	// directory samples live handles without loading history; Task directories
	// also catch detached commands that outlive their producing Turn.
	tasks, err := httpclient.NewTaskClient(remote)
	if err != nil {
		return err
	}
	cursor := ""
	for {
		page, err := remote.ListSessions(ctx, appserver.ListSessionsRequest{Cursor: cursor, Limit: 100})
		if err != nil {
			return err
		}
		if len(page.RunningSessionIDs) != 0 {
			return servicelifecycle.ErrHostBusy
		}
		for _, session := range page.Sessions {
			list, err := tasks.List(ctx, taskstream.ListRequest{SessionID: session.SessionID})
			if err != nil {
				return err
			}
			for _, task := range list.Tasks {
				if task.Running {
					return servicelifecycle.ErrHostBusy
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return errors.New("cli: legacy Session directory did not advance")
		}
		cursor = page.NextCursor
	}
	acknowledged, err := remote.ShutdownHost(ctx)
	if err != nil {
		return err
	}
	if acknowledged.ServerID != record.ServerID || acknowledged.InstanceID != record.InstanceID {
		return errors.New("cli: legacy Host shutdown acknowledgement does not match discovery")
	}
	return nil
}
