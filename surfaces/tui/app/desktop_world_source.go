package tuiapp

import (
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

type desktopWorldAction struct {
	phrase string
	short  string
	read   bool
}

var desktopWorldActions = map[string]desktopWorldAction{
	"look":             {phrase: "inspect the window", short: "inspect", read: true},
	"capture":          {phrase: "take a screenshot", short: "capture", read: true},
	"readText":         {phrase: "read text", short: "read", read: true},
	"wait":             {phrase: "wait", short: "wait", read: true},
	"waitForNewWindow": {phrase: "wait for a window", short: "wait", read: true},
	"click":            {phrase: "click", short: "click"},
	"setValue":         {phrase: "set a value", short: "set value"},
	"typeText":         {phrase: "type text", short: "type"},
	"keypress":         {phrase: "press keys", short: "press"},
	"move":             {phrase: "move the pointer", short: "move"},
	"drag":             {phrase: "drag", short: "drag"},
	"scroll":           {phrase: "scroll", short: "scroll"},
	"focus":            {phrase: "focus the window", short: "focus"},
	"quit":             {phrase: "quit the app", short: "quit"},
}

type desktopWorldBinding struct {
	literal     ast.Expression
	observation bool
}

// Only direct calls in the straight-line prefix can describe what the script
// does. Result formatting can end that prefix without erasing earlier calls.
func desktopWorldScriptActions(source string) []desktopWorldAction {
	if source == "" || len(source) > 64<<10 {
		return nil
	}
	program, err := parser.ParseFile(nil, "", "(async function(){\n"+source+"\n})", 0, parser.WithDisableSourceMaps)
	if err != nil || len(program.Body) != 1 {
		return nil
	}
	stmt, ok := program.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return nil
	}
	fn, ok := stmt.Expression.(*ast.FunctionLiteral)
	if !ok || !fn.Async || desktopWorldShadowsApp(fn) {
		return nil
	}

	bindings := make(map[string]desktopWorldBinding)
	var reads, inputs []desktopWorldAction
	record := func(expr ast.Expression) (desktopWorldBinding, bool) {
		if desktopWorldLiteral(expr, nil) {
			return desktopWorldBinding{literal: expr}, true
		}
		if desktopWorldReadPath(expr, bindings) {
			return desktopWorldBinding{observation: true}, true
		}
		action, ok := desktopWorldCallAction(expr, bindings)
		if !ok {
			return desktopWorldBinding{}, false
		}
		actions := &inputs
		if action.read {
			actions = &reads
		}
		if len(*actions) < 2 && !slices.Contains(*actions, action) {
			*actions = append(*actions, action)
		}
		return desktopWorldBinding{observation: action.read}, true
	}
scan:
	for _, stmt := range fn.Body.List {
		switch node := stmt.(type) {
		case *ast.EmptyStatement:
		case *ast.ExpressionStatement:
			if _, ok := record(node.Expression); !ok {
				break scan
			}
		case *ast.ReturnStatement:
			record(node.Argument)
			break scan
		case *ast.LexicalDeclaration:
			if node.Token != token.CONST {
				break scan
			}
			for _, binding := range node.List {
				id, ok := binding.Target.(*ast.Identifier)
				if !ok {
					break scan
				}
				name := id.Name.String()
				if _, exists := bindings[name]; exists {
					break scan
				}
				value, ok := record(binding.Initializer)
				if !ok {
					break scan
				}
				bindings[name] = value
			}
		case *ast.IfStatement:
			if test, ok := node.Test.(*ast.BooleanLiteral); ok && !test.Value &&
				node.Alternate == nil && desktopWorldDeadStatements(node.Consequent) {
				continue
			}
			break scan
		default:
			break scan
		}
	}
	if len(inputs) > 0 {
		return inputs
	}
	return reads
}

func desktopWorldShadowsApp(fn *ast.FunctionLiteral) bool {
	shadows := func(list []*ast.Binding) bool {
		for _, binding := range list {
			id, ok := binding.Target.(*ast.Identifier)
			if !ok || id.Name == "app" {
				return true
			}
		}
		return false
	}
	for _, decl := range fn.DeclarationList {
		if shadows(decl.List) {
			return true
		}
	}
	for _, stmt := range fn.Body.List {
		switch decl := stmt.(type) {
		case *ast.LexicalDeclaration:
			if shadows(decl.List) {
				return true
			}
		case *ast.FunctionDeclaration:
			if decl.Function.Name.Name == "app" {
				return true
			}
		case *ast.ClassDeclaration:
			if decl.Class.Name.Name == "app" {
				return true
			}
		}
	}
	return false
}

// A literal false test skips these statements without hiding declarations.
func desktopWorldDeadStatements(stmt ast.Statement) bool {
	switch node := stmt.(type) {
	case *ast.EmptyStatement, *ast.ExpressionStatement:
		return true
	case *ast.BlockStatement:
		for _, stmt := range node.List {
			if !desktopWorldDeadStatements(stmt) {
				return false
			}
		}
		return true
	}
	return false
}

func desktopWorldReadPath(expr ast.Expression, bindings map[string]desktopWorldBinding) bool {
	switch node := expr.(type) {
	case *ast.Identifier:
		return bindings[node.Name.String()].observation
	case *ast.DotExpression:
		return desktopWorldReadPath(node.Left, bindings)
	case *ast.BracketExpression:
		switch node.Member.(type) {
		case *ast.NumberLiteral, *ast.StringLiteral:
			return desktopWorldReadPath(node.Left, bindings)
		}
	}
	return false
}

func desktopWorldCallAction(expr ast.Expression, bindings map[string]desktopWorldBinding) (desktopWorldAction, bool) {
	if awaited, ok := expr.(*ast.AwaitExpression); ok {
		expr = awaited.Argument
	}
	call, ok := expr.(*ast.CallExpression)
	if !ok || len(call.ArgumentList) > 1 {
		return desktopWorldAction{}, false
	}
	var method string
	var receiver ast.Expression
	switch member := call.Callee.(type) {
	case *ast.DotExpression:
		receiver, method = member.Left, member.Identifier.Name.String()
	case *ast.BracketExpression:
		key, ok := member.Member.(*ast.StringLiteral)
		if !ok {
			return desktopWorldAction{}, false
		}
		receiver, method = member.Left, key.Value.String()
	}
	id, app := receiver.(*ast.Identifier)
	action, known := desktopWorldActions[method]
	if !app || id.Name != "app" || !known {
		return desktopWorldAction{}, false
	}
	if len(call.ArgumentList) == 1 {
		arg := call.ArgumentList[0]
		var reference ast.Expression
		var chord string
		switch method {
		case "keypress":
			if key, ok := desktopWorldKeys(arg).(*ast.Identifier); ok {
				if array, ok := bindings[key.Name.String()].literal.(*ast.ArrayLiteral); ok {
					reference, chord = key, desktopWorldChordKeys(array)
				}
			}
		case "readText":
			target := desktopWorldField(arg, "target")
			if desktopWorldReadPath(target, bindings) {
				reference = target
			}
		}
		if !desktopWorldLiteral(arg, reference) {
			return desktopWorldAction{}, false
		}
		action.phrase = desktopWorldActionPhrase(method, action.phrase, arg)
		if chord != "" {
			action.phrase = desktopWorldKeypressPhrase(action.phrase, chord)
		}
	}
	return action, true
}

// One validated read path or chord reference can stand in for a literal
// argument. Other expressions, including calls, are left opaque.
func desktopWorldLiteral(expr, reference ast.Expression) bool {
	if reference != nil && expr == reference {
		return true
	}
	switch node := expr.(type) {
	case *ast.StringLiteral, *ast.BooleanLiteral, *ast.NumberLiteral, *ast.NullLiteral, *ast.RegExpLiteral:
		return true
	case *ast.TemplateLiteral:
		return node.Tag == nil && len(node.Expressions) == 0
	case *ast.ArrayLiteral:
		for _, item := range node.Value {
			if !desktopWorldLiteral(item, reference) {
				return false
			}
		}
		return true
	case *ast.ObjectLiteral:
		for _, property := range node.Value {
			keyed, ok := property.(*ast.PropertyKeyed)
			if !ok || keyed.Computed || keyed.Kind != ast.PropertyKindValue || !desktopWorldLiteral(keyed.Value, reference) {
				return false
			}
		}
		return true
	}
	return false
}

func desktopWorldActionPhrase(method, phrase string, arg ast.Expression) string {
	switch method {
	case "keypress":
		keys := desktopWorldKeys(arg)
		if array, ok := keys.(*ast.ArrayLiteral); ok {
			return desktopWorldKeypressPhrase(phrase, desktopWorldChordKeys(array))
		}
		return desktopWorldKeypressPhrase(phrase, desktopWorldString(keys))
	case "click", "setValue", "look", "readText", "typeText":
		target := desktopWorldString(desktopWorldField(arg, "target"))
		if method == "click" || method == "setValue" {
			target = firstNonEmpty(target, desktopWorldString(desktopWorldField(desktopWorldField(arg, "selector"), "text")))
		}
		if method == "click" || method == "readText" {
			target = firstNonEmpty(target, desktopWorldString(arg))
		}
		if method == "look" && target == "" {
			if filter := desktopWorldString(desktopWorldField(arg, "text")); filter != "" {
				return "look for “" + filter + "”"
			}
			if scope := desktopWorldString(desktopWorldField(arg, "scope")); scope == "menu" {
				return "inspect the menu"
			}
			if role := desktopWorldString(desktopWorldField(arg, "role")); role != "" {
				return "look for " + strings.ReplaceAll(role, "_", " ") + " controls"
			}
		}
		if target != "" {
			quoted := "“" + target + "”"
			window, element, _ := strings.Cut(target, ".e")
			if strings.HasPrefix(window, "w") && desktopWorldDigits(window[1:]) && desktopWorldDigits(element) {
				quoted = "the observed control"
			}
			switch method {
			case "look":
				return "inspect " + quoted
			case "setValue":
				return "set the value of " + quoted
			case "readText":
				return "read text from " + quoted
			case "typeText":
				return "type into " + quoted
			default:
				return "click " + quoted
			}
		}
	}
	return phrase
}

func desktopWorldKeys(arg ast.Expression) ast.Expression {
	if keys := desktopWorldField(arg, "keys"); keys != nil {
		return keys
	}
	return arg
}

func desktopWorldChordKeys(array *ast.ArrayLiteral) string {
	var chord []string
	for _, item := range array.Value {
		key := desktopWorldString(item)
		if key == "" {
			return ""
		}
		chord = append(chord, key)
	}
	return strings.Join(chord, "+")
}

func desktopWorldKeypressPhrase(fallback, chord string) string {
	const keys = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-"
	if chord == "" || len(chord) > 32 || strings.IndexFunc(chord, func(r rune) bool {
		return !strings.ContainsRune(keys, r)
	}) >= 0 {
		return fallback
	}
	return "press " + chord
}

func desktopWorldDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) == -1
}

func desktopWorldString(expr ast.Expression) string {
	switch literal := expr.(type) {
	case *ast.StringLiteral:
		return desktopWorldInline(literal.Value.String())
	case *ast.TemplateLiteral:
		if literal.Tag == nil && len(literal.Expressions) == 0 && len(literal.Elements) == 1 {
			return desktopWorldInline(literal.Elements[0].Parsed.String())
		}
	}
	return ""
}

func desktopWorldField(expr ast.Expression, name string) ast.Expression {
	object, ok := expr.(*ast.ObjectLiteral)
	if !ok {
		return nil
	}
	var selected ast.Expression
	for _, property := range object.Value {
		keyed, ok := property.(*ast.PropertyKeyed)
		// Spreads, computed keys and accessors can overwrite a literal.
		if !ok || keyed.Computed || keyed.Kind != ast.PropertyKindValue {
			return nil
		}
		if key, ok := keyed.Key.(*ast.StringLiteral); ok && key.Value.String() == name {
			if selected != nil {
				return nil
			}
			selected = keyed.Value
		}
	}
	return selected
}
