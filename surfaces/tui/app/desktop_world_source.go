package tuiapp

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

const desktopWorldScriptPrefix = "(async function(){\n"

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
	body := parseDesktopWorldScript(source)
	if body == nil {
		return nil
	}

	type site struct {
		position file.Idx
		action   desktopWorldAction
		chord    *ast.Identifier
	}
	var sites []site
	chords := desktopWorldLiteralChords(body)
	receivers := make(map[*ast.Identifier]bool)
	keyArgs := make(map[*ast.Identifier]bool)
	reliable := walkDesktopWorldAST(body, false, false, func(node ast.Node, conditional, inactive bool) bool {
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
			s := site{position: expr.Idx0(), action: action}
			if len(expr.ArgumentList) == 1 {
				arg := expr.ArgumentList[0]
				s.action.phrase = desktopWorldActionPhrase(method, action.phrase, arg)
				if method == "keypress" {
					s.chord, _ = desktopWorldKeys(arg).(*ast.Identifier)
					if s.chord != nil {
						keyArgs[s.chord] = true
					}
				}
			}
			sites = append(sites, s)
		case *ast.Identifier:
			// Any other use can shadow, mutate or expose the array.
			name := expr.Name.String()
			if chord, ok := chords[name]; ok && chord.declaration != expr && !keyArgs[expr] {
				delete(chords, name)
			}
			return expr.Name != "app" || receivers[expr]
		case *ast.PropertyShort:
			delete(chords, expr.Name.Name.String())
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
	actions := make([]desktopWorldAction, 0, len(sites))
	for _, s := range sites {
		if s.chord != nil {
			if chord, ok := chords[s.chord.Name.String()]; ok && chord.declaration.Idx0() < s.position {
				s.action.phrase = desktopWorldKeypressPhrase(s.action.phrase, chord.keys)
			}
		}
		actions = append(actions, s.action)
	}
	return actions
}

func parseDesktopWorldScript(source string) *ast.BlockStatement {
	if len(source) == 0 || len(source) > 64<<10 {
		return nil
	}
	program, err := parser.ParseFile(nil, "", desktopWorldScriptPrefix+source+"\n})", 0, parser.WithDisableSourceMaps)
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

	return fn.Body
}

// A sole, directly returned invocation with matching literal arguments can
// carry the same result as its child. Leave other Code Mode scripts opaque.
func desktopWorldReturnsCall(source, name string, input map[string]any) bool {
	body := parseDesktopWorldScript(source)
	if body == nil || len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStatement)
	if !ok {
		return false
	}
	expr := ret.Argument
	if awaited, ok := expr.(*ast.AwaitExpression); ok {
		expr = awaited.Argument
	}
	call, ok := expr.(*ast.CallExpression)
	if !ok || len(call.ArgumentList) != 1 {
		return false
	}
	callee, ok := call.Callee.(*ast.Identifier)
	object, literal := call.ArgumentList[0].(*ast.ObjectLiteral)
	if !ok || callee.Name.String() != name || !literal {
		return false
	}
	start := int(object.Idx0()) - len(desktopWorldScriptPrefix) - 1
	end := int(object.Idx1()) - len(desktopWorldScriptPrefix) - 1
	if start < 0 || end > len(source) || start >= end {
		return false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(source[start:end]), &args); err != nil {
		return false
	}
	got, err := json.Marshal(args)
	if err != nil {
		return false
	}
	want, err := json.Marshal(input)
	return err == nil && bytes.Equal(got, want)
}

type desktopWorldChord struct {
	declaration *ast.Identifier
	keys        string
}

func desktopWorldLiteralChords(body *ast.BlockStatement) map[string]desktopWorldChord {
	chords := make(map[string]desktopWorldChord)
	for _, stmt := range body.List {
		decl, ok := stmt.(*ast.LexicalDeclaration)
		if !ok || decl.Token != token.CONST {
			continue
		}
		for _, binding := range decl.List {
			id, named := binding.Target.(*ast.Identifier)
			array, literal := binding.Initializer.(*ast.ArrayLiteral)
			if named && literal {
				chords[id.Name.String()] = desktopWorldChord{id, desktopWorldChordKeys(array)}
			}
		}
	}
	return chords
}

// Goja exposes its AST as node fields without a visitor. DeclarationList
// indexes nodes already visited in the body and is skipped.
func walkDesktopWorldAST(node ast.Node, conditional, inactive bool, visit func(ast.Node, bool, bool) bool) bool {
	value := reflect.ValueOf(node)
	if node == nil || value.IsNil() {
		return true
	}
	switch expr := node.(type) {
	case *ast.BlockStatement:
		for _, stmt := range expr.List {
			if !walkDesktopWorldAST(stmt, conditional, inactive, visit) {
				return false
			}
			// Keep visiting unreachable declarations because they can
			// still shadow app or a key binding.
			inactive = inactive || desktopWorldStopsBlock(stmt)
		}
		return true
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

func desktopWorldStopsBlock(stmt ast.Statement) bool {
	switch node := stmt.(type) {
	case *ast.ReturnStatement, *ast.ThrowStatement:
		return true
	case *ast.BlockStatement:
		return slices.ContainsFunc(node.List, desktopWorldStopsBlock)
	case *ast.TryStatement:
		return node.Finally != nil && desktopWorldStopsBlock(node.Finally) ||
			desktopWorldStopsBlock(node.Body) && (node.Catch == nil || desktopWorldStopsBlock(node.Catch.Body))
	case *ast.IfStatement:
		if literal, ok := node.Test.(*ast.BooleanLiteral); ok {
			if literal.Value {
				return desktopWorldStopsBlock(node.Consequent)
			}
			return desktopWorldStopsBlock(node.Alternate)
		}
		return desktopWorldStopsBlock(node.Consequent) && desktopWorldStopsBlock(node.Alternate)
	}
	return false
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
