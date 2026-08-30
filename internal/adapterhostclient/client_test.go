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
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := validateChannelGrantFileSecurity(file, info); err != nil {
		_ = file.Close()
		t.Fatalf("grant file security: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
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
