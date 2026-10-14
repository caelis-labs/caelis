package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These fixtures stop before a real signature or Apple submission. They verify
// command attribution and that every failure restores the runner search list.
func TestMacOSCLISigningFailureRestoresKeychainSearchList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release signing uses macOS shell tools")
	}
	for _, tc := range []struct{ stage, diagnostic string }{
		{"activate", "failed to activate the temporary signing keychain search list"},
		{"g2", "failed to import the Apple Developer ID G2 intermediate"},
		{"identity", "failed to query Developer ID signing identities"},
		{"codesign", "Developer ID codesign failed for Darwin amd64 CLI"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			state := filepath.Join(root, "state")
			runnerTemp := filepath.Join(root, "runner-temp")
			dist := filepath.Join(root, "dist")
			for _, dir := range []string{bin, state, runnerTemp, dist} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64"} {
				name := "caelis_1.2.3_" + target + ".tar.gz"
				if err := os.WriteFile(filepath.Join(dist, name), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			original := "    \"original.keychain-db\"\n"
			if err := os.WriteFile(filepath.Join(state, "list"), []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			fake := map[string]string{
				"security": `#!/usr/bin/env bash
set -euo pipefail
action=$1
printf '%s\n' "$action" >> "$FAKE_KEYCHAIN_STATE/events"
case "$action" in
  list-keychains)
    if [[ "${4:-}" == -s ]]; then
      shift 4
      : > "$FAKE_KEYCHAIN_STATE/list"
      for item in "$@"; do printf '    "%s"\n' "$item" >> "$FAKE_KEYCHAIN_STATE/list"; done
      if [[ "$FAKE_FAIL_STAGE" == activate && ! -f "$FAKE_KEYCHAIN_STATE/activate-failed" ]]; then
        touch "$FAKE_KEYCHAIN_STATE/activate-failed"
        echo 'simulated search list write failure' >&2
        exit 1
      fi
    else
      cat "$FAKE_KEYCHAIN_STATE/list"
    fi
    ;;
  create-keychain)
    for last; do :; done
    touch "$last"
    ;;
  set-keychain-settings|unlock-keychain|set-key-partition-list) ;;
  import)
    if [[ "$2" == *AppleG2.cer && "$FAKE_FAIL_STAGE" == g2 ]]; then
      echo 'simulated intermediate import failure' >&2
      exit 1
    fi
    ;;
  find-identity)
    for last; do :; done
    head -1 "$FAKE_KEYCHAIN_STATE/list" | grep -F -- "\"$last\"" >/dev/null || {
      echo 'temporary keychain absent from search list' >&2; exit 1;
    }
    if [[ "$FAKE_FAIL_STAGE" == identity ]]; then
      echo 'simulated identity query failure' >&2
      exit 1
    fi
    echo '  1) 0123456789abcdef0123456789abcdef01234567 "Developer ID Application: Fixture (ABCDEFGHIJ)"'
    ;;
  delete-keychain)
    rm -f "$2"
    ;;
  *) exit 2 ;;
esac
`,
				"uname": `#!/usr/bin/env bash
echo Darwin
`,
				"curl": `#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
  if [[ "$1" == -o ]]; then printf 'public fixture' > "$2"; exit 0; fi
  shift
done
exit 2
`,
				"shasum": `#!/usr/bin/env bash
cat >/dev/null
echo 'public intermediate: OK'
`,
				"tar": `#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
  if [[ "$1" == -C ]]; then touch "$2/caelis"; exit 0; fi
  shift
done
exit 2
`,
				"lipo": `#!/usr/bin/env bash
echo x86_64
`,
				"codesign": `#!/usr/bin/env bash
echo 'error: The specified item could not be found in the keychain.' >&2
exit 1
`,
			}
			for name, content := range fake {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(testBash(t), "./sign_notarize_cli.sh", "1.2.3", dist)
			cmd.Dir = "../scripts"
			cmd.Env = append(os.Environ(),
				"PATH="+bin+":"+os.Getenv("PATH"), "RUNNER_TEMP="+runnerTemp,
				"FAKE_KEYCHAIN_STATE="+state, "FAKE_FAIL_STAGE="+tc.stage,
				"APPLE_TEAM_ID=ABCDEFGHIJ", "APPLE_DEVELOPER_ID_P12_BASE64=ZHVtbXk=",
				"APPLE_DEVELOPER_ID_P12_PASSWORD=synthetic", "APPLE_NOTARY_APPLE_ID=fixture@example.test",
				"APPLE_NOTARY_PASSWORD=synthetic",
			)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.diagnostic) {
				t.Fatalf("want %s failure, got %v: %s", tc.stage, err, output)
			}
			list, err := os.ReadFile(filepath.Join(state, "list"))
			if err != nil || string(list) != original {
				t.Fatalf("original keychain search list not restored: %q: %v", list, err)
			}
			events, err := os.ReadFile(filepath.Join(state, "events"))
			if err != nil || !strings.Contains(string(events), "delete-keychain\n") {
				t.Fatalf("temporary keychain not deleted: %q: %v", events, err)
			}
			entries, err := os.ReadDir(runnerTemp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("signing temporary directory remains: %v: %v", entries, err)
			}
		})
	}
}
