package updater

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawReleaseSourceConfiguration(t *testing.T) {
	for _, tt := range []struct{ name, config, env, want string }{
		{name: "default", want: "https://releases.caelis.dev"},
		{name: "environment", env: " https://mirror.example/channel/ ", want: "https://mirror.example/channel"},
		{name: "explicit", config: " https://config.example/raw/ ", env: "https://env.example", want: "https://config.example/raw"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := New(Config{ReleasesBaseURL: tt.config, Env: func(key string) string {
				if key == EnvReleasesBaseURL {
					return tt.env
				}
				return ""
			}})
			if got := manager.cfg.ReleasesBaseURL; got != tt.want {
				t.Fatalf("base = %q, want %q", got, tt.want)
			}
			for _, platform := range []string{"linux", "darwin", "windows"} {
				for _, arch := range []string{"amd64", "arm64"} {
					name, err := rawArchiveName("v1.2.3", platform, arch)
					if err != nil {
						t.Fatal(err)
					}
					want := tt.want + "/releases/v1.2.3/caelis_1.2.3_" + platform + "_" + arch + ".tar.gz"
					if got := manager.releaseAssetURL("v1.2.3", name); got != want {
						t.Fatalf("asset = %q, want %q", got, want)
					}
				}
			}
		})
	}
}

func TestRawCheckValidatesLatestChannel(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		want       string
	}{
		{name: "release", body: "v1.2.3\r\n", want: "v1.2.3"},
		{name: "prerelease", body: "v1.2.3-rc.1\n", want: "v1.2.3-rc.1"},
		{name: "empty"}, {name: "html", body: "<html>Not Found</html>"},
		{name: "github-json", body: `{"tag_name":"v1.2.3"}`},
		{name: "short", body: "v1.2"}, {name: "no-prefix", body: "1.2.3"},
		{name: "path", body: "v1.2.3/../../other"}, {name: "multiple", body: "v1.2.3\nv1.2.4"},
		{name: "oversize", body: "v1.2.3" + strings.Repeat(" ", 1024)},
		{name: "unavailable", body: "v1.2.3", status: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.String() != "http://updater.test/channel/latest.txt" {
					t.Errorf("unexpected request: %s", r.URL)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				fmt.Fprint(w, tt.body)
			}))
			manager := New(Config{CurrentVersion: "v1.0.0", StoreDir: t.TempDir(), HTTPClient: server.Client(), Env: func(key string) string {
				if key == EnvReleasesBaseURL {
					return server.URL + "/channel/"
				}
				return ""
			}})
			result, err := manager.Check(context.Background(), CheckOptions{Force: true})
			if tt.want == "" {
				if err == nil || result.Checked || result.Available {
					t.Fatalf("invalid channel accepted: %#v, %v", result, err)
				}
				return
			}
			if err != nil || result.LatestVersion != tt.want || !result.Available {
				t.Fatalf("Check = %#v, %v", result, err)
			}
		})
	}
}

func TestRawUpdateRejectsMissingOrMismatchedRelease(t *testing.T) {
	for _, failure := range []string{"archive-missing", "checksums-missing", "checksum-mismatch"} {
		t.Run(failure, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "caelis")
			if err := os.WriteFile(exe, []byte("old-binary"), 0700); err != nil {
				t.Fatal(err)
			}
			archive := releaseArchive(t, "caelis", []byte("untrusted-binary"))
			server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest.txt":
					fmt.Fprint(w, "v1.2.0\n")
				case "/releases/v1.2.0/caelis_1.2.0_linux_amd64.tar.gz":
					if failure == "archive-missing" {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(archive)
				case "/releases/v1.2.0/checksums.txt":
					if failure == "checksums-missing" {
						http.NotFound(w, r)
						return
					}
					fmt.Fprint(w, strings.Repeat("0", 64)+"  caelis_1.2.0_linux_amd64.tar.gz\n")
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			manager := New(Config{CurrentVersion: "v1.0.0", StoreDir: t.TempDir(), Executable: exe, GOOS: "linux", GOARCH: "amd64", ReleasesBaseURL: server.URL, HTTPClient: server.Client(), Env: emptyUpdaterEnv})
			result, err := manager.Update(context.Background(), UpdateOptions{})
			if err == nil || result.Updated || result.Deferred {
				t.Fatalf("Update = %#v, %v", result, err)
			}
			data, err := os.ReadFile(exe)
			if err != nil || string(data) != "old-binary" {
				t.Fatalf("executable changed: %q, %v", data, err)
			}
		})
	}
}
