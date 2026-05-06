package caelis_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestProductionImportBoundaries(t *testing.T) {
	t.Parallel()

	modulePath := "github.com/OnslaughtSnail/caelis"
	root := "."
	knownDebt := map[string]string{
		"acp/agent.go -> github.com/OnslaughtSnail/caelis/sdk/session": "base acp optional providers still expose SDK session values",
	}

	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".idea", ".next", "dist", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == "" || !strings.HasPrefix(importPath, modulePath) {
				continue
			}
			if !forbiddenProductionImport(rel, importPath) {
				continue
			}
			key := rel + " -> " + importPath
			if _, ok := knownDebt[key]; ok {
				continue
			}
			violations = append(violations, key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func forbiddenProductionImport(rel string, importPath string) bool {
	switch {
	case strings.HasPrefix(rel, "gateway/") && strings.Contains(importPath, "/app/"):
		return true
	case strings.HasPrefix(rel, "gateway/") && strings.Contains(importPath, "/headless"):
		return true
	case strings.HasPrefix(rel, "gateway/") && strings.Contains(importPath, "/tui/"):
		return true
	case strings.HasPrefix(rel, "gateway/") && strings.Contains(importPath, "/acpbridge/"):
		return true
	case strings.HasPrefix(rel, "app/gatewayapp/") && strings.Contains(importPath, "/tui/"):
		return true
	case strings.HasPrefix(rel, "app/gatewayapp/") && strings.Contains(importPath, "/headless"):
		return true
	case strings.HasPrefix(rel, "app/gatewayapp/") && strings.Contains(importPath, "/acpbridge/"):
		return true
	case strings.HasPrefix(rel, "tui/tuiapp/") && strings.Contains(importPath, "/tui/gatewaydriver"):
		return true
	case strings.HasPrefix(rel, "acp/") && strings.Contains(importPath, "/sdk/"):
		return true
	default:
		return false
	}
}
