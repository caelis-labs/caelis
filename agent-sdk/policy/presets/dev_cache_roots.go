package presets

import (
	"runtime"

	"github.com/caelis-labs/caelis/agent-sdk/policy/devcache"
)

func defaultDeveloperWritableRoots() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return devcache.WritableRoots()
}
