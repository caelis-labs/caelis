//go:build !unix

package gatewayapp

import (
	"fmt"
	"os"

	"github.com/caelis-labs/caelis/control/application"
)

// No portable Root.OpenFile flag prevents a non-regular replacement from
// blocking on these platforms. Native application workspace-write execution is
// unavailable there; never fall back to a potentially blocking open.
func openConfinedArtifact(_ *os.Root, _ string) (*os.File, error) {
	return nil, fmt.Errorf("%w: confined artifact reads are unsupported on this platform", application.ErrInvalid)
}
