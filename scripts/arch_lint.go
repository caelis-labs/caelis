package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type violation struct {
	file    string
	line    int
	subject string
	rule    string
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
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		filesChecked++
		if rule, subject, line := semanticBoundaryRule(rel, file, fset, modulePath); rule != "" {
			violations = append(violations, violation{
				file:    rel,
				line:    line,
				subject: subject,
				rule:    rule,
			})
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if rule := boundaryRule(rel, importPath, modulePath); rule != "" {
				violations = append(violations, violation{
					file:    rel,
					line:    fset.Position(spec.Pos()).Line,
					subject: importPath,
					rule:    rule,
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
			fmt.Fprintf(os.Stderr, "%s:%d: %s: %s\n", v.file, v.line, v.rule, v.subject)
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

func semanticBoundaryRule(rel string, file *ast.File, fset *token.FileSet, modulePath string) (string, string, int) {
	if rule, subject, line := surfaceGatewayConsumptionRule(rel, file, fset, modulePath); rule != "" {
		return rule, subject, line
	}
	if rule, subject, line := eventProtocolAliasRule(rel, file, fset, modulePath); rule != "" {
		return rule, subject, line
	}
	if rule, subject, line := topLevelTerminalMetaRule(rel, file, fset); rule != "" {
		return rule, subject, line
	}
	return "", "", 0
}

func surfaceGatewayConsumptionRule(rel string, file *ast.File, fset *token.FileSet, modulePath string) (string, string, int) {
	if !strings.HasPrefix(rel, "surfaces/") || strings.HasSuffix(rel, "_test.go") || file == nil {
		return "", "", 0
	}
	gatewayNames := importNames(file, modulePath+"/ports/gateway")
	if len(gatewayNames) == 0 {
		return "", "", 0
	}
	gatewayTurnHandles := gatewayTurnHandleNames(file, gatewayNames)
	var subject string
	var rule string
	var line int
	ast.Inspect(file, func(node ast.Node) bool {
		if subject != "" {
			return false
		}
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if gatewayNames[ident.Name] {
			switch selector.Sel.Name {
			case "Event":
				subject = ident.Name + "." + selector.Sel.Name
				rule = "surfaces must consume eventstream.Envelope instead of gateway.Event"
				line = fset.Position(selector.Pos()).Line
				return false
			case "AssistantText", "PromptTokens", "CompletionTokens", "ReasoningTokens", "TotalTokens":
				subject = ident.Name + "." + selector.Sel.Name
				rule = "surfaces must parse eventstream.Envelope instead of gateway payload helpers"
				line = fset.Position(selector.Pos()).Line
				return false
			}
		}
		if selector.Sel.Name == "Events" && gatewayTurnHandles[ident.Name] {
			subject = ident.Name + ".Events()"
			rule = "surfaces must consume ACPEventsFromGatewayHandle/eventstream.Envelope instead of gateway.TurnHandle.Events"
			line = fset.Position(selector.Pos()).Line
			return false
		}
		return true
	})
	if subject != "" {
		return rule, subject, line
	}
	return "", "", 0
}

func eventProtocolAliasRule(rel string, file *ast.File, fset *token.FileSet, modulePath string) (string, string, int) {
	if file == nil || strings.HasPrefix(rel, "ports/session/") || strings.HasSuffix(rel, "_test.go") {
		return "", "", 0
	}
	sessionNames := importNames(file, modulePath+"/ports/session")
	if len(sessionNames) == 0 {
		return "", "", 0
	}
	aliasVars := eventProtocolAliasVars(file, sessionNames)
	aliasFields := map[string]bool{
		"UpdateType":  true,
		"ToolCall":    true,
		"Plan":        true,
		"Approval":    true,
		"Participant": true,
		"Handoff":     true,
	}
	var subject string
	var line int
	ast.Inspect(file, func(node ast.Node) bool {
		if subject != "" {
			return false
		}
		if literal, ok := node.(*ast.CompositeLit); ok && isSessionEventProtocolType(literal.Type, sessionNames) {
			for _, item := range literal.Elts {
				kv, ok := item.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Handoff" {
					continue
				}
				subject = "EventProtocol." + key.Name
				line = fset.Position(key.Pos()).Line
				return false
			}
		}
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !aliasFields[selector.Sel.Name] {
			return true
		}
		switch receiver := selector.X.(type) {
		case *ast.Ident:
			if aliasVars[receiver.Name] {
				subject = receiver.Name + "." + selector.Sel.Name
				line = fset.Position(selector.Pos()).Line
				return false
			}
		case *ast.SelectorExpr:
			if receiver.Sel.Name == "Protocol" {
				subject = "EventProtocol." + selector.Sel.Name
				line = fset.Position(selector.Pos()).Line
				return false
			}
		}
		return true
	})
	if subject == "" {
		return "", "", 0
	}
	return "production code must use ports/session protocol helpers instead of EventProtocol json:\"-\" aliases", subject, line
}

func topLevelTerminalMetaRule(rel string, file *ast.File, fset *token.FileSet) (string, string, int) {
	if file == nil || strings.HasSuffix(rel, "_test.go") || rel == "scripts/arch_lint.go" || rel == "protocol/acp/metautil/terminal.go" {
		return "", "", 0
	}
	var subject string
	var line int
	ast.Inspect(file, func(node ast.Node) bool {
		if subject != "" {
			return false
		}
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		switch value {
		case "terminal_info", "terminal_output", "terminal_exit":
			subject = value
			line = fset.Position(lit.Pos()).Line
			return false
		default:
			return true
		}
	})
	if subject == "" {
		return "", "", 0
	}
	return "production code must use protocol/acp/metautil terminal helpers instead of raw top-level terminal metadata keys", subject, line
}

func eventProtocolAliasVars(file *ast.File, sessionNames map[string]bool) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.ValueSpec:
			if isSessionEventProtocolType(typed.Type, sessionNames) {
				for _, name := range typed.Names {
					out[name.Name] = true
				}
			}
			for i, value := range typed.Values {
				if i >= len(typed.Names) || (!isCloneEventProtocolCall(value, sessionNames) && !isEventProtocolSelector(value)) {
					continue
				}
				out[typed.Names[i].Name] = true
			}
		case *ast.AssignStmt:
			for i, value := range typed.Rhs {
				if i >= len(typed.Lhs) || (!isCloneEventProtocolCall(value, sessionNames) && !isEventProtocolSelector(value)) {
					continue
				}
				if ident, ok := typed.Lhs[i].(*ast.Ident); ok {
					out[ident.Name] = true
				}
			}
		}
		return true
	})
	return out
}

func isSessionEventProtocolType(expr ast.Expr, sessionNames map[string]bool) bool {
	switch typed := expr.(type) {
	case nil:
		return false
	case *ast.StarExpr:
		return isSessionEventProtocolType(typed.X, sessionNames)
	case *ast.SelectorExpr:
		ident, ok := typed.X.(*ast.Ident)
		return ok && sessionNames[ident.Name] && typed.Sel.Name == "EventProtocol"
	default:
		return false
	}
}

func isCloneEventProtocolCall(expr ast.Expr, sessionNames map[string]bool) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "CloneEventProtocol" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && sessionNames[ident.Name]
}

func isEventProtocolSelector(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name == "Protocol"
	case *ast.StarExpr:
		return isEventProtocolSelector(typed.X)
	case *ast.ParenExpr:
		return isEventProtocolSelector(typed.X)
	default:
		return false
	}
}

func gatewayTurnHandleNames(file *ast.File, gatewayNames map[string]bool) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.FuncDecl:
			collectGatewayTurnHandleFields(out, typed.Type.Params, gatewayNames)
		case *ast.FuncLit:
			collectGatewayTurnHandleFields(out, typed.Type.Params, gatewayNames)
		case *ast.ValueSpec:
			if isGatewaySelectorType(typed.Type, gatewayNames, "TurnHandle") {
				for _, name := range typed.Names {
					out[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			for i, rhs := range typed.Rhs {
				if i >= len(typed.Lhs) || !isGatewayTurnHandleAssertion(rhs, gatewayNames) {
					continue
				}
				if ident, ok := typed.Lhs[i].(*ast.Ident); ok {
					out[ident.Name] = true
				}
			}
		}
		return true
	})
	return out
}

func collectGatewayTurnHandleFields(out map[string]bool, fields *ast.FieldList, gatewayNames map[string]bool) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		if !isGatewaySelectorType(field.Type, gatewayNames, "TurnHandle") {
			continue
		}
		for _, name := range field.Names {
			out[name.Name] = true
		}
	}
}

func isGatewayTurnHandleAssertion(expr ast.Expr, gatewayNames map[string]bool) bool {
	assertion, ok := expr.(*ast.TypeAssertExpr)
	return ok && isGatewaySelectorType(assertion.Type, gatewayNames, "TurnHandle")
}

func isGatewaySelectorType(expr ast.Expr, gatewayNames map[string]bool, selectorName string) bool {
	switch typed := expr.(type) {
	case *ast.StarExpr:
		return isGatewaySelectorType(typed.X, gatewayNames, selectorName)
	case *ast.SelectorExpr:
		ident, ok := typed.X.(*ast.Ident)
		return ok && gatewayNames[ident.Name] && typed.Sel.Name == selectorName
	default:
		return false
	}
}

func importNames(file *ast.File, importPath string) map[string]bool {
	out := map[string]bool{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		if spec.Name != nil {
			if spec.Name.Name != "." && spec.Name.Name != "_" {
				out[spec.Name.Name] = true
			}
			continue
		}
		out[pathBase(path)] = true
	}
	return out
}

func boundaryRule(rel string, importPath string, modulePath string) string {
	if !strings.HasPrefix(importPath, modulePath+"/") {
		return ""
	}
	target := strings.TrimPrefix(importPath, modulePath+"/")
	if temporaryArchitectureException(rel, target) {
		return ""
	}
	switch {
	case strings.HasPrefix(rel, "kernel/"):
		if target == "internal/kernel" || strings.HasPrefix(target, "internal/kernel/") {
			return "kernel must not depend on internal/kernel"
		}
		if startsWithAny(target, "impl/", "surfaces/") {
			return "kernel must not depend on impl or surfaces"
		}
	case strings.HasPrefix(rel, "internal/kernel/"):
		if startsWithAny(target, "app/", "impl/", "surfaces/") {
			return "internal/kernel must not depend on app, impl, or surfaces"
		}
	case strings.HasPrefix(rel, "ports/"):
		if strings.HasPrefix(target, "internal/") {
			return "ports must not depend on internal packages"
		}
		if startsWithAny(target, "impl/", "surfaces/") {
			return "ports must not depend on impl or surfaces"
		}
	case strings.HasPrefix(rel, "protocol/"):
		if strings.HasPrefix(target, "internal/") {
			return "protocol must not depend on internal packages"
		}
		if startsWithAny(target, "app/", "impl/", "surfaces/") {
			return "protocol must not depend on app, impl, or surfaces"
		}
	case strings.HasPrefix(rel, "impl/"):
		if strings.HasPrefix(target, "surfaces/") {
			return "impl must not depend on surfaces"
		}
	case strings.HasPrefix(rel, "surfaces/"):
		if strings.HasPrefix(target, "app/") {
			return "surfaces must not depend directly on app"
		}
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

func temporaryArchitectureException(rel string, target string) bool {
	switch {
	case strings.HasPrefix(rel, "internal/kernel/") &&
		strings.HasSuffix(rel, "_test.go") &&
		pathIn(target, "impl/session/file", "impl/session/memory"):
		return true
	default:
		return false
	}
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

func pathBase(path string) string {
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return path
	}
	return path[index+1:]
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
