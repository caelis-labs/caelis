package presets

import (
	"runtime"

	"github.com/caelis-labs/caelis/impl/policy/devcache"
)

func defaultDeveloperWritableRoots() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return devcache.WritableRoots()
}
