package acpinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/caelis-labs/caelis/control/agents"
)

const maxArchiveBytes int64 = 1 << 30

// Install validates the user's confirmation against the official source, then
// publishes the whole bundle into a new or empty directory. installed reports
// whether the destination was committed, including when a later check fails.
func (s Source) Install(ctx context.Context, confirmation agents.RuntimeInstallation) (installed bool, err error) {
	plan, err := s.Plan(ctx)
	if err != nil {
		return false, err
	}
	if confirmation.ArchiveURL != plan.ArchiveURL || confirmation.SHA256 != plan.SHA256 {
		return false, errors.New("the official download changed; go back and review the installation again")
	}
	return installArchive(ctx, confirmation, s.Command, &http.Client{
		Timeout: 15 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 || !s.allowsArchive(req.URL.String()) {
				return errors.New("unexpected download redirect")
			}
			return nil
		},
	})
}

func installArchive(ctx context.Context, confirmation agents.RuntimeInstallation, command string, client *http.Client) (bool, error) {
	dest := filepath.Clean(confirmation.Directory)
	if !filepath.IsAbs(dest) || dest == filepath.Dir(dest) {
		return false, errors.New("choose an absolute installation directory")
	}
	if info, err := os.Lstat(dest); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("choose a new or empty installation directory")
		}
		entries, err := os.ReadDir(dest)
		if err != nil {
			return false, err
		}
		if len(entries) > 0 {
			return false, errors.New("this directory already contains files; choose an empty directory or use Manual setup to update")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return false, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".acp-install-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stage)
	archive, err := os.CreateTemp(stage, "download-*.zip")
	if err != nil {
		return false, err
	}
	archivePath := archive.Name()
	defer archive.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, confirmation.ArchiveURL, nil)
	if err != nil {
		return false, err
	}
	res, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("download failed; try again or use Manual setup: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("download returned HTTP %d; try again or use Manual setup", res.StatusCode)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(res.Body, maxArchiveBytes+1))
	if err != nil {
		return false, err
	}
	if n > maxArchiveBytes {
		return false, errors.New("the runtime archive exceeds the download size limit")
	}
	if err := archive.Close(); err != nil {
		return false, err
	}
	if confirmation.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != confirmation.SHA256 {
		return false, errors.New("the runtime download failed checksum verification")
	}
	bundle := filepath.Join(stage, "bundle")
	if err := extractArchive(ctx, archivePath, bundle, command); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Windows cannot rename over an existing empty directory. Remove only an
	// empty directory; concurrent user files make Remove fail without touching
	// those files. Rename likewise cannot replace a nonempty directory.
	if info, statErr := os.Lstat(dest); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("the installation directory changed; choose a new or empty directory")
		}
		if err := os.Remove(dest); err != nil {
			return false, fmt.Errorf("the installation directory is no longer empty: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	if err := os.Rename(bundle, dest); err != nil {
		return false, fmt.Errorf("could not install into %s: %w", dest, err)
	}
	return true, nil
}
