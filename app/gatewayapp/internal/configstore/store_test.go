package configstore

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreSetPathConcurrentWithLoadSave(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := New(root)
	if store == nil {
		t.Fatal("New() = nil")
	}
	paths := []string{
		filepath.Join(root, "one", "config.json"),
		filepath.Join(root, "two", "config.json"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var setters sync.WaitGroup
	for i := 0; i < 4; i++ {
		setters.Add(1)
		go func(offset int) {
			defer setters.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					store.SetPath(paths[offset%len(paths)])
					_ = store.Path()
				}
			}
		}(i)
	}
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				if _, err := store.Load(); err != nil {
					t.Errorf("Load() error = %v", err)
					cancel()
					return
				}
				if err := store.Save(AppConfig{}); err != nil {
					t.Errorf("Save() error = %v", err)
					cancel()
					return
				}
			}
		}()
	}
	workers.Wait()
	cancel()
	setters.Wait()
}
