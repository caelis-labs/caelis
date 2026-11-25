package gojaexec

import (
	"fmt"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
	"github.com/dop251/goja/unistring"
	"github.com/google/uuid"
)

type scriptSource struct {
	program *goja.Program
	names   []string
}

// Keep the original function's lexical scope, hoisting, TDZ and diagnostics.
// Instrument the parsed tree rather than splitting JS text or replacing local
// bindings with globals. The strict outer function hides the publisher from
// the user's arguments and hands the worker a fresh async function body.
func prepareSource(name, source string) (scriptSource, error) {
	helper := "__publish_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	prefix := "(function(" + helper + "){\"use strict\";return async function(){"
	wrapped := prefix + source + "\n};})"
	program, err := parser.ParseFile(nil, name, wrapped, 0)
	if err != nil {
		return scriptSource{}, err
	}
	invalid := fmt.Errorf("expected an async function body")
	if len(program.Body) != 1 {
		return scriptSource{}, invalid
	}
	statement, ok := program.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return scriptSource{}, invalid
	}
	factory, ok := statement.Expression.(*ast.FunctionLiteral)
	if !ok || len(factory.Body.List) != 2 || factory.Body.RightBrace != file.Idx(len(wrapped)-1) {
		return scriptSource{}, invalid
	}
	ret, ok := factory.Body.List[1].(*ast.ReturnStatement)
	if !ok {
		return scriptSource{}, invalid
	}
	fn, ok := ret.Argument.(*ast.FunctionLiteral)
	if !ok || fn.Body.RightBrace != file.Idx(len(prefix)+len(source)+2) {
		return scriptSource{}, invalid
	}
	b := publisher{helper: helper, marker: helper + "_initialized", seen: make(map[string]bool), names: []string{}}
	code := make([]ast.Statement, 0, len(fn.Body.List))
	hoisted := make(map[string]bool)
	for _, statement := range fn.Body.List {
		if d, ok := statement.(*ast.FunctionDeclaration); ok {
			n := d.Function.Name.Name.String()
			if !hoisted[n] {
				hoisted[n] = true
				code = append(code, b.publish(n, true, false, d.Idx0()))
			}
		}
	}
	for _, decl := range fn.DeclarationList {
		for _, slot := range decl.List {
			for _, n := range bindingNames(slot.Target) {
				if !hoisted[n] {
					hoisted[n] = true
					// Publish a new hoisted var immediately. An older name is
					// replaced only after a redeclaration actually executes.
					code = append(code, b.publish(n, true, true, slot.Idx0()))
				}
			}
		}
	}
	for _, stmt := range fn.Body.List {
		switch d := stmt.(type) {
		case *ast.LexicalDeclaration:
			for _, slot := range d.List {
				code = append(code, &ast.LexicalDeclaration{Idx: d.Idx, Token: d.Token, List: []*ast.Binding{slot}})
				for _, n := range bindingNames(slot.Target) {
					code = append(code, b.publish(n, d.Token != token.CONST, false, slot.Idx0()))
				}
			}
		case *ast.ClassDeclaration:
			code = append(code, d, b.publish(d.Class.Name.Name.String(), true, false, d.Idx0()))
		default:
			code = append(code, b.instrumentVars(stmt))
		}
	}
	fn.Body.List = code
	if b.marked {
		// The synthetic var only sequences publication after native var
		// initialization, including destructuring and for-loop initializers.
		at := fn.Body.LeftBrace
		fn.DeclarationList = append(fn.DeclarationList, &ast.VariableDeclaration{Var: at, List: []*ast.Binding{{Target: ident(b.marker, at)}}})
	}
	compiled, err := goja.CompileAST(program, true)
	if err != nil {
		return scriptSource{}, err
	}
	return scriptSource{program: compiled, names: b.names}, nil
}

type publisher struct {
	helper, marker string
	seen           map[string]bool
	names          []string
	marked         bool
}

func ident(name string, at file.Idx) *ast.Identifier {
	return &ast.Identifier{Name: unistring.NewFromString(name), Idx: at}
}

func (b *publisher) publish(name string, mutable, onlyIfMissing bool, at file.Idx) *ast.ExpressionStatement {
	if !b.seen[name] {
		b.seen[name] = true
		b.names = append(b.names, name)
	}
	arrow := func(body ast.Expression, params ...*ast.Binding) *ast.ArrowFunctionLiteral {
		return &ast.ArrowFunctionLiteral{
			Start: at, End: at, ParameterList: &ast.ParameterList{List: params},
			Body: &ast.ExpressionBody{Expression: body},
		}
	}
	var setter ast.Expression = &ast.NullLiteral{Idx: at}
	if mutable {
		param := b.helper + "_value"
		setter = arrow(&ast.AssignExpression{Operator: token.ASSIGN, Left: ident(name, at), Right: ident(param, at)},
			&ast.Binding{Target: ident(param, at)})
	}
	return &ast.ExpressionStatement{Expression: &ast.CallExpression{
		Callee: ident(b.helper, at), LeftParenthesis: at, RightParenthesis: at,
		ArgumentList: []ast.Expression{
			&ast.StringLiteral{Idx: at, Value: unistring.NewFromString(name)},
			arrow(ident(name, at)), setter, &ast.BooleanLiteral{Idx: at, Value: onlyIfMissing},
		},
	}}
}

func (b *publisher) publishVar(name string, at file.Idx) ast.Expression {
	return &ast.CallExpression{
		Callee: ident(b.helper, at), LeftParenthesis: at, RightParenthesis: at,
		ArgumentList: []ast.Expression{&ast.StringLiteral{Idx: at, Value: unistring.NewFromString(name)}},
	}
}

// A marker in the declaration list runs after that binding's initializer and
// before the next, without changing the initialization/destructuring itself.
func (b *publisher) varList(list []*ast.Binding) []*ast.Binding {
	var out []*ast.Binding
	for _, slot := range list {
		out = append(out, slot)
		var calls []ast.Expression
		for _, n := range bindingNames(slot.Target) {
			calls = append(calls, b.publishVar(n, slot.Idx0()))
		}
		if len(calls) != 0 {
			b.marked = true
			out = append(out, &ast.Binding{Target: ident(b.marker, slot.Idx0()), Initializer: &ast.SequenceExpression{Sequence: calls}})
		}
	}
	return out
}

func (b *publisher) loopBody(into ast.ForInto, body ast.Statement) ast.Statement {
	target, ok := into.(*ast.ForIntoVar)
	if !ok {
		return b.instrumentVars(body)
	}
	block := &ast.BlockStatement{LeftBrace: body.Idx0(), RightBrace: body.Idx1() - 1}
	for _, n := range bindingNames(target.Binding.Target) {
		block.List = append(block.List, &ast.ExpressionStatement{Expression: b.publishVar(n, target.Idx0())})
	}
	block.List = append(block.List, b.instrumentVars(body))
	return block
}

// Visit only statements in this function, never nested functions or classes.
func (b *publisher) instrumentVars(statement ast.Statement) ast.Statement {
	switch d := statement.(type) {
	case *ast.VariableStatement:
		d.List = b.varList(d.List)
	case *ast.BlockStatement:
		for i, child := range d.List {
			d.List[i] = b.instrumentVars(child)
		}
	case *ast.ForInStatement:
		d.Body = b.loopBody(d.Into, d.Body)
	case *ast.ForOfStatement:
		d.Body = b.loopBody(d.Into, d.Body)
	case *ast.ForStatement:
		if init, ok := d.Initializer.(*ast.ForLoopInitializerVarDeclList); ok {
			init.List = b.varList(init.List)
		}
		d.Body = b.instrumentVars(d.Body)
	case *ast.WhileStatement:
		d.Body = b.instrumentVars(d.Body)
	case *ast.DoWhileStatement:
		d.Body = b.instrumentVars(d.Body)
	case *ast.IfStatement:
		d.Consequent = b.instrumentVars(d.Consequent)
		d.Alternate = b.instrumentVars(d.Alternate)
	case *ast.LabelledStatement:
		d.Statement = b.instrumentVars(d.Statement)
	case *ast.SwitchStatement:
		for _, branch := range d.Body {
			for i, child := range branch.Consequent {
				branch.Consequent[i] = b.instrumentVars(child)
			}
		}
	case *ast.TryStatement:
		b.instrumentVars(d.Body)
		if d.Catch != nil {
			b.instrumentVars(d.Catch.Body)
		}
		if d.Finally != nil {
			b.instrumentVars(d.Finally)
		}
	}
	return statement
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
