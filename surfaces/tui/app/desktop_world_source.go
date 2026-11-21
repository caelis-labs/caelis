package tuiapp

import (
	"reflect"
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

type desktopWorldAction struct {
	phrase      string
	short       string
	read        bool
	conditional bool
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

// Desktop World accepts an async function body. Parse it without loading source
// maps or running code. Keep direct, identifiable calls in written order.
func desktopWorldScriptActions(source string) []desktopWorldAction {
	if len(source) == 0 || len(source) > 64<<10 {
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

	type site struct {
		position file.Idx
		action   desktopWorldAction
	}
	var sites []site
	receivers := make(map[*ast.Identifier]bool)
	reliable := walkDesktopWorldAST(fn.Body, false, false, func(node ast.Node, conditional, inactive bool) bool {
		switch expr := node.(type) {
		case *ast.CallExpression:
			receiver, method := desktopWorldCall(expr.Callee)
			if receiver == nil || receiver.Name != "app" {
				return true
			}
			receivers[receiver] = true
			if inactive {
				return true
			}
			action, ok := desktopWorldActions[method]
			if !ok {
				return false
			}
			action.conditional = conditional
			if len(expr.ArgumentList) == 1 {
				action.phrase = desktopWorldActionPhrase(method, action.phrase, expr.ArgumentList[0])
			}
			sites = append(sites, site{position: expr.Idx0(), action: action})
		case *ast.Identifier:
			// A local binding, alias or assignment can change which app
			// object these syntactically direct calls refer to.
			return expr.Name != "app" || receivers[expr]
		case *ast.PropertyShort:
			return expr.Name.Name != "app"
		case *ast.WithStatement:
			return false
		}
		return true
	})
	if !reliable {
		return nil
	}
	slices.SortStableFunc(sites, func(a, b site) int { return int(a.position - b.position) })
	readsOnly := !slices.ContainsFunc(sites, func(s site) bool { return !s.action.read })
	var actions []desktopWorldAction
	for _, s := range sites {
		if s.action.read != readsOnly || slices.Contains(actions, s.action) {
			continue
		}
		actions = append(actions, s.action)
		if len(actions) == 2 {
			break
		}
	}
	return actions
}

// Goja exposes its AST as node fields without a visitor. DeclarationList
// indexes nodes already visited in the body and is skipped.
func walkDesktopWorldAST(node ast.Node, conditional, inactive bool, visit func(ast.Node, bool, bool) bool) bool {
	value := reflect.ValueOf(node)
	if node == nil || value.IsNil() {
		return true
	}
	switch expr := node.(type) {
	case *ast.IfStatement:
		if literal, ok := expr.Test.(*ast.BooleanLiteral); ok {
			// Visit dead branches for potential hoisted app bindings, while
			// excluding their action sites from the summary.
			return walkDesktopWorldAST(expr.Consequent, conditional, inactive || !literal.Value, visit) &&
				walkDesktopWorldAST(expr.Alternate, conditional, inactive || literal.Value, visit)
		}
		conditional = true
	case *ast.SwitchStatement, *ast.ConditionalExpression,
		*ast.ForStatement, *ast.ForInStatement, *ast.ForOfStatement, *ast.WhileStatement, *ast.DoWhileStatement,
		*ast.CatchStatement, *ast.FunctionLiteral, *ast.ArrowFunctionLiteral, *ast.OptionalChain:
		conditional = true
	case *ast.BinaryExpression:
		conditional = conditional || expr.Operator == token.LOGICAL_AND || expr.Operator == token.LOGICAL_OR || expr.Operator == token.COALESCE
	}
	if !visit(node, conditional, inactive) {
		return false
	}
	value = value.Elem()
	for i := 0; i < value.NumField(); i++ {
		if value.Type().Field(i).Name == "DeclarationList" {
			continue
		}
		field := value.Field(i)
		if field.Kind() == reflect.Slice {
			for j := 0; j < field.Len(); j++ {
				if child, ok := field.Index(j).Interface().(ast.Node); ok && !walkDesktopWorldAST(child, conditional, inactive, visit) {
					return false
				}
			}
		} else if child, ok := field.Interface().(ast.Node); ok && !walkDesktopWorldAST(child, conditional, inactive, visit) {
			return false
		}
	}
	return true
}

func desktopWorldCall(callee ast.Expression) (*ast.Identifier, string) {
	if optional, ok := callee.(*ast.Optional); ok {
		callee = optional.Expression
	}
	var receiver ast.Expression
	var method string
	switch member := callee.(type) {
	case *ast.DotExpression:
		receiver, method = member.Left, member.Identifier.Name.String()
	case *ast.BracketExpression:
		receiver, method = member.Left, desktopWorldString(member.Member)
	}
	if optional, ok := receiver.(*ast.Optional); ok {
		receiver = optional.Expression
	}
	identifier, _ := receiver.(*ast.Identifier)
	return identifier, method
}

func desktopWorldActionPhrase(method, phrase string, arg ast.Expression) string {
	switch method {
	case "keypress":
		keys := desktopWorldField(arg, "keys")
		if keys == nil {
			keys = arg
		}
		if array, ok := keys.(*ast.ArrayLiteral); ok {
			var chord []string
			for _, item := range array.Value {
				chord = append(chord, desktopWorldString(item))
			}
			if len(chord) == 0 || slices.Contains(chord, "") {
				return phrase
			}
			return desktopWorldKeypressPhrase(phrase, strings.Join(chord, "+"))
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
