// Package acpinstall installs an explicitly confirmed official ACP archive into
// a user-owned directory. It owns no version catalog, cache, or update policy.
package acpinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/agents"
)

// Source is a catalog-owned official distribution entry, never user input.
type Source struct {
	RegistryURL, DirectoryName, Command string
	ArchiveHost, ArchivePathPrefix      string
	Args                                []string
}

type binaryDistribution struct {
	Archive string   `json:"archive"`
	Cmd     string   `json:"cmd"`
	Args    []string `json:"args"`
	SHA256  string   `json:"sha256"`
}

// Setup describes installation without making a network request.
func (s Source) Setup() (agents.RuntimeSetup, error) {
	var base string
	var err error
	if runtime.GOOS == "windows" {
		base, err = os.UserCacheDir()
	} else {
		base, err = os.UserHomeDir()
		base = filepath.Join(base, ".local", "share")
	}
	if err != nil {
		return agents.RuntimeSetup{}, err
	}
	return agents.RuntimeSetup{Command: s.Command, Directory: filepath.Join(base, s.DirectoryName)}, nil
}

// Plan resolves the current official download for the Host platform. The
// returned URL and destination must be shown before submitting an installation.
func (s Source) Plan(ctx context.Context) (agents.RuntimeSetup, error) {
	setup, err := s.Setup()
	if err != nil {
		return setup, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.RegistryURL, nil)
	if err != nil {
		return setup, err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected registry redirect") }}
	res, err := client.Do(req)
	if err != nil {
		return setup, fmt.Errorf("could not check the official download; try again: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return setup, fmt.Errorf("official download catalog returned HTTP %d; try again", res.StatusCode)
	}
	var entry struct {
		Distribution struct {
			Binary map[string]binaryDistribution `json:"binary"`
		} `json:"distribution"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&entry); err != nil {
		return setup, errors.New("could not read the official download catalog")
	}
	return s.resolvePlan(setup, entry.Distribution.Binary, runtime.GOOS, runtime.GOARCH)
}

func (s Source) resolvePlan(setup agents.RuntimeSetup, binaries map[string]binaryDistribution, goos, goarch string) (agents.RuntimeSetup, error) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	platform := goos + "-" + arch
	binary, ok := binaries[platform]
	if arch == "" || !ok {
		return setup, fmt.Errorf("no official runtime download is available for %s", platform)
	}
	if strings.TrimPrefix(binary.Cmd, "./") != s.Command || !slices.Equal(binary.Args, s.Args) || !s.allowsArchive(binary.Archive) {
		return setup, errors.New("the official launch instructions changed; this installation method is unavailable")
	}
	setup.ArchiveURL, setup.SHA256 = binary.Archive, strings.ToLower(binary.SHA256)
	setup.ManualSteps = manualSetupSteps(setup, goos)
	setup.InstallPrompt = installPrompt(setup, goos, goarch, s.Args)
	return setup, nil
}

func (s Source) allowsArchive(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Host == s.ArchiveHost &&
		strings.HasPrefix(u.Path, s.ArchivePathPrefix) && strings.HasSuffix(u.Path, ".zip") && u.RawQuery == "" && u.Fragment == ""
}
