package kernel

import "strings"

type SurfaceClass string

const (
	SurfaceClassInteractive SurfaceClass = "interactive"
	SurfaceClassBatch       SurfaceClass = "batch"
)

func ClassifySurface(surface string) SurfaceClass {
	normalized := strings.ToLower(strings.TrimSpace(surface))
	switch {
	case normalized == "":
		return SurfaceClassInteractive
	case strings.HasPrefix(normalized, "headless"),
		strings.HasPrefix(normalized, "batch"),
		strings.HasPrefix(normalized, "cron"),
		strings.HasPrefix(normalized, "export"),
		strings.HasPrefix(normalized, "script"):
		return SurfaceClassBatch
	default:
		return SurfaceClassInteractive
	}
}
