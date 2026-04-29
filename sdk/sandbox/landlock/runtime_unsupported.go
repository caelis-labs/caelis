//go:build !linux

package landlock

import (
	"fmt"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

func newRuntime(cfg Config) (sdksandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("landlock sandbox is only supported on linux")
}
