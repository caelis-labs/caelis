package file

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkEventIndexAnchorValidation(b *testing.B) {
	for _, size := range []int{256, 2 << 10, 2 << 20} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			raw := strings.Repeat("a", size-1) + "\n"
			path := filepath.Join(b.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				b.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				b.Fatal(err)
			}
			defer file.Close()
			checkpoint := eventPageCheckpoint{Seq: 1, Offset: int64(size), LineNo: 1, AnchorHash: sha256.Sum256([]byte(raw))}
			ctx := b.Context()
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				valid, err := validateEventPageCheckpoint(ctx, file, int64(size), checkpoint)
				if err != nil || !valid {
					b.Fatalf("checkpoint valid = %v, error = %v", valid, err)
				}
			}
		})
	}
}

func BenchmarkSQLiteIndexOpen(b *testing.B) {
	for _, index := range []struct {
		name string
		open func(*Store) (*sql.DB, error)
	}{
		{"session", (*Store).openSessionIndex},
		{"task", (*Store).openTaskIndex},
	} {
		b.Run(index.name, func(b *testing.B) {
			for _, fresh := range []bool{false, true} {
				b.Run(fmt.Sprintf("fresh=%v", fresh), func(b *testing.B) {
					store := NewStore(Config{RootDir: b.TempDir()})
					openAndClose := func() {
						db, err := index.open(store)
						if err != nil {
							b.Fatal(err)
						}
						if err := db.Close(); err != nil {
							b.Fatal(err)
						}
					}
					openAndClose()
					b.ReportAllocs()
					for b.Loop() {
						if fresh {
							b.StopTimer()
							if err := os.Remove(store.sessionIndexPath()); err != nil {
								b.Fatal(err)
							}
							b.StartTimer()
						}
						openAndClose()
					}
				})
			}
		})
	}
}
