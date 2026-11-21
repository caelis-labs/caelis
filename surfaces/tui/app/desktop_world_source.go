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

// Summarize the intent of a small straight-line subset of Desktop World
// scripts. An unsupported statement or argument leaves the script opaque.
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

	var actions []desktopWorldAction
	returned := false
	for _, stmt := range fn.Body.List {
		var expressions []ast.Expression
		ends := false
		switch node := stmt.(type) {
		case *ast.EmptyStatement:
			continue
		case *ast.ExpressionStatement:
			expressions = append(expressions, node.Expression)
		case *ast.ReturnStatement:
			if node.Argument != nil {
				expressions = append(expressions, node.Argument)
			}
			ends = true
		case *ast.LexicalDeclaration:
			if node.Token != token.CONST {
				return nil
			}
			for _, binding := range node.List {
				id, ok := binding.Target.(*ast.Identifier)
				if !ok || id.Name == "app" {
					return nil
				}
				expressions = append(expressions, binding.Initializer)
			}
		default:
			return nil
		}
		for _, expr := range expressions {
			if desktopWorldLiteral(expr) {
				continue
			}
			action, ok := desktopWorldCallAction(expr)
			if !ok {
				return nil
			}
			// Validate the rest of the body too, since declarations can
			// affect the app binding before a return.
			if !returned {
				actions = append(actions, action)
			}
		}
		returned = returned || ends
	}

	readsOnly := !slices.ContainsFunc(actions, func(action desktopWorldAction) bool { return !action.read })
	var chosen []desktopWorldAction
	for _, action := range actions {
		if action.read == readsOnly && !slices.Contains(chosen, action) {
			chosen = append(chosen, action)
			if len(chosen) == 2 {
				break
			}
		}
	}
	return chosen
}

func desktopWorldCallAction(expr ast.Expression) (desktopWorldAction, bool) {
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
		if !desktopWorldLiteral(arg) {
			return desktopWorldAction{}, false
		}
		action.phrase = desktopWorldActionPhrase(method, action.phrase, arg)
	}
	return action, true
}

func desktopWorldLiteral(expr ast.Expression) bool {
	switch node := expr.(type) {
	case *ast.StringLiteral, *ast.BooleanLiteral, *ast.NumberLiteral, *ast.NullLiteral, *ast.RegExpLiteral:
		return true
	case *ast.TemplateLiteral:
		return node.Tag == nil && len(node.Expressions) == 0
	case *ast.ArrayLiteral:
		for _, item := range node.Value {
			if !desktopWorldLiteral(item) {
				return false
			}
		}
		return true
	case *ast.ObjectLiteral:
		for _, property := range node.Value {
			keyed, ok := property.(*ast.PropertyKeyed)
			if !ok || keyed.Computed || keyed.Kind != ast.PropertyKindValue || !desktopWorldLiteral(keyed.Value) {
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
