package agentprofiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/ports/agentprofile"
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

func LoadDirStatus(dir string) (LoadStatus, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return LoadStatus{}, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadStatus{}, nil
		}
		return LoadStatus{}, err
	}
	if !info.IsDir() {
		return LoadStatus{}, fmt.Errorf("gatewayapp: agent profiles path %s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
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
