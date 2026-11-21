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

// Summarize intent from straight-line input calls and read-only observations.
// Keep the two lists separate so mixed scripts only preview the input calls.
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
	if !ok || !fn.Async {
		return nil
	}

	bindings := make(map[string]desktopWorldBinding)
	var reads, inputs []desktopWorldAction
	returned, guardedRead := false, false
	record := func(expr ast.Expression) (desktopWorldBinding, bool) {
		if desktopWorldLiteral(expr, nil) {
			return desktopWorldBinding{literal: expr}, true
		}
		action, ok := desktopWorldCallAction(expr, bindings)
		if !ok || guardedRead && !action.read {
			return desktopWorldBinding{}, false
		}
		// Later declarations still need validation because they may shadow
		// an earlier app or constant reference.
		if !returned {
			actions := &inputs
			if action.read {
				actions = &reads
			}
			if len(*actions) < 2 && !slices.Contains(*actions, action) {
				*actions = append(*actions, action)
			}
		}
		return desktopWorldBinding{observation: action.read}, true
	}
	for _, stmt := range fn.Body.List {
		switch node := stmt.(type) {
		case *ast.EmptyStatement:
		case *ast.ExpressionStatement:
			if _, ok := record(node.Expression); !ok {
				return nil
			}
		case *ast.ReturnStatement:
			if !desktopWorldLiteralReturn(node.Argument, bindings) {
				if _, ok := record(node.Argument); !ok {
					return nil
				}
			}
			returned = true
		case *ast.LexicalDeclaration:
			if node.Token != token.CONST {
				return nil
			}
			for _, binding := range node.List {
				id, ok := binding.Target.(*ast.Identifier)
				if !ok || id.Name == "app" {
					return nil
				}
				name := id.Name.String()
				if _, exists := bindings[name]; exists {
					return nil
				}
				value, ok := record(binding.Initializer)
				if !ok {
					return nil
				}
				bindings[name] = value
			}
		case *ast.IfStatement:
			if test, ok := node.Test.(*ast.BooleanLiteral); ok && !test.Value &&
				node.Alternate == nil && desktopWorldDeadStatements(node.Consequent) {
				continue
			}
			if len(inputs) > 0 || !desktopWorldReadGuard(node, bindings) {
				return nil
			}
			guardedRead = true
		default:
			return nil
		}
	}
	if len(inputs) > 0 {
		return inputs
	}
	return reads
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

// A simple early exit based on a prior read can limit subsequent observations.
// Keep describing read intent without evaluating the predicate.
func desktopWorldReadGuard(node *ast.IfStatement, bindings map[string]desktopWorldBinding) bool {
	ret, returns := node.Consequent.(*ast.ReturnStatement)
	test, compares := node.Test.(*ast.BinaryExpression)
	if node.Alternate != nil || !returns || !compares ||
		(test.Operator != token.STRICT_EQUAL && test.Operator != token.STRICT_NOT_EQUAL) {
		return false
	}
	return (ret.Argument == nil || desktopWorldLiteral(ret.Argument, nil)) &&
		desktopWorldReadPath(test.Left, bindings) && desktopWorldLiteral(test.Right, nil)
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

func desktopWorldLiteralReturn(expr ast.Expression, bindings map[string]desktopWorldBinding) bool {
	if expr == nil || desktopWorldLiteral(expr, nil) {
		return true
	}
	object, ok := expr.(*ast.ObjectLiteral)
	if !ok {
		return false
	}
	for _, property := range object.Value {
		shorthand, ok := property.(*ast.PropertyShort)
		if !ok || shorthand.Initializer != nil || bindings[shorthand.Name.Name.String()].literal == nil {
			return false
		}
	}
	return true
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
