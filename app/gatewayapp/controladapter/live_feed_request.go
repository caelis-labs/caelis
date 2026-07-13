package controladapter

import (
	"github.com/caelis-labs/caelis/internal/controlclient/turningress"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

func streamRequestFromACPEvent(env eventstream.Envelope) (acpprojector.StreamRequest, bool) {
	return turningress.StreamRequestFromACPEvent(env)
}
