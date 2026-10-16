package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"golang.org/x/mod/semver"
)

// Pre-v0.72 Hosts lack atomic idle shutdown; v0.72.0/1 also count detached
// Tasks as busy. This transition protects live Session turns, then uses public
// shutdown. A turn admitted between the read and shutdown can be interrupted.
// Remove this boundary when the minimum managed Host is v0.72.2.
func shutdownLegacyIdleHost(ctx context.Context, remote *httpclient.Client, record controlserver.DiscoveryRecord, info appserver.ServerInfo) error {
	v := "v" + strings.TrimPrefix(info.DistributionVersion, "v")
	if info.BuildKind != "release" || !semver.IsValid(v) || semver.Compare(v, "v0.72.2") >= 0 {
		return errors.New("cli: Host does not provide idle replacement capability")
	}
	status, err := remote.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		return err
	}
	r := status.Runtime
	if r.Running {
		return servicelifecycle.ErrHostBusy
	}
	// Legacy /status only samples the default Runtime. Page the public live
	// Session activity view as well; detached Tasks deliberately do not pin it.
	cursor := ""
	for {
		page, err := remote.ListSessions(ctx, appserver.ListSessionsRequest{Cursor: cursor, Limit: 100})
		if err != nil {
			return err
		}
		if len(page.RunningSessionIDs) != 0 {
			return servicelifecycle.ErrHostBusy
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
