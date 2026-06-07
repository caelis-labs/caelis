package main

import (
	"bufio"
	"bytes"
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
		if rule := deletedLegacyRootRule(rel); rule != "" {
			violations = append(violations, violation{
				file: rel,
				line: 1,
				rule: rule,
			})
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if rule := textRule(rel, src); rule != "" {
			violations = append(violations, violation{
				file: rel,
				line: firstLineContaining(src, []byte("StreamEvent")),
				rule: rule,
			})
		}
		file, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
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
	if pathIn(target, "ports") {
		if pathIn(rel, "ports") {
			return ""
		}
		if strings.HasPrefix(rel, "protocol/acp/") {
			return "protocol/acp must not depend on old ports"
		}
		return "active packages must not depend on old ports"
	}
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
	case strings.HasPrefix(rel, "runner/"):
		if strings.HasPrefix(target, "tool/builtin/") {
			return "runner must not depend on built-in tool implementations"
		}
		if target == "protocol/acp" || strings.HasPrefix(target, "protocol/acp/") {
			return "runner must not depend on ACP, control, or presentation packages"
		}
		if target == "app" || strings.HasPrefix(target, "app/") ||
			target == "gateway" || strings.HasPrefix(target, "gateway/") ||
			target == "tui" || strings.HasPrefix(target, "tui/") ||
			target == "headless" || strings.HasPrefix(target, "headless/") ||
			target == "impl" || strings.HasPrefix(target, "impl/") ||
			target == "surfaces" || strings.HasPrefix(target, "surfaces/") {
			return "runner must not depend on control or presentation packages"
		}
		if target == "orchestrator" || strings.HasPrefix(target, "orchestrator/") {
			return "runner must not depend on orchestrator (use injected interfaces)"
		}
	case strings.HasPrefix(rel, "orchestrator/"):
		if target == "protocol/acp" || strings.HasPrefix(target, "protocol/acp/") {
			return "orchestrator must not depend on deprecated protocol/acp (use acp/)"
		}
		if target == "app" || strings.HasPrefix(target, "app/") ||
			target == "gateway" || strings.HasPrefix(target, "gateway/") ||
			target == "tui" || strings.HasPrefix(target, "tui/") ||
			target == "headless" || strings.HasPrefix(target, "headless/") ||
			target == "cmd" || strings.HasPrefix(target, "cmd/") {
			return "orchestrator must not depend on control or presentation packages"
		}
	case strings.HasPrefix(rel, "sandbox/"):
		if startsWithAny(target, "app/", "cmd/", "protocol/", "impl/", "surfaces/") {
			return "sandbox must not depend on app, cmd, protocol, impl, or surfaces"
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

func deletedLegacyRootRule(rel string) string {
	if pathIn(rel,
		"impl",
		"surfaces",
		"tui",
		"headless",
		"eval",
		"ports",
		"cmd/caelis",
		"app/gatewayapp",
		"internal/acpe2eagent",
		"internal/bootstrap",
		"internal/cli",
		"internal/evalharness",
		"internal/kernel",
		"internal/modelcataloggen",
	) {
		return "legacy production roots must not be active in the rewrite branch"
	}
	return ""
}

func textRule(rel string, src []byte) string {
	if !isLayer4RuntimeFile(rel) {
		return ""
	}
	if bytes.Contains(src, []byte("StreamEvent")) {
		return "layer4 runtime must use model.ResponseEvent, not legacy StreamEvent"
	}
	return ""
}

func isLayer4RuntimeFile(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	return strings.HasPrefix(rel, "model/") ||
		strings.HasPrefix(rel, "agent/") ||
		strings.HasPrefix(rel, "runner/") ||
		strings.HasPrefix(rel, "session/")
}

func firstLineContaining(src []byte, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	line := 1
	for len(src) > 0 {
		next := bytes.IndexByte(src, '\n')
		var current []byte
		if next < 0 {
			current = src
			src = nil
		} else {
			current = src[:next]
			src = src[next+1:]
		}
		if bytes.Contains(current, needle) {
			return line
		}
		line++
	}
	return 0
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
