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

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
}

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

func BuildInfo() Info {
	return Info{
		Version: String(),
		Commit:  strings.TrimSpace(Commit),
		Date:    strings.TrimSpace(Date),
	}
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
