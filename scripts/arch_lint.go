package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type violation struct {
	file       string
	line       int
	importPath string
	rule       string
}

func main() {
	rootFlag := flag.String("root", ".", "repository root")
	includeTests := flag.Bool("include-tests", false, "include _test.go files")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fatal(err)
	}
	modulePath, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		fatal(err)
	}
	var violations []violation
	filesChecked := 0
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".tmp", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if !*includeTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		filesChecked++
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if rule := boundaryRule(rel, importPath, modulePath); rule != "" {
				violations = append(violations, violation{
					file:       rel,
					line:       fset.Position(spec.Pos()).Line,
					importPath: importPath,
					rule:       rule,
				})
			}
		}
		return nil
	})
	if err != nil {
		fatal(err)
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "%s:%d: %s imports %q\n", v.file, v.line, v.rule, v.importPath)
		}
		os.Exit(1)
	}
	fmt.Printf("architecture lint passed (%d files checked)\n", filesChecked)
}

func readModulePath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("module path not found in %s", path)
}

func boundaryRule(rel string, importPath string, modulePath string) string {
	if !strings.HasPrefix(importPath, modulePath+"/") {
		return ""
	}
	target := strings.TrimPrefix(importPath, modulePath+"/")
	switch {
	case strings.HasPrefix(rel, "kernel/"):
		if target == "internal/kernel" || strings.HasPrefix(target, "internal/kernel/") {
			return "kernel must not depend on internal/kernel"
		}
		if startsWithAny(target, "impl/", "surfaces/") {
			return "kernel must not depend on impl or surfaces"
		}
	case strings.HasPrefix(rel, "ports/"):
		if strings.HasPrefix(target, "internal/") {
			return "ports must not depend on internal packages"
		}
		if startsWithAny(target, "impl/", "surfaces/") {
			return "ports must not depend on impl or surfaces"
		}
	case strings.HasPrefix(rel, "impl/"):
		if strings.HasPrefix(target, "surfaces/") {
			return "impl must not depend on surfaces"
		}
	case strings.HasPrefix(rel, "surfaces/"):
		if strings.HasPrefix(target, "impl/") {
			return "surfaces must not depend directly on impl"
		}
	case strings.HasPrefix(rel, "cmd/caelis/"):
		if pathIn(target, "internal/cli", "internal/bootstrap") {
			return ""
		}
		if strings.HasPrefix(target, "internal/") || startsWithAny(target, "app/", "kernel/", "ports/", "impl/", "surfaces/", "protocol/") {
			return "cmd/caelis should only enter internal/cli and startup bootstrap"
		}
	}
	return ""
}

func pathIn(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func startsWithAny(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
