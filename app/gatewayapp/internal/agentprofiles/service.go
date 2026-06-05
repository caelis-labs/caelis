package agentprofiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
)

const DefaultAgentsDirName = agentprofile.DefaultAgentsDirName

type LoadWarning struct {
	Path    string
	Message string
}

type LoadStatus struct {
	Profiles []agentprofile.Profile
	Warnings []LoadWarning
}

func LoadDir(dir string) ([]agentprofile.Profile, error) {
	status, err := LoadDirStatus(dir)
	if err != nil {
		return nil, err
	}
	return status.Profiles, nil
}

func LoadDirStatus(dir string) (LoadStatus, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return LoadStatus{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadStatus{}, nil
		}
		return LoadStatus{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	status := LoadStatus{}
	seen := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			status.Warnings = append(status.Warnings, LoadWarning{Path: path, Message: err.Error()})
			continue
		}
		profile, err := agentprofile.ParseMarkdown(path, data)
		if err != nil {
			status.Warnings = append(status.Warnings, LoadWarning{Path: path, Message: err.Error()})
			continue
		}
		if existing := seen[profile.ID]; existing != "" {
			status.Warnings = append(status.Warnings, LoadWarning{
				Path:    path,
				Message: fmt.Sprintf("duplicate profile id %q already loaded from %s", profile.ID, existing),
			})
			continue
		}
		seen[profile.ID] = path
		status.Profiles = append(status.Profiles, profile)
	}
	return status, nil
}

func ProfileBuiltIn(profile agentprofile.Profile) bool {
	return metadataBool(profile.Metadata, "built_in")
}

func ProfileSystemManaged(profile agentprofile.Profile) bool {
	return metadataBool(profile.Metadata, "system_managed")
}

func metadataBool(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "1", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
