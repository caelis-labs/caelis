package controlserver

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadOrCreateBearerTokenPersistsStrict0600File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "control.token")
	first, err := LoadOrCreateBearerToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateBearerToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second != first {
		t.Fatalf("tokens = %q and %q", first, second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBearerTokenFile(path); err != nil {
		t.Fatalf("token file security = %v (mode %04o)", err, info.Mode().Perm())
	}
}

func TestLoadOrCreateBearerTokenCoordinatesConcurrentCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	const workers = 32
	tokens := make(chan string, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			token, err := LoadOrCreateBearerToken(path)
			if err != nil {
				errors <- err
				return
			}
			tokens <- token
		}()
	}
	group.Wait()
	close(tokens)
	close(errors)
	for err := range errors {
		t.Fatalf("LoadOrCreateBearerToken() error = %v", err)
	}
	var want string
	for token := range tokens {
		if want == "" {
			want = token
		}
		if token != want {
			t.Fatalf("concurrent token = %q, want %q", token, want)
		}
	}
	if want == "" {
		t.Fatal("no token returned")
	}
}

func TestLoadOrCreateBearerTokenIgnoresCrashedCreatorResidue(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.token")
	for name, contents := range map[string]string{
		path + ".lock": "stale lock",
		filepath.Join(directory, ".control.token.tmp-orphan"): "partial secret",
	} {
		if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if token, err := LoadOrCreateBearerToken(path); err != nil || token == "" {
		t.Fatalf("LoadOrCreateBearerToken() = %q, %v", token, err)
	}
}

func TestLoadOrCreateBearerTokenFailsClosedForInsecureOrCorruptFiles(t *testing.T) {
	valid := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"
	for name, setup := range map[string]func(*testing.T, string){
		"world readable": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(valid+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"corrupt": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not-a-valid-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"partial": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.WriteFile(target, []byte(valid+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.token")
			setup(t, path)
			if _, err := LoadOrCreateBearerToken(path); err == nil {
				t.Fatal("insecure or corrupt token file accepted")
			}
		})
	}
}
