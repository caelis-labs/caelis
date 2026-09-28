package gatewayapp

import (
	"os"
	"strings"
)

// runtimeCommandEnvironment snapshots the Host's user environment without its
// private Control/collaboration connection. Process configuration is applied by
// the SDK after this boundary; neither Session assembly nor attach mutates the
// Host environment. This is credential hygiene, not filesystem authorization.
func runtimeCommandEnvironment() []string {
	ambient := os.Environ()
	out := make([]string, 0, len(ambient))
	for _, entry := range ambient {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "CAELIS_CONTROL_TOKEN", "CAELIS_CONTROL_TOKEN_FILE", "CAELIS_CONTROL_URL",
			"CAELIS_COLLABORATION_TOKEN", "CAELIS_COLLABORATION_URL", "CAELIS_COLLABORATION_TOOLS":
			continue
		}
		out = append(out, entry)
	}
	return out
}
