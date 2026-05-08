package version

import (
	"runtime/debug"
	"strings"
)

var (
	Version = ""
	Commit  = ""
	Date    = ""
)

func String() string {
	if value := normalizedVersion(Version); value != "" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if value := normalizedVersion(info.Main.Version); value != "" {
			return value
		}
	}
	return "dev"
}

func normalizedVersion(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "", "(devel)", "dev":
		return ""
	default:
		return value
	}
}
