//go:build windows

package windows

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("impl/sandbox/windows: elevated Windows sandbox backend is not implemented yet")
}
