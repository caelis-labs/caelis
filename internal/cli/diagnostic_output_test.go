package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func TestDoctorStartupFailureIncludesReadOnlyStorageDiagnostics(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, "memory", "appliance")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "memoryd.lock"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := doctorResultFromStartupFailure(root, productClientModeManaged, errors.New("ignored"))
	if result.StorageDiagnostics == nil || !result.StorageDiagnostics.ReadOnly {
		t.Fatalf("startup diagnostics = %#v", result.StorageDiagnostics)
	}
	if result.StorageDiagnostics.Memory.OwnerLockFileState != "present" {
		t.Fatalf("startup Memory diagnostics = %#v", result.StorageDiagnostics.Memory)
	}
	if result.StorageDiagnostics.Memory.DatabaseState != "missing" {
		t.Fatalf("startup Memory database state = %#v", result.StorageDiagnostics.Memory)
	}
	text := formatDoctorResult(result)
	for _, want := range []string{
		"storage_diagnostics_read_only: true",
		"memory_database_state: missing",
		"memory_owner_lock_file_state: present",
		"recovery_advice:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ignored") {
		t.Fatalf("doctor startup diagnostics copied raw startup error: %s", raw)
	}
}

func TestAnnotateStartupStorageDiagnosticsOnlyMarksExplicitOwnerContention(t *testing.T) {
	for _, test := range []struct {
		name string
		err  string
	}{
		{name: "secure", err: "secure owner lock: permission denied"},
		{name: "open", err: "open owner lock: permission denied"},
		{name: "acquire", err: "acquire owner lock: input/output error"},
		{name: "truncate", err: "truncate owner lock: read-only file system"},
		{name: "write", err: "write owner lock: no space left on device"},
		{name: "sync", err: "sync owner lock: input/output error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostics := gatewayapp.StoreDiagnostics{
				Memory: gatewayapp.MemoryStorageDiagnostics{OwnerLockState: "unknown"},
			}
			annotateStartupStorageDiagnostics(&diagnostics, errors.New(test.err))
			if diagnostics.Memory.OwnerLockState != "unknown" {
				t.Fatalf("startup error %q changed owner lock state to %q; want unknown", test.err, diagnostics.Memory.OwnerLockState)
			}
		})
	}

	diagnostics := gatewayapp.StoreDiagnostics{
		Memory: gatewayapp.MemoryStorageDiagnostics{OwnerLockState: "unknown"},
	}
	annotateStartupStorageDiagnostics(&diagnostics, errors.New("open embedded Memory: memory data directory is already owned"))
	if diagnostics.Memory.OwnerLockState != "held" {
		t.Fatalf("explicit owner contention state = %q, want held", diagnostics.Memory.OwnerLockState)
	}
}

func TestDoctorRemoteStatusDoesNotInspectLocalStore(t *testing.T) {
	client := &cliStatusClientProbe{}
	client.status.Session.StoreDir = filepath.Join(t.TempDir(), "remote-store")
	client.status.Diagnostics.Warnings = []string{"host warning"}
	var out bytes.Buffer
	if err := runDoctor(context.Background(), client, "remote-session", nil, outputJSON, &out); err != nil {
		t.Fatal(err)
	}
	var result doctorResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.StorageDiagnostics != nil || len(result.Warnings) != 1 || result.Warnings[0] != "host warning" {
		t.Fatalf("remote status mixed with local diagnostics: %s", out.String())
	}
	if strings.Contains(formatDoctorResult(result), "memory_database_state:") {
		t.Fatal("remote text output contains local storage diagnosis")
	}
}

func TestDoctorDoesNotInferMemorySchemaFromStartupError(t *testing.T) {
	diagnostics := gatewayapp.StoreDiagnostics{Memory: gatewayapp.MemoryStorageDiagnostics{SchemaState: "unknown"}}
	annotateStartupStorageDiagnostics(&diagnostics, errors.New("Control: unsupported schema"))
	if diagnostics.Memory.SchemaState != "unknown" {
		t.Fatalf("unrelated schema failure changed Memory state: %#v", diagnostics.Memory)
	}
}

func TestDoctorRemoteConnectionFailureDoesNotDiagnoseLocalStore(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			storeDir := t.TempDir()
			var out bytes.Buffer
			err := runWithProductClientOpener(t.Context(), []string{
				"doctor", "--control-url", "http://127.0.0.1:7777", "--store-dir", storeDir, "--format", format,
			}, nil, &out, io.Discard, func(_ context.Context, _ gatewayapp.Config, options productClientOptions) (*productClients, error) {
				if options.Mode != productClientModeRemote {
					t.Fatalf("client mode = %v", options.Mode)
				}
				return nil, errors.New("remote connection failed: private-token")
			})
			if err != nil {
				t.Fatal(err)
			}
			output := out.String()
			for _, forbidden := range []string{storeDir, "private-token", "storage_diagnostics", "memory_database_state", "recovery_advice"} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("remote failure contains %q: %s", forbidden, output)
				}
			}
			if !strings.Contains(output, "configured remote Control Host is unavailable") {
				t.Fatalf("missing remote failure: %s", output)
			}
		})
	}
}
