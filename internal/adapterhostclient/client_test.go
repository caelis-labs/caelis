package adapterhostclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChannelGrantFileIsPrivateAndConsumedOnce(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteChannelGrantFile(dir, ChannelGrantFile{
		Endpoint: "http://127.0.0.1:7777", AdapterID: "Codex", Token: "one-use",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("grant mode = %o, want 600", info.Mode().Perm())
	}
	grant, err := ConsumeChannelGrantFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if grant.SchemaVersion != channelGrantFileSchemaVersion || grant.AdapterID != "codex" || grant.Token != "one-use" {
		t.Fatalf("consumed grant = %#v", grant)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("grant file remains after consume: %v", err)
	}
	if _, err := ConsumeChannelGrantFile(path); !os.IsNotExist(err) {
		t.Fatalf("second consume error = %v, want not-exist", err)
	}
}

func TestConsumeChannelGrantFileRejectsNonRegularPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := ConsumeChannelGrantFile(filepath.Join(dir, ".")); err == nil {
		t.Fatal("ConsumeChannelGrantFile() accepted a directory")
	}
}
