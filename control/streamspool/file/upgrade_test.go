package file

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

func TestNewStoreReclaimsExhaustedLegacyCache(t *testing.T) {
	root := t.TempDir()
	spoolRoot := filepath.Join(root, "control", "spool", "v1")
	canonical := filepath.Join(root, "sessions", "history.events.jsonl")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	durable := []byte("durable session history must survive cache reclamation\n")
	if err := os.WriteFile(canonical, durable, 0o600); err != nil {
		t.Fatal(err)
	}
	const limit, partitionCharge, segmentCharge = 64 << 10, 128, 64
	// The previous append-only format has no retention metadata. Seed it
	// directly so fixture creation cannot benefit from the new eviction path.
	var oldKeys []streamspool.Key
	var accounted int64
	for _, namespace := range []streamspool.Namespace{streamspool.NamespaceSession, streamspool.NamespaceTask} {
		key := streamspool.Key{LogicalKey: streamspool.LogicalKey{Namespace: namespace, Digest: streamspool.DigestStrings("legacy", namespace.String())}, Epoch: [16]byte{1}, Incarnation: [16]byte{2}}
		payloadSize := limit/2 - partitionCharge - segmentCharge - int(segmentHeaderSize) - recordPrefixSize - recordFixedBody - recordChecksumSize
		record, err := encodeRecord(0, 1, time.Unix(1, 0), bytes.Repeat([]byte("x"), payloadSize))
		if err != nil {
			t.Fatal(err)
		}
		data := append(encodeSegmentHeader(key, true, 0, time.Unix(1, 0)), record...)
		dir := filepath.Join(spoolRoot, partitionRelativeDir(key))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, segmentFilename(0)), data, 0o600); err != nil {
			t.Fatal(err)
		}
		accounted += int64(len(data) + partitionCharge + segmentCharge)
		oldKeys = append(oldKeys, key)
	}
	if accounted != limit {
		t.Fatalf("legacy cache = %d bytes, want full budget %d", accounted, limit)
	}
	store, err := New(t.Context(), Config{RootDir: spoolRoot, GCInterval: -1, MaxBytes: limit, MaxStreamBytes: 12 << 10, SegmentBytes: 2 << 10, PartitionAllocationCharge: partitionCharge, SegmentAllocationCharge: segmentCharge})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.usedBytes != 0 {
		t.Fatalf("startup retained exhausted cache: %d", store.usedBytes)
	}
	for _, key := range oldKeys {
		if _, err := os.Stat(filepath.Join(spoolRoot, partitionRelativeDir(key))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old epoch retained: %v", err)
		}
		writer, err := store.Register(t.Context(), streamspool.LogicalKey{Namespace: key.Namespace, Digest: streamspool.DigestStrings("new-session", key.Namespace.String())}, streamspool.WriterOptions{OriginComplete: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Append(t.Context(), 1, time.Now(), []byte("approved")); err != nil {
			t.Fatal(err)
		}
		reader, err := store.Reader(t.Context(), writer.Key(), 0)
		if err != nil {
			t.Fatal(err)
		}
		record, err := reader.Next(t.Context())
		_ = reader.Close()
		if err != nil || string(record.Payload) != "approved" {
			t.Fatalf("new stream after upgrade = %#v, %v", record, err)
		}
	}
	if data, err := os.ReadFile(canonical); err != nil || !bytes.Equal(data, durable) {
		t.Fatalf("canonical history changed: %q, %v", data, err)
	}
}
