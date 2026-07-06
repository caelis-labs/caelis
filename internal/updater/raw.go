package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func (m *Manager) installRaw(ctx context.Context, latest string, progress io.Writer) (bool, error) {
	archiveName, err := rawArchiveName(latest, m.cfg.GOOS, m.cfg.GOARCH)
	if err != nil {
		return false, err
	}
	binaryName := rawBinaryName(m.cfg.GOOS)
	client := m.installHTTPClient()
	tmpDir, err := os.MkdirTemp("", "caelis-update-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, archiveName)
	archiveURL := m.releaseAssetURL(latest, archiveName)
	writeUpdateProgress(progress, "Downloading %s\n", archiveName)
	if err := m.downloadFile(ctx, client, archiveURL, archivePath, archiveName, progress); err != nil {
		return false, err
	}
	writeUpdateProgress(progress, "Download complete.\n")
	writeUpdateProgress(progress, "Downloading checksums\n")
	checksums, err := m.downloadBytes(ctx, client, m.releaseAssetURL(latest, "checksums.txt"), 2<<20)
	if err != nil {
		return false, err
	}
	writeUpdateProgress(progress, "Verifying checksum\n")
	if err := verifyChecksum(archiveName, archivePath, checksums); err != nil {
		return false, err
	}
	writeUpdateProgress(progress, "Checksum OK.\n")
	extracted := filepath.Join(tmpDir, binaryName)
	writeUpdateProgress(progress, "Extracting %s\n", binaryName)
	if err := extractBinary(archivePath, binaryName, extracted); err != nil {
		return false, err
	}
	writeUpdateProgress(progress, "Installing %s\n", binaryName)
	deferred, err := m.replaceExecutable(extracted)
	if err != nil {
		return false, err
	}
	if deferred {
		writeUpdateProgress(progress, "Update scheduled; restart caelis after this process exits.\n")
	} else {
		writeUpdateProgress(progress, "Install complete.\n")
	}
	return deferred, nil
}

func (m *Manager) installHTTPClient() *http.Client {
	if m.cfg.HTTPClient == nil {
		return &http.Client{}
	}
	clone := *m.cfg.HTTPClient
	clone.Timeout = 0
	return &clone
}

func writeUpdateProgress(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, format, args...)
}

func (m *Manager) releaseAssetURL(version string, name string) string {
	return strings.TrimRight(m.cfg.GitHubReleaseBase, "/") + "/" + displayVersion(version) + "/" + name
}

func rawArchiveName(version string, goos string, goarch string) (string, error) {
	osPart := strings.ToLower(strings.TrimSpace(goos))
	archPart := strings.ToLower(strings.TrimSpace(goarch))
	switch osPart {
	case "linux", "darwin", "windows":
	default:
		return "", fmt.Errorf("unsupported update OS: %s", goos)
	}
	switch archPart {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported update architecture: %s", goarch)
	}
	return fmt.Sprintf("caelis_%s_%s_%s.tar.gz", npmVersion(version), osPart, archPart), nil
}

func rawBinaryName(goos string) string {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return "caelis.exe"
	}
	return "caelis"
}

func (m *Manager) downloadFile(ctx context.Context, client *http.Client, url string, dest string, label string, progress io.Writer) error {
	if client == nil {
		client = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "caelis-updater")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	reader := io.Reader(resp.Body)
	if progress != nil {
		reader = &downloadProgressReader{
			reader:   resp.Body,
			total:    resp.ContentLength,
			label:    label,
			progress: progress,
		}
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (m *Manager) downloadBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	if client == nil {
		client = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "caelis-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

type downloadProgressReader struct {
	reader      io.Reader
	total       int64
	downloaded  int64
	lastPercent int
	label       string
	progress    io.Writer
}

func (p *downloadProgressReader) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	if n > 0 {
		p.downloaded += int64(n)
		p.report()
	}
	return n, err
}

func (p *downloadProgressReader) report() {
	if p.progress == nil {
		return
	}
	if p.total > 0 {
		percent := int(p.downloaded * 100 / p.total)
		if percent < 100 && percent-p.lastPercent < 5 {
			return
		}
		p.lastPercent = percent
		writeUpdateProgress(
			p.progress,
			"%s: %s / %s (%d%%)\n",
			p.label,
			formatDownloadSize(p.downloaded),
			formatDownloadSize(p.total),
			percent,
		)
		return
	}
	const step = 5 << 20
	milestone := int(p.downloaded / step)
	if milestone <= p.lastPercent {
		return
	}
	p.lastPercent = milestone
	writeUpdateProgress(p.progress, "%s: %s downloaded\n", p.label, formatDownloadSize(p.downloaded))
}

func formatDownloadSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func verifyChecksum(archiveName string, archivePath string, checksums []byte) error {
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == archiveName || filepath.Base(fields[1]) == archiveName {
			expected = strings.ToLower(strings.TrimSpace(fields[0]))
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum for %s not found", archiveName)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum verification failed for %s", archiveName)
	}
	return nil
}

func extractBinary(archivePath string, binaryName string, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, reader)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Chmod(dest, 0o755)
	}
	return fmt.Errorf("%s not found in release archive", binaryName)
}

func (m *Manager) replaceExecutable(newBinary string) (bool, error) {
	dest := strings.TrimSpace(m.cfg.Executable)
	if dest == "" {
		exe, err := os.Executable()
		if err != nil {
			return false, err
		}
		dest = exe
	}
	dest = filepath.Clean(dest)
	if strings.EqualFold(m.cfg.GOOS, "windows") {
		return m.scheduleWindowsReplacement(newBinary, dest)
	}
	tmp, err := copyToDestinationTemp(newBinary, dest)
	if err != nil {
		return false, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return false, nil
}

func copyToDestinationTemp(src string, dest string) (string, error) {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".caelis-update-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	in, err := os.Open(src)
	if err != nil {
		_ = tmp.Close()
		return "", err
	}
	_, copyErr := io.Copy(tmp, in)
	closeInErr := in.Close()
	syncErr := tmp.Sync()
	closeTmpErr := tmp.Close()
	switch {
	case copyErr != nil:
		return "", copyErr
	case closeInErr != nil:
		return "", closeInErr
	case syncErr != nil:
		return "", syncErr
	case closeTmpErr != nil:
		return "", closeTmpErr
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	committed = true
	return tmpPath, nil
}

func (m *Manager) scheduleWindowsReplacement(newBinary string, dest string) (bool, error) {
	dir := filepath.Dir(dest)
	tmpExe, err := copyToDestinationTemp(newBinary, dest)
	if err != nil {
		return false, err
	}
	finalTmpExe := strings.TrimSuffix(tmpExe, ".tmp") + ".exe"
	if err := os.Rename(tmpExe, finalTmpExe); err != nil {
		_ = os.Remove(tmpExe)
		return false, err
	}
	script := filepath.Join(dir, ".caelis-update-"+strconv.Itoa(os.Getpid())+".cmd")
	body := strings.Join([]string{
		"@echo off",
		"ping 127.0.0.1 -n 2 > nul",
		"move /Y " + windowsQuote(finalTmpExe) + " " + windowsQuote(dest) + " > nul",
		"del \"%~f0\" > nul 2> nul",
		"",
	}, "\r\n")
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		_ = os.Remove(finalTmpExe)
		return false, err
	}
	cmd := exec.Command("cmd.exe", "/C", "start", "", "/B", script)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(script)
		_ = os.Remove(finalTmpExe)
		return false, err
	}
	return true, nil
}

func windowsQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
