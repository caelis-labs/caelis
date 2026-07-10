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
			case ".git", ".tmp", ".claude", "node_modules", "vendor":
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
		if rule, subject, line := deletedSDKCompatFileRule(rel); rule != "" {
			violations = append(violations, violation{
				file:    rel,
				line:    line,
				subject: subject,
				rule:    rule,
			})
		}
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
	if rule, subject, line := gatewayAggregateAccessorRule(rel, file, fset); rule != "" {
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
	if file == nil || strings.HasSuffix(rel, "_test.go") {
		return "", "", 0
	}
	if strings.HasPrefix(rel, "agent-sdk/session/") {
		return "", "", 0
	}
	sessionNames := importNames(file, modulePath+"/agent-sdk/session")
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
	return "production code must use agent-sdk/session protocol helpers instead of EventProtocol json:\"-\" aliases", subject, line
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

func gatewayAggregateAccessorRule(rel string, file *ast.File, fset *token.FileSet) (string, string, int) {
	if file == nil || strings.HasSuffix(rel, "_test.go") || rel == "app/gatewayapp/stack.go" || rel == "app/gatewayapp/services.go" {
		return "", "", 0
	}
	var subject string
	var line int
	ast.Inspect(file, func(node ast.Node) bool {
		if subject != "" {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "Kernel", "CurrentGateway":
			subject = gatewayAggregateAccessorSubject(selector)
			line = fset.Position(selector.Pos()).Line
			return false
		default:
			return true
		}
	})
	if subject == "" {
		return "", "", 0
	}
	return "production code must use narrow Stack gateway accessors instead of aggregate gateway escape hatches", subject, line
}

func gatewayAggregateAccessorSubject(selector *ast.SelectorExpr) string {
	if ident, ok := selector.X.(*ast.Ident); ok {
		return ident.Name + "." + selector.Sel.Name + "()"
	}
	return "." + selector.Sel.Name + "()"
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
	if isMigratedRuntimePortsPackage(target) {
		return sdkOwnedPortsImportMessage(target)
	}
	if rule := deletedSDKImplImportRule(target); rule != "" {
		return rule
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
	case strings.HasPrefix(rel, "agent-sdk/"):
		if startsWithAny(target, "app/", "surfaces/", "protocol/acp/", "internal/acpagentbridge/") {
			return "agent-sdk must not depend on product-host, surface, ACP protocol, or ACP implementation packages"
		}
		if target == "ports/gateway" || strings.HasPrefix(target, "ports/gateway/") {
			return "agent-sdk must not depend on ports/gateway"
		}
		if leaf := agentSDKSandboxMovedLeaf(rel); leaf != "" {
			if rule := agentSDKSandboxMovedLeafForbiddenDependency(leaf, target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(target, "internal/") {
			return "agent-sdk must not depend on repository internal packages"
		}
		if strings.HasPrefix(rel, "agent-sdk/model/codefreecaps/") {
			if strings.HasPrefix(target, "impl/") {
				return "agent-sdk/model/codefreecaps must not depend on impl packages"
			}
			if strings.HasPrefix(target, "ports/") {
				return "agent-sdk/model/codefreecaps must not depend on ports packages"
			}
			if strings.HasPrefix(target, "app/") {
				return "agent-sdk/model/codefreecaps must not depend on app packages"
			}
			if strings.HasPrefix(target, "internal/") {
				return "agent-sdk/model/codefreecaps must not depend on repository internal packages"
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/model/providers/") {
			if strings.HasPrefix(target, "ports/") {
				return "agent-sdk/model/providers must not depend on ports packages"
			}
			if strings.HasPrefix(target, "impl/") {
				return "agent-sdk/model/providers must not depend on impl packages"
			}
			if strings.HasPrefix(target, "app/") {
				return "agent-sdk/model/providers must not depend on app packages"
			}
			if strings.HasPrefix(target, "surfaces/") {
				return "agent-sdk/model/providers must not depend on surfaces packages"
			}
			if strings.HasPrefix(target, "protocol/acp/") {
				return "agent-sdk/model/providers must not depend on protocol/acp packages"
			}
			if strings.HasPrefix(target, "internal/") {
				return "agent-sdk/model/providers must not depend on repository internal packages"
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/model/") &&
			(target == "ports/model" || strings.HasPrefix(target, "ports/model/")) {
			return "agent-sdk/model must not depend on ports/model"
		}
		if isAgentSDKToolRootPackage(rel) &&
			(target == "ports/tool" || strings.HasPrefix(target, "ports/tool/")) {
			return "agent-sdk/tool must not depend on ports/tool"
		}
		if isAgentSDKToolRootPackage(rel) &&
			(target == "ports/model" || strings.HasPrefix(target, "ports/model/")) {
			return "agent-sdk/tool must not depend on ports/model"
		}
		if strings.HasPrefix(rel, "agent-sdk/tool/registry/") {
			if strings.HasPrefix(target, "ports/") {
				return "agent-sdk/tool/registry must not depend on ports packages"
			}
			if strings.HasPrefix(target, "impl/") {
				return "agent-sdk/tool/registry must not depend on impl packages"
			}
			if strings.HasPrefix(target, "app/") {
				return "agent-sdk/tool/registry must not depend on app packages"
			}
			if strings.HasPrefix(target, "internal/") {
				return "agent-sdk/tool/registry must not depend on repository internal packages"
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/tool/mcp/") {
			if strings.HasPrefix(target, "ports/") {
				return "agent-sdk/tool/mcp must not depend on ports packages"
			}
			if strings.HasPrefix(target, "impl/") {
				return "agent-sdk/tool/mcp must not depend on impl packages"
			}
			if strings.HasPrefix(target, "app/") {
				return "agent-sdk/tool/mcp must not depend on app packages"
			}
			if strings.HasPrefix(target, "surfaces/") {
				return "agent-sdk/tool/mcp must not depend on surfaces packages"
			}
			if strings.HasPrefix(target, "protocol/acp/") {
				return "agent-sdk/tool/mcp must not depend on protocol/acp packages"
			}
			if strings.HasPrefix(target, "internal/") {
				return "agent-sdk/tool/mcp must not depend on repository internal packages"
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/tool/builtin/toolutil/") {
			if rule := agentSDKBuiltinLeafForbiddenDependency("toolutil", target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/tool/builtin/argparse/") {
			if rule := agentSDKBuiltinLeafForbiddenDependency("argparse", target); rule != "" {
				return rule
			}
		}
		if leaf := agentSDKBuiltinStatelessLeaf(rel); leaf != "" {
			if rule := agentSDKBuiltinLeafForbiddenDependency(leaf, target); rule != "" {
				return rule
			}
		}
		if isAgentSDKToolBuiltinRootFile(rel) {
			if rule := agentSDKToolBuiltinRootForbiddenDependency(target); rule != "" {
				return rule
			}
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/model" || strings.HasPrefix(target, "ports/model/")) {
			return "agent-sdk root must not depend on ports/model"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/tool" || strings.HasPrefix(target, "ports/tool/")) {
			return "agent-sdk root must not depend on ports/tool"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/session" || strings.HasPrefix(target, "ports/session/")) {
			return "agent-sdk root must not depend on ports/session"
		}
		if strings.HasPrefix(rel, "agent-sdk/session/") &&
			(target == "ports/session" || strings.HasPrefix(target, "ports/session/")) {
			return "agent-sdk/session must not depend on ports/session"
		}
		if strings.HasPrefix(rel, "agent-sdk/session/") &&
			(target == "ports/model" || strings.HasPrefix(target, "ports/model/")) {
			return "agent-sdk/session must not depend on ports/model"
		}
		if strings.HasPrefix(rel, "agent-sdk/session/") &&
			(target == "ports/tool" || strings.HasPrefix(target, "ports/tool/")) {
			return "agent-sdk/session must not depend on ports/tool"
		}
		if strings.HasPrefix(rel, "agent-sdk/approval/") &&
			(target == "ports/approval" || strings.HasPrefix(target, "ports/approval/")) {
			return "agent-sdk/approval must not depend on ports/approval"
		}
		if strings.HasPrefix(rel, "agent-sdk/approval/") &&
			(target == "ports/agent" || strings.HasPrefix(target, "ports/agent/")) {
			return "agent-sdk/approval must not depend on ports/agent"
		}
		if strings.HasPrefix(rel, "agent-sdk/approval/") &&
			(target == "ports/model" || strings.HasPrefix(target, "ports/model/")) {
			return "agent-sdk/approval must not depend on ports/model"
		}
		if strings.HasPrefix(rel, "agent-sdk/approval/") &&
			(target == "ports/session" || strings.HasPrefix(target, "ports/session/")) {
			return "agent-sdk/approval must not depend on ports/session"
		}
		if strings.HasPrefix(rel, "agent-sdk/skill/") {
			if strings.HasPrefix(target, "ports/") {
				return "agent-sdk/skill must not depend on ports packages"
			}
			if strings.HasPrefix(target, "impl/") {
				return "agent-sdk/skill must not depend on impl packages"
			}
			if strings.HasPrefix(target, "app/") {
				return "agent-sdk/skill must not depend on app packages"
			}
			if strings.HasPrefix(target, "surfaces/") {
				return "agent-sdk/skill must not depend on surfaces packages"
			}
			if strings.HasPrefix(target, "protocol/acp/") {
				return "agent-sdk/skill must not depend on protocol/acp packages"
			}
			if strings.HasPrefix(target, "internal/") {
				return "agent-sdk/skill must not depend on repository internal packages"
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/policy/") {
			if rule := agentSDKPolicyForbiddenDependency(rel, target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/runtime/compact/") {
			if rule := agentSDKRuntimeCompactForbiddenDependency(target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/runtime/controller/") {
			if rule := agentSDKRuntimeControllerForbiddenDependency(target); rule != "" {
				return rule
			}
		}
		if isAgentSDKRuntimeImplementation(rel) && !strings.HasSuffix(rel, "_test.go") {
			if rule := agentSDKRuntimeImplementationForbiddenDependency(target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/session/userdisplay/") {
			if rule := agentSDKSessionUserdisplayForbiddenDependency(target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/display/") {
			if rule := agentSDKDisplayForbiddenDependency(target); rule != "" {
				return rule
			}
		}
		if strings.HasPrefix(rel, "agent-sdk/sandbox/") &&
			(target == "ports/sandbox" || strings.HasPrefix(target, "ports/sandbox/")) {
			return "agent-sdk/sandbox must not depend on ports/sandbox"
		}
		if strings.HasPrefix(rel, "agent-sdk/tool/commanddiag/") &&
			(target == "ports/sandbox" || strings.HasPrefix(target, "ports/sandbox/")) {
			return "agent-sdk/tool/commanddiag must not depend on ports/sandbox"
		}
		if isAgentSDKTaskRootPackage(rel) &&
			(target == "ports/task" || strings.HasPrefix(target, "ports/task/")) {
			return "agent-sdk/task must not depend on ports/task"
		}
		if isAgentSDKTaskRootPackage(rel) &&
			(target == "ports/sandbox" || strings.HasPrefix(target, "ports/sandbox/")) {
			return "agent-sdk/task must not depend on ports/sandbox"
		}
		if isAgentSDKTaskRootPackage(rel) &&
			(target == "ports/session" || strings.HasPrefix(target, "ports/session/")) {
			return "agent-sdk/task must not depend on ports/session"
		}
		if isAgentSDKTaskRootPackage(rel) &&
			(target == "ports/subagent" || strings.HasPrefix(target, "ports/subagent/")) {
			return "agent-sdk/task must not depend on ports/subagent"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/delegation/") &&
			(target == "ports/delegation" || strings.HasPrefix(target, "ports/delegation/")) {
			return "agent-sdk/task/delegation must not depend on ports/delegation"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/stream/") &&
			(target == "ports/stream" || strings.HasPrefix(target, "ports/stream/")) {
			return "agent-sdk/task/stream must not depend on ports/stream"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/stream/") &&
			(target == "ports/session" || strings.HasPrefix(target, "ports/session/")) {
			return "agent-sdk/task/stream must not depend on ports/session"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/subagent/") &&
			(target == "ports/subagent" || strings.HasPrefix(target, "ports/subagent/")) {
			return "agent-sdk/task/subagent must not depend on ports/subagent"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/subagent/") &&
			(target == "ports/delegation" || strings.HasPrefix(target, "ports/delegation/")) {
			return "agent-sdk/task/subagent must not depend on ports/delegation"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/subagent/") &&
			(target == "ports/session" || strings.HasPrefix(target, "ports/session/")) {
			return "agent-sdk/task/subagent must not depend on ports/session"
		}
		if strings.HasPrefix(rel, "agent-sdk/task/subagent/") &&
			(target == "ports/stream" || strings.HasPrefix(target, "ports/stream/")) {
			return "agent-sdk/task/subagent must not depend on ports/stream"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/delegation" || strings.HasPrefix(target, "ports/delegation/")) {
			return "agent-sdk root must not depend on ports/delegation"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/stream" || strings.HasPrefix(target, "ports/stream/")) {
			return "agent-sdk root must not depend on ports/stream"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/subagent" || strings.HasPrefix(target, "ports/subagent/")) {
			return "agent-sdk root must not depend on ports/subagent"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/task" || strings.HasPrefix(target, "ports/task/")) {
			return "agent-sdk root must not depend on ports/task"
		}
		if isAgentSDKRootFile(rel) &&
			(target == "ports/approval" || strings.HasPrefix(target, "ports/approval/")) {
			return "agent-sdk root must not depend on ports/approval"
		}
		if target != "agent-sdk" && !strings.HasPrefix(target, "agent-sdk/") {
			return "agent-sdk must not depend on non-SDK Caelis packages"
		}
	case strings.HasPrefix(rel, "internal/sandboxrouter/"):
		if strings.HasSuffix(rel, "_test.go") {
			return ""
		}
		if target == "ports/sandbox" || strings.HasPrefix(target, "ports/sandbox/") {
			return "internal/sandboxrouter must not depend on ports/sandbox; use agent-sdk/sandbox"
		}
	case strings.HasPrefix(rel, "app/gatewayapp/internal/sandboxpolicy/"):
		if strings.HasSuffix(rel, "_test.go") {
			return ""
		}
		if target == "ports/sandbox" || strings.HasPrefix(target, "ports/sandbox/") {
			return "app/gatewayapp/internal/sandboxpolicy must not depend on ports/sandbox; use agent-sdk/sandbox"
		}
	case strings.HasPrefix(rel, "app/gatewayapp/internal/skilldiscovery/"):
		if strings.HasSuffix(rel, "_test.go") {
			return ""
		}
		if !isAllowedSkillDiscoveryTarget(target) {
			return "app/gatewayapp/internal/skilldiscovery must only depend on agent-sdk/skill and agent-sdk/skill/fs"
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

type deletedSDKImplPath struct {
	prefix string
	sdk    string
}

func deletedSDKImplPaths() []deletedSDKImplPath {
	var paths []deletedSDKImplPath
	paths = append(paths,
		deletedSDKImplPath{prefix: "impl/agent/local/chat", sdk: "agent-sdk/runtime/chat"},
		deletedSDKImplPath{prefix: "impl/tool/builtin/internal/toolutil", sdk: "agent-sdk/tool/builtin/toolutil"},
		deletedSDKImplPath{prefix: "impl/tool/internal/argparse", sdk: "agent-sdk/tool/builtin/argparse"},
	)
	for _, leaf := range builtinStatelessLeafPackages() {
		paths = append(paths, deletedSDKImplPath{
			prefix: "impl/tool/builtin/" + leaf,
			sdk:    "agent-sdk/tool/builtin/" + leaf,
		})
	}
	paths = append(paths, deletedSDKImplPath{prefix: "impl/tool/builtin", sdk: "agent-sdk/tool/builtin"})
	for _, helper := range sandboxMovedWindowsHelperNames() {
		paths = append(paths, deletedSDKImplPath{
			prefix: "impl/sandbox/windows/internal/" + helper,
			sdk:    "agent-sdk/sandbox/windows/internal/" + helper,
		})
	}
	for _, helper := range sandboxMovedBackendHelperNames() {
		paths = append(paths, deletedSDKImplPath{
			prefix: "impl/sandbox/internal/" + helper,
			sdk:    "agent-sdk/sandbox/backend/" + helper,
		})
	}
	paths = append(paths,
		deletedSDKImplPath{prefix: "impl/sandbox/internal/consoleoutput", sdk: "agent-sdk/sandbox/consoleoutput"},
		deletedSDKImplPath{prefix: "impl/sandbox/internal/textstream", sdk: "agent-sdk/sandbox/textstream"},
		deletedSDKImplPath{prefix: "impl/sandbox/internal/winps", sdk: "agent-sdk/sandbox/windows/winps"},
	)
	paths = append(paths,
		deletedSDKImplPath{prefix: "impl/agent/local", sdk: "agent-sdk/runtime"},
		deletedSDKImplPath{prefix: "impl/model/internal/codefreecaps", sdk: "agent-sdk/model/codefreecaps"},
		deletedSDKImplPath{prefix: "impl/model/providers", sdk: "agent-sdk/model/providers"},
		deletedSDKImplPath{prefix: "impl/model/catalog", sdk: "agent-sdk/model/catalog"},
		deletedSDKImplPath{prefix: "impl/approval/agentreview", sdk: "agent-sdk/approval"},
		deletedSDKImplPath{prefix: "impl/policy/presets", sdk: "agent-sdk/policy/presets"},
		deletedSDKImplPath{prefix: "impl/policy/devcache", sdk: "agent-sdk/policy/devcache"},
		deletedSDKImplPath{prefix: "impl/session/file", sdk: "agent-sdk/session/file"},
		deletedSDKImplPath{prefix: "impl/session/memory", sdk: "agent-sdk/session/memory"},
		deletedSDKImplPath{prefix: "impl/stream/memory", sdk: "agent-sdk/task/stream/memory"},
		deletedSDKImplPath{prefix: "impl/sandbox/host", sdk: "agent-sdk/sandbox/host"},
		deletedSDKImplPath{prefix: "impl/sandbox/bwrap", sdk: "agent-sdk/sandbox/bwrap"},
		deletedSDKImplPath{prefix: "impl/sandbox/landlock", sdk: "agent-sdk/sandbox/landlock"},
		deletedSDKImplPath{prefix: "impl/sandbox/seatbelt", sdk: "agent-sdk/sandbox/seatbelt"},
		deletedSDKImplPath{prefix: "impl/sandbox/windows", sdk: "agent-sdk/sandbox/windows"},
		deletedSDKImplPath{prefix: "impl/tool/mcp", sdk: "agent-sdk/tool/mcp"},
		deletedSDKImplPath{prefix: "impl/tool/registry", sdk: "agent-sdk/tool/registry"},
		deletedSDKImplPath{prefix: "impl/skill/fs", sdk: "app/gatewayapp/internal/skilldiscovery"},
		deletedSDKImplPath{prefix: "impl/skill/system", sdk: "app/gatewayapp/internal/skilldiscovery"},
		deletedSDKImplPath{prefix: "impl/agent/acp", sdk: "internal/acpagentbridge"},
	)
	paths = append(paths,
		deletedSDKImplPath{prefix: "impl/policy", sdk: "agent-sdk/policy"},
		deletedSDKImplPath{prefix: "impl/session", sdk: "agent-sdk/session"},
		deletedSDKImplPath{prefix: "impl/stream", sdk: "agent-sdk/task/stream"},
		deletedSDKImplPath{prefix: "impl/sandbox", sdk: "agent-sdk/sandbox"},
		deletedSDKImplPath{prefix: "impl/tool", sdk: "agent-sdk/tool"},
	)
	return paths
}

func deletedSDKImplImportRule(target string) string {
	if mapping, ok := deletedSDKImplPathMatch(target); ok {
		switch mapping.prefix {
		case "impl/skill/fs", "impl/skill/system":
			return fmt.Sprintf(
				"must not import %s; use agent-sdk/skill/fs for reusable discovery and app/gatewayapp/internal/skilldiscovery for Caelis system skill discovery",
				mapping.prefix,
			)
		default:
			return fmt.Sprintf("must not import %s; use %s", mapping.prefix, mapping.sdk)
		}
	}
	return ""
}

func deletedSDKCompatFileRule(rel string) (string, string, int) {
	pkg := filepath.ToSlash(filepath.Dir(rel))
	if pkg == "." {
		return "", "", 0
	}
	if isMigratedRuntimePortsPackage(pkg) {
		return sdkOwnedPortsCompatFileMessage(pkg), pkg, 1
	}
	if mapping, ok := deletedSDKImplPathMatch(pkg); ok {
		return fmt.Sprintf("must not recreate %s; use %s", mapping.prefix, mapping.sdk), pkg, 1
	}
	return "", "", 0
}

func deletedSDKImplPathMatch(target string) (deletedSDKImplPath, bool) {
	for _, mapping := range deletedSDKImplPaths() {
		if target == mapping.prefix || strings.HasPrefix(target, mapping.prefix+"/") {
			return mapping, true
		}
	}
	return deletedSDKImplPath{}, false
}

func sdkOwnedPortsCompatFileMessage(pkg string) string {
	message := sdkOwnedPortsImportMessage(pkg)
	switch {
	case strings.HasPrefix(message, "production code must not depend on "):
		return "must not recreate" + strings.TrimPrefix(message, "production code must not depend on")
	case message != "":
		return strings.Replace(message, "must not depend on", "must not recreate", 1)
	default:
		return "must not recreate SDK-owned ports compatibility packages; use agent-sdk/*"
	}
}

func temporaryArchitectureException(rel string, target string) bool {
	switch {
	case strings.HasPrefix(rel, "internal/kernel/") &&
		strings.HasSuffix(rel, "_test.go") &&
		pathIn(target, "agent-sdk/session/file", "agent-sdk/session/memory"):
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

func isAgentSDKRootFile(rel string) bool {
	if !strings.HasPrefix(rel, "agent-sdk/") {
		return false
	}
	rest := strings.TrimPrefix(rel, "agent-sdk/")
	return !strings.Contains(rest, "/")
}

func isAgentSDKToolRootPackage(rel string) bool {
	if !strings.HasPrefix(rel, "agent-sdk/tool/") {
		return false
	}
	rest := strings.TrimPrefix(rel, "agent-sdk/tool/")
	return rest != "" && !strings.Contains(rest, "/")
}

func isAgentSDKTaskRootPackage(rel string) bool {
	if !strings.HasPrefix(rel, "agent-sdk/task/") {
		return false
	}
	rest := strings.TrimPrefix(rel, "agent-sdk/task/")
	return rest != "" && !strings.Contains(rest, "/")
}

func isAllowedSkillDiscoveryTarget(target string) bool {
	return target == "agent-sdk/skill" ||
		strings.HasPrefix(target, "agent-sdk/skill/")
}

func builtinStatelessLeafPackages() []string {
	return []string{"plan", "task", "spawn", "toolsearch", "filesystem", "shell", "web", "skill"}
}

func isAgentSDKToolBuiltinRootFile(rel string) bool {
	if !strings.HasPrefix(rel, "agent-sdk/tool/builtin/") {
		return false
	}
	rest := strings.TrimPrefix(rel, "agent-sdk/tool/builtin/")
	return rest != "" && !strings.Contains(rest, "/")
}

func agentSDKToolBuiltinRootForbiddenDependency(target string) string {
	if strings.HasPrefix(target, "ports/") {
		return "agent-sdk/tool/builtin must not depend on ports packages"
	}
	if strings.HasPrefix(target, "impl/") {
		return "agent-sdk/tool/builtin must not depend on impl packages"
	}
	if strings.HasPrefix(target, "app/") {
		return "agent-sdk/tool/builtin must not depend on app packages"
	}
	if strings.HasPrefix(target, "surfaces/") {
		return "agent-sdk/tool/builtin must not depend on surfaces packages"
	}
	if strings.HasPrefix(target, "protocol/acp/") {
		return "agent-sdk/tool/builtin must not depend on protocol/acp packages"
	}
	if strings.HasPrefix(target, "internal/") {
		return "agent-sdk/tool/builtin must not depend on repository internal packages"
	}
	return ""
}

func agentSDKBuiltinStatelessLeaf(rel string) string {
	for _, leaf := range builtinStatelessLeafPackages() {
		if strings.HasPrefix(rel, "agent-sdk/tool/builtin/"+leaf+"/") {
			return leaf
		}
	}
	return ""
}

func sandboxMovedBackendHelperNames() []string {
	return []string{"cmdsession", "fsboundary", "policy", "policyfs", "procutil", "runnerruntime"}
}

func sandboxMovedWindowsHelperNames() []string {
	return []string{"acl", "capability", "job", "pathutil", "policy", "win32"}
}

func agentSDKSandboxMovedLeaf(rel string) string {
	switch {
	case strings.HasPrefix(rel, "agent-sdk/sandbox/host/"):
		return "host"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/consoleoutput/"):
		return "consoleoutput"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/textstream/"):
		return "textstream"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/bwrap/"):
		return "bwrap"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/landlock/"):
		return "landlock"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/seatbelt/"):
		return "seatbelt"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/windows/winps/"):
		return "windows/winps"
	case strings.HasPrefix(rel, "agent-sdk/sandbox/windows/internal/"):
		rest := strings.TrimPrefix(rel, "agent-sdk/sandbox/windows/internal/")
		parts := strings.SplitN(rest, "/", 2)
		if parts[0] == "" {
			return ""
		}
		for _, helper := range sandboxMovedWindowsHelperNames() {
			if parts[0] == helper {
				return "windows/internal/" + helper
			}
		}
		return ""
	case strings.HasPrefix(rel, "agent-sdk/sandbox/windows/"):
		return "windows"
	default:
		rest := strings.TrimPrefix(rel, "agent-sdk/sandbox/backend/")
		if rest == rel {
			return ""
		}
		parts := strings.SplitN(rest, "/", 2)
		if parts[0] == "" {
			return ""
		}
		for _, helper := range sandboxMovedBackendHelperNames() {
			if parts[0] == helper {
				return "backend/" + helper
			}
		}
		return ""
	}
}

func agentSDKSandboxMovedLeafForbiddenDependency(leaf, target string) string {
	if isAllowedAgentSDKSandboxMovedLeafTarget(leaf, target) {
		return ""
	}
	if strings.HasPrefix(target, "ports/") {
		return fmt.Sprintf("agent-sdk/sandbox/%s must not depend on ports packages", leaf)
	}
	if strings.HasPrefix(target, "impl/") {
		return fmt.Sprintf("agent-sdk/sandbox/%s must not depend on impl packages", leaf)
	}
	if strings.HasPrefix(target, "app/") {
		return fmt.Sprintf("agent-sdk/sandbox/%s must not depend on app packages", leaf)
	}
	if strings.HasPrefix(target, "surfaces/") {
		return fmt.Sprintf("agent-sdk/sandbox/%s must not depend on surfaces packages", leaf)
	}
	if strings.HasPrefix(target, "protocol/acp/") {
		return fmt.Sprintf("agent-sdk/sandbox/%s must not depend on protocol/acp packages", leaf)
	}
	if strings.HasPrefix(target, "internal/") {
		return fmt.Sprintf("agent-sdk/sandbox/%s must not depend on repository internal packages", leaf)
	}
	return ""
}

func isAllowedAgentSDKSandboxMovedLeafTarget(leaf, target string) bool {
	allowed := []string{"golang.org/"}
	switch {
	case leaf == "host":
		allowed = append(allowed,
			"agent-sdk/sandbox",
			"agent-sdk/sandbox/consoleoutput",
			"agent-sdk/sandbox/textstream",
		)
	case leaf == "bwrap", leaf == "landlock", leaf == "seatbelt", leaf == "windows":
		allowed = append(allowed, allowedAgentSDKSandboxBackendDependencyPrefixes()...)
	case strings.HasPrefix(leaf, "backend/"):
		allowed = append(allowed, allowedAgentSDKSandboxBackendDependencyPrefixes()...)
	case strings.HasPrefix(leaf, "windows/internal/"):
		allowed = append(allowed, allowedAgentSDKSandboxWindowsDependencyPrefixes()...)
	}
	for _, prefix := range allowed {
		if target == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}

func allowedAgentSDKSandboxBackendDependencyPrefixes() []string {
	prefixes := []string{
		"agent-sdk/sandbox",
		"agent-sdk/sandbox/host",
		"agent-sdk/sandbox/consoleoutput",
		"agent-sdk/sandbox/textstream",
		"agent-sdk/sandbox/bwrap",
		"agent-sdk/sandbox/landlock",
		"agent-sdk/sandbox/seatbelt",
		"agent-sdk/sandbox/windows",
		"agent-sdk/sandbox/windows/winps",
	}
	for _, helper := range sandboxMovedBackendHelperNames() {
		prefixes = append(prefixes, "agent-sdk/sandbox/backend/"+helper)
	}
	for _, helper := range sandboxMovedWindowsHelperNames() {
		prefixes = append(prefixes, "agent-sdk/sandbox/windows/internal/"+helper)
	}
	return prefixes
}

func allowedAgentSDKSandboxWindowsDependencyPrefixes() []string {
	prefixes := []string{"agent-sdk/sandbox"}
	for _, helper := range sandboxMovedWindowsHelperNames() {
		prefixes = append(prefixes, "agent-sdk/sandbox/windows/internal/"+helper)
	}
	for _, helper := range sandboxMovedBackendHelperNames() {
		prefixes = append(prefixes, "agent-sdk/sandbox/backend/"+helper)
	}
	return prefixes
}

func agentSDKBuiltinLeafForbiddenDependency(leaf, target string) string {
	if strings.HasPrefix(target, "ports/") {
		return fmt.Sprintf("agent-sdk/tool/builtin/%s must not depend on ports packages", leaf)
	}
	if strings.HasPrefix(target, "impl/") {
		return fmt.Sprintf("agent-sdk/tool/builtin/%s must not depend on impl packages", leaf)
	}
	if strings.HasPrefix(target, "app/") {
		return fmt.Sprintf("agent-sdk/tool/builtin/%s must not depend on app packages", leaf)
	}
	if strings.HasPrefix(target, "surfaces/") {
		return fmt.Sprintf("agent-sdk/tool/builtin/%s must not depend on surfaces packages", leaf)
	}
	if strings.HasPrefix(target, "protocol/acp/") {
		return fmt.Sprintf("agent-sdk/tool/builtin/%s must not depend on protocol/acp packages", leaf)
	}
	if strings.HasPrefix(target, "internal/") {
		return fmt.Sprintf("agent-sdk/tool/builtin/%s must not depend on repository internal packages", leaf)
	}
	return ""
}

func agentSDKPolicyForbiddenDependency(rel, target string) string {
	for _, prefix := range []string{"ports/", "impl/", "app/", "surfaces/", "protocol/acp/", "internal/"} {
		if strings.HasPrefix(target, prefix) {
			switch prefix {
			case "ports/":
				return "agent-sdk/policy must not depend on ports packages"
			case "impl/":
				return "agent-sdk/policy must not depend on impl packages"
			case "app/":
				return "agent-sdk/policy must not depend on app packages"
			case "surfaces/":
				return "agent-sdk/policy must not depend on surfaces packages"
			case "protocol/acp/":
				return "agent-sdk/policy must not depend on protocol/acp packages"
			case "internal/":
				return "agent-sdk/policy must not depend on repository internal packages"
			}
		}
	}
	if strings.HasPrefix(rel, "agent-sdk/policy/presets/") &&
		target == "agent-sdk/policy/devcache" {
		return ""
	}
	if strings.HasPrefix(rel, "agent-sdk/policy/presets/") &&
		(target == "agent-sdk/policy" || strings.HasPrefix(target, "agent-sdk/policy/")) {
		return ""
	}
	if strings.HasPrefix(rel, "agent-sdk/policy/devcache/") {
		return ""
	}
	if strings.HasPrefix(rel, "agent-sdk/policy/") &&
		(target == "agent-sdk/session" || strings.HasPrefix(target, "agent-sdk/session/") ||
			target == "agent-sdk/tool" || strings.HasPrefix(target, "agent-sdk/tool/") ||
			target == "agent-sdk/sandbox" || strings.HasPrefix(target, "agent-sdk/sandbox/")) {
		return ""
	}
	return ""
}

func agentSDKRuntimeControllerForbiddenDependency(target string) string {
	for _, prefix := range []string{"ports/", "impl/", "app/", "surfaces/", "protocol/acp/", "internal/"} {
		if strings.HasPrefix(target, prefix) {
			switch prefix {
			case "ports/":
				return "agent-sdk/runtime/controller must not depend on ports packages"
			case "impl/":
				return "agent-sdk/runtime/controller must not depend on impl packages"
			case "app/":
				return "agent-sdk/runtime/controller must not depend on app packages"
			case "surfaces/":
				return "agent-sdk/runtime/controller must not depend on surfaces packages"
			case "protocol/acp/":
				return "agent-sdk/runtime/controller must not depend on protocol/acp packages"
			case "internal/":
				return "agent-sdk/runtime/controller must not depend on repository internal packages"
			}
		}
	}
	if target == "agent-sdk/model" || strings.HasPrefix(target, "agent-sdk/model/") ||
		target == "agent-sdk/session" || strings.HasPrefix(target, "agent-sdk/session/") {
		return ""
	}
	return ""
}

func agentSDKRuntimeCompactForbiddenDependency(target string) string {
	for _, prefix := range []string{"ports/", "impl/", "app/", "surfaces/", "protocol/acp/", "internal/"} {
		if strings.HasPrefix(target, prefix) {
			switch prefix {
			case "ports/":
				return "agent-sdk/runtime/compact must not depend on ports packages"
			case "impl/":
				return "agent-sdk/runtime/compact must not depend on impl packages"
			case "app/":
				return "agent-sdk/runtime/compact must not depend on app packages"
			case "surfaces/":
				return "agent-sdk/runtime/compact must not depend on surfaces packages"
			case "protocol/acp/":
				return "agent-sdk/runtime/compact must not depend on protocol/acp packages"
			case "internal/":
				return "agent-sdk/runtime/compact must not depend on repository internal packages"
			}
		}
	}
	if target == "agent-sdk/model" || strings.HasPrefix(target, "agent-sdk/model/") ||
		target == "agent-sdk/session" || strings.HasPrefix(target, "agent-sdk/session/") {
		return ""
	}
	return ""
}

func agentSDKSessionUserdisplayForbiddenDependency(target string) string {
	for _, prefix := range []string{"ports/", "impl/", "app/", "surfaces/", "protocol/acp/", "internal/"} {
		if strings.HasPrefix(target, prefix) {
			switch prefix {
			case "ports/":
				return "agent-sdk/session/userdisplay must not depend on ports packages"
			case "impl/":
				return "agent-sdk/session/userdisplay must not depend on impl packages"
			case "app/":
				return "agent-sdk/session/userdisplay must not depend on app packages"
			case "surfaces/":
				return "agent-sdk/session/userdisplay must not depend on surfaces packages"
			case "protocol/acp/":
				return "agent-sdk/session/userdisplay must not depend on protocol/acp packages"
			case "internal/":
				return "agent-sdk/session/userdisplay must not depend on repository internal packages"
			}
		}
	}
	if target == "agent-sdk/model" || strings.HasPrefix(target, "agent-sdk/model/") {
		return ""
	}
	return ""
}

func agentSDKDisplayForbiddenDependency(target string) string {
	for _, prefix := range []string{"ports/", "impl/", "app/", "surfaces/", "protocol/acp/", "internal/"} {
		if strings.HasPrefix(target, prefix) {
			switch prefix {
			case "ports/":
				return "agent-sdk/display must not depend on ports packages"
			case "impl/":
				return "agent-sdk/display must not depend on impl packages"
			case "app/":
				return "agent-sdk/display must not depend on app packages"
			case "surfaces/":
				return "agent-sdk/display must not depend on surfaces packages"
			case "protocol/acp/":
				return "agent-sdk/display must not depend on protocol/acp packages"
			case "internal/":
				return "agent-sdk/display must not depend on repository internal packages"
			}
		}
	}
	return ""
}

func isAgentSDKRuntimeImplementation(rel string) bool {
	if strings.HasPrefix(rel, "agent-sdk/runtime/compact/") ||
		strings.HasPrefix(rel, "agent-sdk/runtime/controller/") {
		return false
	}
	return strings.HasPrefix(rel, "agent-sdk/runtime/")
}

func agentSDKRuntimeImplementationForbiddenDependency(target string) string {
	for _, prefix := range []string{"ports/", "impl/", "app/", "surfaces/", "protocol/acp/", "internal/"} {
		if strings.HasPrefix(target, prefix) {
			return "agent-sdk/runtime must not depend on product-host or old ports packages"
		}
	}
	return ""
}

func isMigratedRuntimePortsPackage(target string) bool {
	for _, prefix := range sdkOwnedPortsCompatPrefixes() {
		if target == prefix || strings.HasPrefix(target, prefix+"/") {
			return true
		}
	}
	return false
}

func sdkOwnedPortsCompatPrefixes() []string {
	return []string{
		"ports/agent",
		"ports/model",
		"ports/tool",
		"ports/session",
		"ports/sandbox",
		"ports/approval",
		"ports/policy",
		"ports/delegation",
		"ports/stream",
		"ports/task",
		"ports/subagent",
		"ports/skill",
		"ports/compact",
		"ports/userdisplay",
		"ports/displaypolicy",
		"ports/assembly",
		"ports/controller",
	}
}

func sdkOwnedPortsImportMessage(target string) string {
	for _, mapping := range []struct {
		ports string
		sdk   string
	}{
		{"ports/tool/commanddiag", "agent-sdk/tool/commanddiag"},
		{"ports/subagent/agenthandle", "agent-sdk/task/agenthandle"},
		{"ports/displaypolicy", "agent-sdk/display"},
		{"ports/userdisplay", "agent-sdk/session/userdisplay"},
		{"ports/assembly", "internal/controlassembly"},
		{"ports/controller", "agent-sdk/runtime/controller"},
		{"ports/delegation", "agent-sdk/task/delegation"},
		{"ports/subagent", "agent-sdk/task/subagent"},
		{"ports/compact", "agent-sdk/runtime/compact"},
		{"ports/approval", "agent-sdk/approval"},
		{"ports/sandbox", "agent-sdk/sandbox"},
		{"ports/session", "agent-sdk/session"},
		{"ports/policy", "agent-sdk/policy"},
		{"ports/stream", "agent-sdk/task/stream"},
		{"ports/agent", "agent-sdk"},
		{"ports/model", "agent-sdk/model"},
		{"ports/tool", "agent-sdk/tool"},
		{"ports/task", "agent-sdk/task"},
		{"ports/skill", "agent-sdk/skill"},
	} {
		if target == mapping.ports || strings.HasPrefix(target, mapping.ports+"/") {
			return fmt.Sprintf("production code must not depend on %s; use %s", mapping.ports, mapping.sdk)
		}
	}
	return "production code must not depend on SDK-owned ports compatibility packages; use agent-sdk/*"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
