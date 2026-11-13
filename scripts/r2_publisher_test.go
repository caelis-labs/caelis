package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestR2PublisherImmutableObjects(t *testing.T) {
	for _, scenario := range []string{"fresh", "identical", "conflict", "denied", "race", "readback"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			fixture := filepath.Join(dir, "fixture")
			remote := filepath.Join(dir, "remote")
			for _, path := range []string{bin, fixture, remote} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, data string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			var sums strings.Builder
			for _, platform := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64"} {
				name := "caelis_0.79.1_" + platform + ".tar.gz"
				data := "archive-" + platform
				write(filepath.Join(fixture, name), data, 0600)
				fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256([]byte(data)), name)
				if scenario == "identical" || scenario == "conflict" {
					write(filepath.Join(remote, name), data, 0600)
				}
			}
			write(filepath.Join(fixture, "checksums.txt"), sums.String(), 0600)
			if scenario == "identical" {
				write(filepath.Join(remote, "checksums.txt"), sums.String(), 0600)
			}
			if scenario == "conflict" {
				write(filepath.Join(remote, "caelis_0.79.1_darwin_amd64.tar.gz"), "different", 0600)
			}
			write(filepath.Join(remote, "old-release"), "preserve", 0600)
			write(filepath.Join(dir, "publish_latest_to_r2.sh"), readWorkflow(t, "./publish_latest_to_r2.sh"), 0700)
			// Archive-format validation is independently tested. Keep this fixture focused
			// on the real publisher's immutable writes and pointer ordering.
			write(filepath.Join(dir, "verify_release_archives.sh"), "#!/usr/bin/env bash\nexit 0\n", 0700)
			write(filepath.Join(bin, "gh"), `#!/usr/bin/env bash
set -eu
if [[ "$1" == api ]]; then echo v0.79.1; exit; fi
while [[ $# -gt 0 ]]; do
 if [[ "$1" == --dir ]]; then cp "${FIXTURE}/"* "$2/"; exit; fi
 shift
done
exit 1
`, 0700)
			write(filepath.Join(bin, "aws"), `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CALLS"
kind="$1"; operation="$2"; shift 2
if [[ "$kind" == s3 ]]; then
 [[ "$operation" == cp ]] || exit 99
 src="$1"; dst="$2"
 if [[ "$src" == s3://* ]]; then
  cp "${REMOTE}/${src##*/}" "$dst"
  if [[ "$SCENARIO" == readback ]]; then echo bad >> "$dst"; fi
 else
  [[ "$dst" == s3://bucket/latest.txt ]] || exit 98
  cp "$src" "${REMOTE}/latest.txt"
 fi
 exit
fi
key= body= condition= query=
while [[ $# -gt 0 ]]; do
 case "$1" in
 --key) key="$2";; --body) body="$2";; --if-none-match) condition="$2";; --query) query="$2";;
 esac
 shift 2
done
path="${REMOTE}/${key##*/}"
if [[ "$operation" == head-object ]]; then
 if [[ "$SCENARIO" == denied ]]; then echo 'An error occurred (403) when calling the HeadObject operation: Forbidden' >&2; exit 1; fi
 if [[ ! -f "$path" ]]; then echo 'An error occurred (404) when calling the HeadObject operation: Not Found' >&2; exit 1; fi
 if [[ "$query" == ContentLength ]]; then wc -c < "$path"; else echo '{}'; fi
elif [[ "$operation" == put-object ]]; then
 [[ "$condition" == '*' ]] || exit 97
 if [[ "$SCENARIO" == race ]]; then echo competitor > "$path"; fi
 [[ ! -e "$path" ]] || { echo 'PreconditionFailed' >&2; exit 1; }
 cp "$body" "$path"
else
 exit 96
fi
`, 0700)
			calls := filepath.Join(dir, "calls")
			cmd := exec.Command(testBash(t), filepath.Join(dir, "publish_latest_to_r2.sh"), "v0.79.1")
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE="+fixture, "REMOTE="+remote, "CALLS="+calls, "SCENARIO="+scenario, "GITHUB_REPOSITORY=caelis-labs/caelis", "GH_TOKEN=fixture", "R2_BUCKET=bucket", "R2_ENDPOINT=https://example.invalid")
			output, err := cmd.CombinedOutput()
			wantSuccess := scenario == "fresh" || scenario == "identical"
			if (err == nil) != wantSuccess {
				t.Fatalf("success=%v want %v: %v\n%s", err == nil, wantSuccess, err, output)
			}
			_, latestErr := os.Stat(filepath.Join(remote, "latest.txt"))
			if (latestErr == nil) != wantSuccess {
				t.Fatalf("latest pointer existence does not match success: %v", latestErr)
			}
			old, err := os.ReadFile(filepath.Join(remote, "old-release"))
			if err != nil || string(old) != "preserve" {
				t.Fatalf("historical object changed: %q %v", old, err)
			}
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "identical" && strings.Contains(string(log), "put-object") {
				t.Fatal("matching immutable objects were rewritten")
			}
			if scenario == "denied" && strings.Contains(string(log), "put-object") {
				t.Fatal("read failure treated as absence")
			}
			if scenario == "conflict" || scenario == "race" {
				data, err := os.ReadFile(filepath.Join(remote, "caelis_0.79.1_darwin_amd64.tar.gz"))
				if err != nil {
					t.Fatal(err)
				}
				expected := "different"
				if scenario == "race" {
					expected = "competitor\n"
				}
				if string(data) != expected {
					t.Fatalf("existing object overwritten: %q", data)
				}
			}
		})
	}
}
