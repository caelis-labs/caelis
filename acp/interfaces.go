package acp

import sdksession "github.com/OnslaughtSnail/caelis/sdk/session"

// Projector converts canonical session events into ACP-compatible session/update
// notifications and request_permission payloads.
type Projector interface {
	ProjectEvent(*sdksession.Event) ([]Update, error)
	ProjectNotifications(*sdksession.Event) ([]SessionNotification, error)
	ProjectPermissionRequest(*sdksession.Event) (*RequestPermissionRequest, bool, error)
}
