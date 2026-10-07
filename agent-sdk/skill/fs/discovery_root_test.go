package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverRootMetaSelectsOneSkillWithoutBody(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "selected")
	ambient := filepath.Join(parent, "ambient")
	for _, root := range []string{selected, ambient} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(selected, "SKILL.md"), []byte("---\nname: selected\ndescription: Selected description.\n---\nBODY_SENTINEL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "SKILL.md"), []byte("---\nname: ambient\ndescription: Ambient description.\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := DiscoverRootMeta(selected)
	if err != nil || meta.Name != "selected" || meta.Description != "Selected description." || meta.Path != filepath.Join(selected, "SKILL.md") || strings.Contains(meta.Description, "BODY_SENTINEL") {
		t.Fatalf("root metadata = %+v, %v", meta, err)
	}
}
