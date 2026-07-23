package controlclient

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteOperationStoreJSONDurabilityBoundaries(t *testing.T) {
	t.Run("temporary file sync failure leaves destination unchanged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "operation.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		fault := errors.New("sync temporary operation")
		directorySyncCalled := false
		err := writeOperationStoreJSON(path, map[string]string{"value": "new"}, operationStoreDurability{
			syncFile: func(*os.File) error { return fault },
			syncDirectory: func(string) error {
				directorySyncCalled = true
				return nil
			},
		})
		if !errors.Is(err, fault) {
			t.Fatalf("writeOperationStoreJSON() error = %v, want %v", err, fault)
		}
		if directorySyncCalled {
			t.Fatal("directory sync called before destination replacement")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != "old" {
			t.Fatalf("destination = %q, want unchanged old value", data)
		}
	})

	t.Run("directory sync failure follows destination replacement", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "operation.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		fault := errors.New("sync operation directory")
		err := writeOperationStoreJSON(path, map[string]string{"value": "new"}, operationStoreDurability{
			syncFile:      func(*os.File) error { return nil },
			syncDirectory: func(string) error { return fault },
		})
		if !errors.Is(err, fault) {
			t.Fatalf("writeOperationStoreJSON() error = %v, want %v", err, fault)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var got map[string]string
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode destination: %v", err)
		}
		if len(got) != 1 || got["value"] != "new" {
			t.Fatalf("destination = %#v, want new value", got)
		}
	})
}
