//go:build !linux

package landlock

import (
	"fmt"

	"github.com/caelis-labs/caelis/ports/sandbox"
)

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("landlock sandbox is only supported on linux")
}
