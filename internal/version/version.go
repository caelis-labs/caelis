package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync"
)

const (
	BuildKindDev     = "dev"
	BuildKindRelease = "release"
)

var (
	Version   = ""
	Commit    = ""
	Date      = ""
	BuildID   = ""
	BuildKind = ""

	currentExecutableDigest struct {
		once  sync.Once
		value string
	}
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	BuildID   string `json:"build_id"`
	BuildKind string `json:"build_kind"`
}

func String() string {
	if stamped := strings.TrimSpace(Version); stamped != "" {
		if value := normalizedVersion(stamped); value != "" {
			return value
		}
		return "dev"
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if value := normalizedVersion(info.Main.Version); value != "" {
			return value
		}
	}
	return "dev"
}

func BuildInfo() Info {
	version := String()
	commit := strings.TrimSpace(Commit)
	date := strings.TrimSpace(Date)
	buildID := strings.TrimSpace(BuildID)
	if buildID == "" {
		buildID = derivedBuildID(commit, date)
	}
	return Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		BuildID:   buildID,
		BuildKind: normalizeBuildKind(BuildKind),
	}
}

func normalizeBuildKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case BuildKindRelease:
		return BuildKindRelease
	case BuildKindDev:
		return BuildKindDev
	}
	return BuildKindDev
}

func derivedBuildID(commit string, date string) string {
	commit = strings.TrimSpace(commit)
	date = strings.TrimSpace(date)
	if commit != "" && date != "" {
		return commit + "@" + date
	}
	if commit != "" {
		return commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision string
		var vcsTime string
		var modified string
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = strings.TrimSpace(setting.Value)
			case "vcs.time":
				vcsTime = strings.TrimSpace(setting.Value)
			case "vcs.modified":
				modified = strings.TrimSpace(setting.Value)
			}
		}
		if revision != "" && modified != "true" {
			if vcsTime != "" {
				revision += "@" + vcsTime
			}
			return revision
		}
	}
	return executableContentBuildID()
}

func executableContentBuildID() string {
	currentExecutableDigest.once.Do(func() {
		path, err := os.Executable()
		if err != nil {
			currentExecutableDigest.value = "executable-unavailable"
			return
		}
		currentExecutableDigest.value, err = contentBuildID(path)
		if err != nil {
			currentExecutableDigest.value = "executable-unavailable"
		}
	})
	return currentExecutableDigest.value
}

func contentBuildID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
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
