package version

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

var (
	Version = ""
	Commit  = ""
	Date    = ""
)

func String() string {
	if value := strings.TrimSpace(os.Getenv("CAELIS_VERSION")); value != "" {
		return value
	}
	if value := normalizedVersion(Version); value != "" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if value := normalizedVersion(info.Main.Version); value != "" {
			return value
		}
	}
	if value := versionFromFile(); value != "" {
		return value
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

func versionFromFile() string {
	start, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir := start; dir != ""; {
		data, err := os.ReadFile(filepath.Join(dir, "VERSION"))
		if err == nil {
			return firstNonEmptyLine(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func firstNonEmptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
