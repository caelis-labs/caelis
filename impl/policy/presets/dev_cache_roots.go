package presets

import (
	"runtime"

	"github.com/OnslaughtSnail/caelis/impl/policy/devcache"
)

func defaultDeveloperWritableRoots() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return devcache.WritableRoots()
}
