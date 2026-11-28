package gatewayapp

import (
	"context"
	"errors"
	"time"

	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/internal/hostownership"
)

// MaintainDoctorAttachments previews or applies one Session's attachment prune.
// A mutation requires the same exclusive local ownership as Store repairs.
func MaintainDoctorAttachments(ctx context.Context, storeDir, id string, before time.Time, ownership *hostownership.Authority) (sessionfile.AttachmentPrune, error) {
	if ownership != nil {
		release, ok := ownership.Pin(storeDir)
		if !ok {
			return sessionfile.AttachmentPrune{}, errors.New("attachment prune requires exclusive Store ownership")
		}
		defer release()
	}
	return doctorSessionStore(storeDir).PruneAttachments(ctx, id, before, ownership != nil)
}
