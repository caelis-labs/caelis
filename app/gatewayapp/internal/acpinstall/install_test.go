package acpinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
)

type archiveEntry struct {
	name, body string
	mode       os.FileMode
}

func archiveFixture(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		h.SetMode(entry.mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func archiveClient(data []byte) *http.Client {
	return &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
}

func TestInstallPublishesCompleteExecutableBundleAndPreservesExistingFiles(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "runtime")
	data := archiveFixture(t, archiveEntry{"agy_acp_server.par", "runtime", 0o600}, archiveEntry{"localharness_external", "companion", 0o600}, archiveEntry{"data/config.json", "{}", 0o600})
	hash := sha256.Sum256(data)
	req := agents.RuntimeInstallation{Directory: dest, ArchiveURL: "https://dl.google.com/runtime.zip", SHA256: hex.EncodeToString(hash[:])}
	installed, err := installArchive(t.Context(), req, "agy_acp_server.par", archiveClient(data))
	if err != nil || !installed {
		t.Fatalf("install = %v, %v", installed, err)
	}
	for _, name := range []string{"agy_acp_server.par", "localharness_external", "data/config.json"} {
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && !strings.Contains(name, "/") && info.Mode()&0o100 == 0 {
			t.Fatalf("%s is not executable", name)
		}
	}
	installed, err = installArchive(t.Context(), req, "agy_acp_server.par", archiveClient([]byte("replacement")))
	if err == nil || installed {
		t.Fatal("existing user files were overwritten")
	}
	got, err := os.ReadFile(filepath.Join(dest, "agy_acp_server.par"))
	if err != nil || string(got) != "runtime" {
		t.Fatal("existing runtime changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Fatalf("installation left staging artifacts: %v", entries)
	}
}

func TestInstallRejectsUnsafeAndIncompleteArchivesWithoutPublishing(t *testing.T) {
	valid := archiveEntry{"agy_acp_server.par", "runtime", 0o700}
	for _, test := range []struct {
		name     string
		entries  []archiveEntry
		checksum string
	}{
		{"traversal", []archiveEntry{valid, {"../escape", "bad", 0o600}}, ""},
		{"absolute", []archiveEntry{valid, {"/escape", "bad", 0o600}}, ""},
		{"windows-path", []archiveEntry{valid, {"C:\\escape", "bad", 0o600}}, ""},
		{"symlink", []archiveEntry{valid, {"link", "../escape", os.ModeSymlink | 0o777}}, ""},
		{"duplicate", []archiveEntry{valid, valid}, ""},
		{"missing-runtime", []archiveEntry{{"other", "data", 0o600}}, ""},
		{"checksum", []archiveEntry{valid}, strings.Repeat("0", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "runtime")
			installed, err := installArchive(t.Context(), agents.RuntimeInstallation{Directory: dest, ArchiveURL: "https://dl.google.com/runtime.zip", SHA256: test.checksum}, "agy_acp_server.par", archiveClient(archiveFixture(t, test.entries...)))
			if installed || err == nil {
				t.Fatal("unsafe archive installed")
			}
			files, _ := os.ReadDir(parent)
			if len(files) != 0 {
				t.Fatalf("failed installation left files: %v", files)
			}
		})
	}
}

func TestInstallCancellationDoesNotPublishOrLeaveDownloads(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	client := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archiveFixture(t, archiveEntry{"agy_acp_server.par", "runtime", 0o700}))), Header: make(http.Header)}, nil
	})}
	installed, err := installArchive(ctx, agents.RuntimeInstallation{Directory: filepath.Join(parent, "runtime"), ArchiveURL: "https://dl.google.com/runtime.zip"}, "agy_acp_server.par", client)
	if installed || err == nil {
		t.Fatal("cancelled install published")
	}
	files, _ := os.ReadDir(parent)
	if len(files) != 0 {
		t.Fatalf("cancel left files: %v", files)
	}
}

func TestSourceRejectsUnapprovedDownloadDestinations(t *testing.T) {
	s := Source{ArchiveHost: "dl.google.com", ArchivePathPrefix: "/agy-extensions/releases/"}
	for _, url := range []string{"http://dl.google.com/agy-extensions/releases/a.zip", "https://evil.test/agy-extensions/releases/a.zip", "https://dl.google.com/other.zip", "https://user:secret@dl.google.com/agy-extensions/releases/a.zip"} {
		if s.allowsArchive(url) {
			t.Fatalf("accepted %s", url)
		}
	}
}

func TestInstallSupportsEmptyDirectoryAndPreservesConcurrentUserFiles(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint(concurrent), func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "runtime")
			if err := os.Mkdir(dest, 0o700); err != nil {
				t.Fatal(err)
			}
			data := archiveFixture(t, archiveEntry{"agy_acp_server.par", "runtime", 0o700})
			client := archiveClient(data)
			if concurrent {
				client.Transport = testTransport(func(*http.Request) (*http.Response, error) {
					if err := os.WriteFile(filepath.Join(dest, "user-file"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
				})
			}
			installed, err := installArchive(t.Context(), agents.RuntimeInstallation{Directory: dest, ArchiveURL: "https://dl.google.com/runtime.zip"}, "agy_acp_server.par", client)
			if concurrent {
				if installed || err == nil {
					t.Fatal("published over concurrent user files")
				}
				got, err := os.ReadFile(filepath.Join(dest, "user-file"))
				if err != nil || string(got) != "keep" {
					t.Fatalf("user file changed: %q, %v", got, err)
				}
			} else if !installed || err != nil {
				t.Fatalf("empty directory installation failed: %v", err)
			}
		})
	}
}

func TestSourcePlanAndInstallBindTheConfirmedOfficialArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	source := Source{RegistryURL: "https://registry.test/agent.json", DirectoryName: "runtime", Command: "agy_acp_server.par", ArchiveHost: "dl.google.com", ArchivePathPrefix: "/agy-extensions/releases/"}
	data := archiveFixture(t, archiveEntry{source.Command, "runtime", 0o700})
	archive := "https://dl.google.com/agy-extensions/releases/runtime.zip"
	downloads := 0
	transport := http.DefaultTransport
	http.DefaultTransport = testTransport(func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.URL.String() == source.RegistryURL {
			platform := runtime.GOOS + "-" + map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
			body = []byte(fmt.Sprintf(`{"distribution":{"binary":{%q:{"archive":%q,"cmd":"./agy_acp_server.par"}}}}`, platform, archive))
		} else if req.URL.String() == archive {
			downloads++
			body = data
		} else {
			t.Fatalf("unapproved request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	plan, err := source.Plan(t.Context())
	if err != nil || downloads != 0 || plan.ArchiveURL != archive || !strings.Contains(strings.Join(plan.ManualSteps, "\n"), plan.Directory) {
		t.Fatalf("plan = %#v, %v; downloads=%d", plan, err, downloads)
	}
	req := agents.RuntimeInstallation{Directory: filepath.Join(t.TempDir(), "runtime"), ArchiveURL: plan.ArchiveURL}
	archive = "https://dl.google.com/agy-extensions/releases/new.zip"
	if installed, err := source.Install(t.Context(), req); err == nil || installed || downloads != 0 {
		t.Fatalf("changed source installed: %v, %v; downloads=%d", installed, err, downloads)
	}
	req.ArchiveURL = archive
	if installed, err := source.Install(t.Context(), req); err != nil || !installed || downloads != 1 {
		t.Fatalf("confirmed source failed: %v, %v; downloads=%d", installed, err, downloads)
	}
}
