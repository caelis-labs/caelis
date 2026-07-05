package skilldiscovery

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestDefaultDiscoveryDirsPrioritizeSystemWorkspaceAndUserRoots(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")

	got := DefaultDiscoveryDirs(workspace)
	want := []string{
		"~/.caelis/skills/.system",
		filepath.Join(workspace, ".claude", "skills"),
		filepath.Join(workspace, ".opencode", "skills"),
		filepath.Join(workspace, ".opencode", "skill"),
		filepath.Join(workspace, ".agents", "skills"),
		filepath.Join(workspace, "skills"),
		"~/.caelis/skills",
		"~/.claude/skills",
		"~/.config/opencode/skills",
		"~/.config/opencode/skill",
		"~/.agents/skills",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DefaultDiscoveryDirs() = %#v, want %#v", got, want)
	}
}

func TestDiscoverMetaMaterializesSystemSkillsAndDedupesByPriority(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	workspace := filepath.Join(t.TempDir(), "workspace")

	systemRoot := filepath.Join(home, ".caelis", "skills", ".system")
	stale := filepath.Join(systemRoot, "stale-skill")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale system skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "SKILL.md"), []byte("---\nname: stale-skill\ndescription: stale\n---\n"), 0o600); err != nil {
		t.Fatalf("write stale system skill: %v", err)
	}

	writeSkillForDiscoveryTest(t, filepath.Join(home, ".agents", "skills", "skill-creator"), "skill-creator", "public skill creator")
	writeSkillForDiscoveryTest(t, filepath.Join(home, ".agents", "skills", "shared"), "shared", "public shared")
	writeSkillForDiscoveryTest(t, filepath.Join(home, ".caelis", "skills", "shared"), "shared", "private shared")
	writeSkillForDiscoveryTest(t, filepath.Join(home, ".caelis", "skills", "dupe"), "dupe", "private dupe")
	writeSkillForDiscoveryTest(t, filepath.Join(workspace, ".agents", "skills", "dupe"), "dupe", "workspace dupe")

	metas, err := DiscoverMeta(nil, workspace)
	if err != nil {
		t.Fatalf("DiscoverMeta() error = %v", err)
	}
	byName := map[string]skill.Meta{}
	for _, meta := range metas {
		if _, exists := byName[meta.Name]; exists {
			t.Fatalf("duplicate skill name %q in %#v", meta.Name, metas)
		}
		byName[meta.Name] = meta
	}
	if _, err := os.Stat(filepath.Join(systemRoot, "stale-skill")); !os.IsNotExist(err) {
		t.Fatalf("stale system skill stat err = %v, want os.IsNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(systemRoot, "skill-creator", "scripts", "init_skill.py")); err != nil {
		t.Fatalf("system skill script not materialized: %v", err)
	}
	if got := byName["skill-creator"].Path; got != filepath.Join(systemRoot, "skill-creator", "SKILL.md") {
		t.Fatalf("skill-creator path = %q, want system skill", got)
	}
	if got := byName["skill-installer"].Path; got != filepath.Join(systemRoot, "skill-installer", "SKILL.md") {
		t.Fatalf("skill-installer path = %q, want system skill", got)
	}
	if got := byName["review"].Path; got != filepath.Join(systemRoot, "review", "SKILL.md") {
		t.Fatalf("review path = %q, want system skill", got)
	}
	if got := byName["review"].Description; !strings.Contains(got, "review code changes") {
		t.Fatalf("review description = %q, want code review trigger", got)
	}
	if got := byName["subagent-creator"].Description; !strings.Contains(got, "create or edit a reusable subagent markdown profile") || strings.Contains(got, "/agents") || strings.Contains(got, ".caelis") {
		t.Fatalf("subagent-creator description = %q, want clear trigger without storage paths", got)
	}
	if got := byName["shared"].Description; got != "private shared" {
		t.Fatalf("shared description = %q, want private user skill over public .agents skill", got)
	}
	if got := byName["dupe"].Description; got != "workspace dupe" {
		t.Fatalf("dupe description = %q, want workspace skill over user skill", got)
	}
}

func TestDiscoverMetaMaterializesSystemSkillsForExplicitDefaultDirs(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	workspace := filepath.Join(t.TempDir(), "workspace")
	systemRoot := filepath.Join(home, ".caelis", "skills", ".system")

	metas, err := DiscoverMeta(DefaultDiscoveryDirs(workspace), workspace)
	if err != nil {
		t.Fatalf("DiscoverMeta(explicit default dirs) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(systemRoot, "review", "SKILL.md")); err != nil {
		t.Fatalf("review system skill was not materialized: %v", err)
	}
	if got := metaByNameForDiscoveryTest(metas, "review").Path; got != filepath.Join(systemRoot, "review", "SKILL.md") {
		t.Fatalf("review path = %q, want materialized system skill", got)
	}
}

func TestDiscoverMetaSkipsSystemRootWhenMaterializationFails(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".caelis"), 0o755); err != nil {
		t.Fatalf("mkdir .caelis: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".caelis", "skills"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write skills placeholder: %v", err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	writeSkillForDiscoveryTest(t, filepath.Join(workspace, "skills", "workspace-skill"), "workspace-skill", "workspace skill")

	metas, err := DiscoverMeta(nil, workspace)
	if err != nil {
		t.Fatalf("DiscoverMeta() error = %v, want workspace discovery despite unavailable system root", err)
	}
	if got := len(metas); got != 1 {
		t.Fatalf("len(metas) = %d, want only workspace skill: %#v", got, metas)
	}
	if metas[0].Name != "workspace-skill" {
		t.Fatalf("metas[0] = %#v, want workspace skill", metas[0])
	}
}

func TestDiscoverMetaConcurrentSystemMaterializationDoesNotExposeEmptySkills(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	workspace := filepath.Join(t.TempDir(), "workspace")
	systemRoot := filepath.Join(home, ".caelis", "skills", ".system")
	emptySkill := filepath.Join(systemRoot, "subagent-creator")
	if err := os.MkdirAll(emptySkill, 0o755); err != nil {
		t.Fatalf("mkdir empty system skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(emptySkill, "SKILL.md"), nil, 0o600); err != nil {
		t.Fatalf("write empty system skill: %v", err)
	}

	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := DiscoverMeta(nil, workspace)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("DiscoverMeta() error = %v", err)
		}
	}
}

func writeSkillForDiscoveryTest(t *testing.T, dir string, name string, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", dir, err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill %s: %v", dir, err)
	}
}

func metaByNameForDiscoveryTest(metas []skill.Meta, name string) skill.Meta {
	for _, meta := range metas {
		if meta.Name == name {
			return meta
		}
	}
	return skill.Meta{}
}
