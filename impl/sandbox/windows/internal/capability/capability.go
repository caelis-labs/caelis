package capability

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
)

type Store struct {
	WorkspaceByCWD     map[string]string `json:"workspace_by_cwd,omitempty"`
	WritableRootByPath map[string]string `json:"writable_root_by_path,omitempty"`
}

type Binding struct {
	AllSIDs     []string
	WriteRootTo map[string]string
}

func BindWriteRoots(storePath string, cwd string, writeRoots []string) (Binding, error) {
	writeRoots = pathutil.Dedupe(writeRoots)
	if len(writeRoots) == 0 {
		return Binding{}, nil
	}
	store, err := readStore(storePath)
	if err != nil {
		return Binding{}, err
	}
	if store.WorkspaceByCWD == nil {
		store.WorkspaceByCWD = map[string]string{}
	}
	if store.WritableRootByPath == nil {
		store.WritableRootByPath = map[string]string{}
	}
	cwdKey := pathutil.Key(cwd)
	binding := Binding{WriteRootTo: map[string]string{}}
	seen := map[string]struct{}{}
	changed := false
	for _, root := range writeRoots {
		key := pathutil.Key(root)
		if key == "" {
			continue
		}
		table := store.WritableRootByPath
		if cwdKey != "" && key == cwdKey {
			table = store.WorkspaceByCWD
		}
		sid := strings.TrimSpace(table[key])
		if sid == "" {
			sid, err = randomSID()
			if err != nil {
				return Binding{}, err
			}
			table[key] = sid
			changed = true
		}
		binding.WriteRootTo[pathutil.Normalize(root)] = sid
		if _, ok := seen[sid]; !ok {
			seen[sid] = struct{}{}
			binding.AllSIDs = append(binding.AllSIDs, sid)
		}
	}
	if changed {
		if err := writeStore(storePath, store); err != nil {
			return Binding{}, err
		}
	}
	return binding, nil
}

func readStore(path string) (Store, error) {
	if strings.TrimSpace(path) == "" {
		return Store{}, fmt.Errorf("capability: store path is required")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Store{}, nil
	}
	if err != nil {
		return Store{}, err
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return Store{}, fmt.Errorf("capability: decode store: %w", err)
	}
	return store, nil
}

func writeStore(path string, store Store) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func randomSID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	a := binary.LittleEndian.Uint32(raw[0:4])
	b := binary.LittleEndian.Uint32(raw[4:8])
	c := binary.LittleEndian.Uint32(raw[8:12])
	d := binary.LittleEndian.Uint32(raw[12:16])
	return fmt.Sprintf("S-1-5-21-%d-%d-%d-%d", a, b, c, d), nil
}
