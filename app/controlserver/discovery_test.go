package controlserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestDiscoveryRecordRoundTripAndInstanceOwnedRemoval(t *testing.T) {
	path := DefaultDiscoveryFile(t.TempDir())
	record := testDiscoveryRecord()
	if err := PublishDiscoveryRecord(path, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDiscoveryRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InstanceID != record.InstanceID || loaded.Endpoint != record.Endpoint || loaded.PID != record.PID {
		t.Fatalf("loaded discovery = %#v", loaded)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("discovery mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	if err := RemoveDiscoveryRecord(path, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("foreign instance removed discovery: %v", err)
	}
	if err := RemoveDiscoveryRecord(path, record.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned discovery remains: %v", err)
	}
}

func TestLoadDiscoveryRecordFailsClosedForMalformedOrLinkedFile(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		path := DefaultDiscoveryFile(t.TempDir())
		if err := PublishDiscoveryRecord(path, testDiscoveryRecord()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"schema_version":"caelis.control.host-discovery/v1","unknown":true}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDiscoveryRecord(path); err == nil {
			t.Fatal("discovery with unknown fields was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target")
		if err := PublishDiscoveryRecord(target, testDiscoveryRecord()); err != nil {
			t.Fatal(err)
		}
		path := DefaultDiscoveryFile(directory)
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := LoadDiscoveryRecord(path); err == nil {
			t.Fatal("linked discovery file was accepted")
		}
	})
}

func TestPublishDiscoveryRecordRejectsNonLoopbackOrCredentialEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://example.com:7777",
		"http://localhost:7777",
		"http://127.0.0.1",
		"http://127.0.0.1:0",
		"http://user@127.0.0.1:7777",
		"file:///tmp/control.sock",
	} {
		record := testDiscoveryRecord()
		record.Endpoint = endpoint
		if err := PublishDiscoveryRecord(DefaultDiscoveryFile(t.TempDir()), record); err == nil {
			t.Fatalf("endpoint %q was accepted", endpoint)
		}
	}
	record := testDiscoveryRecord()
	record.Transports = []string{"https"}
	if err := PublishDiscoveryRecord(DefaultDiscoveryFile(t.TempDir()), record); err == nil {
		t.Fatal("endpoint with mismatched transport was accepted")
	}
}

func testDiscoveryRecord() DiscoveryRecord {
	return DiscoveryRecord{
		SchemaVersion: DiscoverySchemaVersion,
		ServerID:      appserver.ServerIdentity, InstanceID: uuid.NewString(),
		AppName: "caelis", PrincipalID: "local-user", PID: os.Getpid(),
		Endpoint: "http://127.0.0.1:7777", ProtocolVersion: 1,
		EnvelopeVersion: appserver.EnvelopeVersion, APIVersion: appserver.HTTPAPIVersion,
		DistributionVersion: "v1.2.3", BuildID: "test-build", BuildKind: "release",
		Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
		StartedAt: time.Now().UTC(),
	}
}
