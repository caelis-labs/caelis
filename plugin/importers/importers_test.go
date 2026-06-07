package importers_test

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/plugin/importers"
)

func TestCodexManifestNormalizesSuperpowersSkills(t *testing.T) {
	raw := []byte(`{
		"name": "superpowers",
		"version": "5.1.0",
		"description": "Planning, TDD, debugging, and delivery workflows for coding agents",
		"repository": "https://github.com/obra/superpowers",
		"license": "MIT",
		"keywords": ["skills", "tdd"],
		"skills": "./skills/",
		"interface": {
			"displayName": "Superpowers",
			"category": "Coding",
			"capabilities": ["Interactive", "Read", "Write"]
		}
	}`)

	manifest, err := importers.Codex(raw)
	if err != nil {
		t.Fatalf("Codex() error = %v", err)
	}
	if manifest.Name != "superpowers" || manifest.Version != "5.1.0" {
		t.Fatalf("manifest identity = %s/%s, want superpowers/5.1.0", manifest.Name, manifest.Version)
	}
	if got, want := len(manifest.Contributions.Skills), 1; got != want {
		t.Fatalf("skills contributions = %d, want %d", got, want)
	}
	skills := manifest.Contributions.Skills[0]
	if skills.Plugin != "superpowers" || skills.Namespace != "superpowers" || skills.Root != "./skills/" {
		t.Fatalf("skills contribution = %#v, want plugin namespace and root", skills)
	}
	if manifest.Interface.DisplayName != "Superpowers" {
		t.Fatalf("display name = %q, want Superpowers", manifest.Interface.DisplayName)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal normalized manifest: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("normalized manifest marshaled to empty JSON")
	}
}
