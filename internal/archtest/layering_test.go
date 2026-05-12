package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPlannedArchitectureRootsExist(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	for _, rel := range []string{
		"kernel",
		"ports",
		"ports/approval",
		"ports/session",
		"ports/model",
		"ports/sandbox",
		"ports/policy",
		"ports/tool",
		"ports/skill",
		"ports/prompt",
		"ports/agent",
		"ports/compact",
		"ports/controller",
		"ports/delegation",
		"ports/subagent",
		"ports/task",
		"ports/stream",
		"ports/config",
		"ports/assembly",
		"impl",
		"impl/config/file",
		"impl/agent/local",
		"impl/agent/local/chat",
		"impl/agent/acp",
		"impl/agent/acp/assembly",
		"impl/agent/acp/controller",
		"impl/agent/acp/internal/acputil",
		"impl/agent/acp/loader",
		"impl/agent/acp/subagent",
		"impl/agent/acp/terminal",
		"impl/approval/manual",
		"impl/approval/agentreview",
		"impl/approval/deny",
		"impl/model/catalog",
		"impl/model/providers",
		"impl/model/providers/e2etest",
		"impl/policy/presets",
		"impl/prompt/static",
		"impl/sandbox/host",
		"impl/sandbox/bwrap",
		"impl/sandbox/internal",
		"impl/sandbox/landlock",
		"impl/sandbox/seatbelt",
		"impl/session/file",
		"impl/session/memory",
		"impl/skill/fs",
		"impl/stream/memory",
		"impl/task/file",
		"impl/tool/builtin",
		"impl/tool/builtin/filesystem",
		"impl/tool/builtin/plan",
		"impl/tool/builtin/shell",
		"impl/tool/builtin/spawn",
		"impl/tool/builtin/task",
		"kernel/host",
		"surfaces",
		"surfaces/headless",
		"surfaces/acpserver",
		"surfaces/tui",
		"surfaces/tui/acpprojector",
		"surfaces/tui/app",
		"surfaces/tui/driver",
		"surfaces/tui/gatewaydriver",
		"surfaces/tui/tuikit",
		"surfaces/tui/tuidiff",
		"protocol/acp",
		"protocol/acp/fixture",
		"protocol/acp/projector",
		"protocol/acp/schema",
		"protocol/acp/jsonrpc",
		"protocol/acp/client",
		"protocol/acp/server",
		"protocol/acp/terminal",
		"protocol/acp/transport/stdio",
		"internal/acpe2eagent",
		"internal/kernel",
	} {
		info, err := os.Stat(filepath.Join(repo, rel))
		if err != nil {
			t.Fatalf("missing planned architecture root %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("planned architecture root %s is not a directory", rel)
		}
	}
}

func TestPortPackageNamesMatchArchitectureVocabulary(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	for _, item := range []struct {
		dir  string
		want string
	}{
		{dir: "ports/agent", want: "agent"},
	} {
		item := item
		t.Run(item.dir, func(t *testing.T) {
			t.Parallel()
			checkPackageName(t, filepath.Join(repo, filepath.FromSlash(item.dir)), item.want)
		})
	}
}

func TestGatewayAppInternalPackagesUseDomainNames(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	root := filepath.Join(repo, "app", "gatewayapp", "internal")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if shouldSkipDir(name) {
			return filepath.SkipDir
		}
		if strings.Contains(name, "wire") {
			t.Fatalf("%s uses a wiring-oriented package name; use a domain role name instead", packageRel(t, repo, path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", root, err)
	}
}

func TestBoundaryImportsUseCurrentVocabulary(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		for _, item := range importSpecsForFile(t, path) {
			name := strings.TrimSpace(item.name)
			if name == "" || name == "_" || name == "." {
				continue
			}
			switch {
			case strings.HasPrefix(name, "sdk"):
				t.Fatalf("%s imports %q as %q; use current port or implementation vocabulary", packageRel(t, repo, path), item.path, name)
			case name == "gateway" && item.path == modulePath+"/kernel":
				t.Fatalf("%s imports kernel as %q; use kernel vocabulary", packageRel(t, repo, path), name)
			case name == "appgateway":
				t.Fatalf("%s imports %q as %q; use kernel vocabulary", packageRel(t, repo, path), item.path, name)
			case name == "implacp":
				t.Fatalf("%s imports %q as %q; use ACP agent vocabulary", packageRel(t, repo, path), item.path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", repo, err)
	}
}

func TestInternalImportsAvoidAliasesByDefault(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	packageNames := map[string]string{}
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		imports := importSpecsForFile(t, path)
		nameCounts := map[string]int{}
		for _, item := range imports {
			if !strings.HasPrefix(item.path, modulePath+"/") {
				continue
			}
			packageName := packageNames[item.path]
			if packageName == "" {
				packageName = packageNameForImport(t, repo, item.path)
				packageNames[item.path] = packageName
			}
			nameCounts[packageName]++
		}

		for _, item := range imports {
			alias := strings.TrimSpace(item.name)
			if alias == "" || alias == "_" || alias == "." || !strings.HasPrefix(item.path, modulePath+"/") {
				continue
			}
			packageName := packageNames[item.path]
			if alias == packageName {
				t.Fatalf("%s imports %q with redundant alias %q", packageRel(t, repo, path), item.path, alias)
			}
			if nameCounts[packageName] < 2 {
				t.Fatalf("%s imports %q as %q; omit the alias unless this file imports another %q package", packageRel(t, repo, path), item.path, alias, packageName)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", repo, err)
	}
}

func TestPlannedLayerImportRules(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	rules := []layerRule{
		{
			dir: "kernel",
			forbidden: []string{
				modulePath + "/app/",
				modulePath + "/internal/kernel",
				modulePath + "/impl/",
				modulePath + "/surfaces/",
			},
		},
		{
			dir: "ports",
			forbidden: []string{
				modulePath + "/app/",
				modulePath + "/impl/",
				modulePath + "/internal/",
				modulePath + "/surfaces/",
			},
		},
		{
			dir: "impl",
			forbidden: []string{
				modulePath + "/app/",
				modulePath + "/internal/kernel",
				modulePath + "/surfaces/",
			},
		},
		{
			dir: "surfaces",
			forbidden: []string{
				modulePath + "/impl/",
				modulePath + "/internal/kernel",
			},
		},
		{
			dir: "internal/kernel",
			forbidden: []string{
				modulePath + "/app/",
				modulePath + "/impl/",
				modulePath + "/surfaces/",
			},
		},
		{
			dir: "protocol/acp",
			forbidden: []string{
				modulePath + "/app/",
				modulePath + "/impl/",
				modulePath + "/internal/",
				modulePath + "/kernel",
				modulePath + "/ports/",
				modulePath + "/surfaces/",
			},
		},
	}

	for _, rule := range rules {
		rule := rule
		t.Run(rule.dir, func(t *testing.T) {
			t.Parallel()
			checkLayerRule(t, repo, rule)
		})
	}
}

func TestGatewayAppIsOnlyProductionPackageImportingNewWiringLayers(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		pkgRel := packageRel(t, repo, filepath.Dir(path))
		for _, importPath := range importsForFile(t, path) {
			if strings.HasPrefix(importPath, modulePath+"/impl/") && !isImplImportAllowed(pkgRel, importPath) {
				t.Fatalf("%s imports wiring-only package %q; keep impl/internal kernel construction behind app/gatewayapp", packageRel(t, repo, path), importPath)
			}
			if isInternalKernelImport(importPath) && !isInternalKernelAllowedImporter(pkgRel) {
				t.Fatalf("%s imports internal kernel package %q; keep implementation ownership behind kernel/gateway compatibility or app composition", packageRel(t, repo, path), importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", repo, err)
	}
}

func TestProtocolACPOwnedSubpackagesDoNotImportLegacyACP(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	checkNoLegacyACPImportsInPackage(t, repo, "protocol/acp")
	for _, dir := range []string{
		"protocol/acp/schema",
		"protocol/acp/jsonrpc",
		"protocol/acp/transport/stdio",
		"protocol/acp/client",
		"protocol/acp/server",
		"protocol/acp/terminal",
		"protocol/acp/projector",
	} {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			checkNoLegacyACPImports(t, repo, dir)
		})
	}
}

func TestProductionCodeOutsideCompatibilityACPUsesProtocolACPImports(t *testing.T) {
	t.Parallel()

	repo := repoRoot(t)
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		pkgRel := packageRel(t, repo, filepath.Dir(path))
		if pkgRel == "acp" || strings.HasPrefix(pkgRel, "acp/") {
			return nil
		}
		for _, importPath := range importsForFile(t, path) {
			if isLegacyRootACPImport(importPath) {
				t.Fatalf("%s imports legacy ACP package %q; use protocol/acp outside compatibility packages", packageRel(t, repo, path), importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", repo, err)
	}
}

const modulePath = "github.com/OnslaughtSnail/caelis"

type layerRule struct {
	dir       string
	forbidden []string
}

func checkPackageName(t *testing.T, dir string, want string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", filepath.Join(dir, entry.Name()), err)
		}
		if file.Name == nil {
			t.Fatalf("%s has no package clause", filepath.Join(dir, entry.Name()))
		}
		if got := strings.TrimSuffix(file.Name.Name, "_test"); got != want {
			t.Fatalf("%s package = %q, want %q", filepath.Join(dir, entry.Name()), file.Name.Name, want)
		}
	}
}

func checkLayerRule(t *testing.T, repo string, rule layerRule) {
	t.Helper()

	root := filepath.Join(repo, filepath.FromSlash(rule.dir))
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("missing layer root %s: %v", rule.dir, err)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		for _, importPath := range importsForFile(t, path) {
			for _, forbidden := range rule.forbidden {
				if importPath == strings.TrimSuffix(forbidden, "/") || strings.HasPrefix(importPath, forbidden) {
					if isLayerImportAllowed(rule.dir, packageRel(t, repo, filepath.Dir(path)), importPath) {
						continue
					}
					t.Fatalf("%s imports %q, forbidden for layer %s", packageRel(t, repo, path), importPath, rule.dir)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", root, err)
	}
}

func checkNoLegacyACPImportsInPackage(t *testing.T, repo string, dir string) {
	t.Helper()

	root := filepath.Join(repo, filepath.FromSlash(dir))
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", root, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		for _, importPath := range importsForFile(t, path) {
			if isLegacyACPImport(importPath) {
				t.Fatalf("%s imports legacy ACP package %q; move protocol ownership under protocol/acp", packageRel(t, repo, path), importPath)
			}
		}
	}
}

func checkNoLegacyACPImports(t *testing.T, repo string, dir string) {
	t.Helper()

	root := filepath.Join(repo, filepath.FromSlash(dir))
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("missing protocol ACP root %s: %v", dir, err)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		for _, importPath := range importsForFile(t, path) {
			if isLegacyACPImport(importPath) {
				t.Fatalf("%s imports legacy ACP package %q; move protocol ownership under protocol/acp", packageRel(t, repo, path), importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", root, err)
	}
}

func isLegacyACPImport(importPath string) bool {
	return importPath == modulePath+"/acp" ||
		strings.HasPrefix(importPath, modulePath+"/acp/")
}

func isLegacyRootACPImport(importPath string) bool {
	return importPath == modulePath+"/acp" ||
		strings.HasPrefix(importPath, modulePath+"/acp/")
}

func isGatewayAppPackage(pkgRel string) bool {
	return pkgRel == "app/gatewayapp" || strings.HasPrefix(pkgRel, "app/gatewayapp/internal/")
}

func isImplImportAllowed(pkgRel string, importPath string) bool {
	if isGatewayAppPackage(pkgRel) || strings.HasPrefix(pkgRel, "impl/") ||
		pkgRel == "internal/acpe2eagent" ||
		pkgRel == "internal/bootstrap" ||
		pkgRel == "internal/cli" {
		return true
	}
	return isTUIGatewayDriverModelWiring(pkgRel, importPath)
}

func isLayerImportAllowed(layer string, pkgRel string, importPath string) bool {
	switch layer {
	case "kernel":
		return pkgRel == "kernel" && importPath == modulePath+"/internal/kernel"
	case "protocol/acp":
		return importPath == modulePath+"/ports/session" ||
			importPath == modulePath+"/ports/model" ||
			importPath == modulePath+"/internal/displaypolicy"
	case "surfaces":
		return isTUIGatewayDriverModelWiring(pkgRel, importPath)
	default:
		return false
	}
}

func isTUIGatewayDriverModelWiring(pkgRel string, importPath string) bool {
	if pkgRel != "surfaces/tui/gatewaydriver" && !strings.HasPrefix(pkgRel, "surfaces/tui/gatewaydriver/") {
		return false
	}
	switch importPath {
	case modulePath + "/impl/model/catalog",
		modulePath + "/impl/model/providers",
		modulePath + "/impl/skill/fs":
		return true
	default:
		return false
	}
}

func isInternalKernelAllowedImporter(pkgRel string) bool {
	return pkgRel == "kernel" || isGatewayAppPackage(pkgRel)
}

func isInternalKernelImport(importPath string) bool {
	return importPath == modulePath+"/internal/kernel" ||
		strings.HasPrefix(importPath, modulePath+"/internal/kernel/")
}

func importsForFile(t *testing.T, path string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(%s) error = %v", path, err)
	}
	out := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		out = append(out, importPath)
	}
	return out
}

type importSpec struct {
	name string
	path string
}

func importSpecsForFile(t *testing.T, path string) []importSpec {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(%s) error = %v", path, err)
	}
	out := make([]importSpec, 0, len(file.Imports))
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		}
		out = append(out, importSpec{name: name, path: importPath})
	}
	return out
}

func packageNameForImport(t *testing.T, repo string, importPath string) string {
	t.Helper()

	rel := strings.TrimPrefix(importPath, modulePath+"/")
	dir := filepath.Join(repo, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", filepath.Join(dir, name), err)
		}
		return file.Name.Name
	}
	t.Fatalf("no non-test Go files found for import %q", importPath)
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()

	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Abs(repo) error = %v", err)
	}
	return repo
}

func packageRel(t *testing.T, repo string, path string) string {
	t.Helper()

	rel, err := filepath.Rel(repo, path)
	if err != nil {
		t.Fatalf("Rel(%s, %s) error = %v", repo, path, err)
	}
	return filepath.ToSlash(rel)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".codex", ".tmp", "node_modules", "dist", "tmp", "vendor":
		return true
	default:
		return false
	}
}
