package gojaexec

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dop251/goja"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/google/uuid"
)

type declaration struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type scriptSource struct {
	code         string
	declarations []declaration
	hoists       []string
}

// prepareSource keeps top-level bindings in cells shared with their closures.
// Blocks and nested functions retain their original lexical scope. Only the
// execution/return boundary is async; it is not the lifetime of these cells.
func prepareSource(name, source string) (scriptSource, error) {
	const prefix = "(async function(){\"use strict\";"
	wrapped := prefix + source + "\n})()"
	program, err := parser.ParseFile(nil, name, wrapped, 0)
	if err != nil {
		return scriptSource{}, err
	}
	if _, err = goja.CompileAST(program, true); err != nil {
		return scriptSource{}, err
	}
	fn := program.Body[0].(*ast.ExpressionStatement).Expression.(*ast.CallExpression).Callee.(*ast.FunctionLiteral)
	result := scriptSource{declarations: []declaration{}}
	helper := "__publish_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var code strings.Builder
	cursor := len(prefix)
	into := map[*ast.Binding]bool{}
	collectForIntoVars(fn.Body, into)
	bytes := []byte(wrapped)
	for _, decl := range fn.DeclarationList {
		for _, binding := range decl.List {
			for _, name := range bindingNames(binding.Target) {
				result.declarations = append(result.declarations, declaration{name, "var"})
			}
		}
		// An expression-leading comma keeps destructuring assignments unambiguous.
		// Loop assignment targets cannot contain that comma. Keep offsets unchanged.
		replacement := "0, "
		if len(decl.List) == 1 && into[decl.List[0]] {
			replacement = "   "
		}
		copy(bytes[int(decl.Var)-1:], replacement)
	}
	wrapped = string(bytes)
	publish := func(names []string, kind string) string {
		var out strings.Builder
		for _, name := range names {
			q, _ := json.Marshal(name)
			fmt.Fprintf(&out, "%s(%s,()=>%s,%s_value=>%s=%s_value);", helper, q, name, helper, name, helper)
			result.declarations = append(result.declarations, declaration{name, kind})
		}
		return out.String()
	}
	for index, statement := range fn.Body.List {
		start, end := int(statement.Idx0())-1, len(prefix)+len(source)+1
		if index+1 < len(fn.Body.List) {
			end = int(fn.Body.List[index+1].Idx0()) - 1
		}
		if start < len(prefix) {
			continue
		}
		code.WriteString(wrapped[cursor:start])
		text := wrapped[start:end]
		switch one := statement.(type) {
		case *ast.LexicalDeclaration:
			var names []string
			for _, binding := range one.List {
				names = append(names, bindingNames(binding.Target)...)
			}
			text = "{" + text + ";" + publish(names, one.Token.String()) + "}"
		case *ast.ClassDeclaration:
			text = "{" + text + ";" + publish([]string{one.Class.Name.Name.String()}, "let") + "}"
		case *ast.FunctionDeclaration:
			name := one.Function.Name.Name.String()
			result.declarations = append(result.declarations, declaration{name, "var"})
			// Function declarations are available before the first statement, including
			// before a top-level await. Compile separately to preserve source line names.
			functionText := wrapped[start : int(one.Idx1())-1]
			nameStart, nameEnd := int(one.Function.Name.Idx0())-1-start, int(one.Function.Name.Idx1())-1-start
			anonymous := functionText[:nameStart] + strings.Repeat(" ", nameEnd-nameStart) + functionText[nameEnd:]
			result.hoists = append(result.hoists, strings.Repeat("\n", strings.Count(wrapped[len(prefix):start], "\n"))+name+"=("+anonymous+");")
			text = strings.Map(func(r rune) rune {
				if r == '\n' {
					return r
				}
				return ' '
			}, text)
		}
		code.WriteString(text)
		cursor = end
	}
	if cursor < len(prefix)+len(source) {
		code.WriteString(wrapped[cursor : len(prefix)+len(source)])
	}
	result.code = "(async function(" + helper + "){\"use strict\";" + code.String() + "\n})"
	return result, nil
}

func bindingNames(expression ast.Expression) []string {
	switch one := expression.(type) {
	case *ast.Identifier:
		return []string{one.Name.String()}
	case *ast.Binding:
		return bindingNames(one.Target)
	case *ast.AssignExpression:
		return bindingNames(one.Left)
	case *ast.ArrayPattern:
		var names []string
		for _, item := range one.Elements {
			names = append(names, bindingNames(item)...)
		}
		return append(names, bindingNames(one.Rest)...)
	case *ast.ObjectPattern:
		var names []string
		for _, item := range one.Properties {
			switch p := item.(type) {
			case *ast.PropertyShort:
				names = append(names, p.Name.Name.String())
			case *ast.PropertyKeyed:
				names = append(names, bindingNames(p.Value)...)
			}
		}
		return append(names, bindingNames(one.Rest)...)
	}
	return nil
}

// Only var declarations owned by this script become shared cells. Loop targets
// use assignment patterns rather than parenthesized expression initializers.
func collectForIntoVars(statement ast.Statement, into map[*ast.Binding]bool) {
	if statement == nil {
		return
	}
	walk := func(one ast.Statement) { collectForIntoVars(one, into) }
	switch one := statement.(type) {
	case *ast.BlockStatement:
		for _, child := range one.List {
			walk(child)
		}
	case *ast.ForInStatement:
		if target, ok := one.Into.(*ast.ForIntoVar); ok {
			into[target.Binding] = true
		}
		walk(one.Body)
	case *ast.ForOfStatement:
		if target, ok := one.Into.(*ast.ForIntoVar); ok {
			into[target.Binding] = true
		}
		walk(one.Body)
	case *ast.ForStatement:
		walk(one.Body)
	case *ast.WhileStatement:
		walk(one.Body)
	case *ast.DoWhileStatement:
		walk(one.Body)
	case *ast.IfStatement:
		walk(one.Consequent)
		walk(one.Alternate)
	case *ast.LabelledStatement:
		walk(one.Statement)
	case *ast.SwitchStatement:
		for _, branch := range one.Body {
			for _, child := range branch.Consequent {
				walk(child)
			}
		}
	case *ast.TryStatement:
		walk(one.Body)
		if one.Catch != nil {
			walk(one.Catch.Body)
		}
		if one.Finally != nil {
			walk(one.Finally)
		}
	}
}
