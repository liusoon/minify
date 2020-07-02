// Package js minifies ECMAScript5.1 following the specifications at http://www.ecma-international.org/ecma-262/5.1/.
package js

// TODO: remove dead code, such as in if (false) or statements after return statement, difficulty with var decls
// TODO: move var declaration or expr statement into for loop init (var only if for has var decl)
// TODO: don't minify variable names in with statement, what todo with eval? Don't minify any variable name?

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

var (
	spaceBytes     = []byte(" ")
	starBytes      = []byte("*")
	semicolonBytes = []byte(";")
)

// DefaultMinifier is the default minifier.
var DefaultMinifier = &Minifier{}

// Minifier is a JS minifier.
type Minifier struct {
	Precision     int // number of significant digits
	Deterministic bool
}

// Minify minifies JS data, it reads from r and writes to w.
func Minify(m *minify.M, w io.Writer, r io.Reader, params map[string]string) error {
	return DefaultMinifier.Minify(m, w, r, params)
}

// Minify minifies JS data, it reads from r and writes to w.
func (o *Minifier) Minify(_ *minify.M, w io.Writer, r io.Reader, _ map[string]string) error {
	z := parse.NewInput(r)
	ast, err := js.Parse(z)
	if err != nil {
		return err
	}

	m := &jsMinifier{
		o:       o,
		w:       w,
		src:     ast.Src,
		renamer: newRenamer(ast.Undeclared, o.Deterministic),
	}
	ast.List = m.mergeStmtList(ast.List)
	for _, item := range ast.List {
		m.writeSemicolon()
		m.minifyStmt(item)
	}

	definesBiggest := 0
	definesTotal := 0
	for _, n := range defines {
		definesTotal += n
		if definesBiggest < n {
			definesBiggest = n
		}
	}
	definesMean := float64(definesTotal) / float64(len(defines))
	definesStdDev := 0.0
	for _, n := range defines {
		definesStdDev += (float64(n) - definesMean) * (float64(n) - definesMean)
	}
	definesStdDev = math.Sqrt(definesStdDev / float64(len(defines)))

	fmt.Printf("# scopes: f=%d, b=%d (%d empty)\n", fscopes, bscopes, bescopes)
	fmt.Printf("# max depth: %d\n", maxdepth)
	fmt.Printf("# defines: %d (mean=%g, stddev=%g, biggest=%d)\n# undefines: %d\n", definesTotal, definesMean, definesStdDev, definesBiggest, len(undefines))
	fmt.Println("undefines =", undefines)

	if _, err := w.Write(nil); err != nil {
		return err
	}
	return nil
}

type jsMinifier struct {
	o *Minifier
	w io.Writer

	prev           []byte
	needsSemicolon bool
	needsSpace     bool
	src            js.Src
	renamer        *renamer
}

func (m *jsMinifier) write(b []byte) {
	if m.needsSpace && js.IsIdentifierStart(b) {
		m.w.Write(spaceBytes)
	}
	m.w.Write(b)
	m.prev = b
	m.needsSpace = false
}

func (m *jsMinifier) writeSpaceAfterIdent() {
	if js.IsIdentifierEnd(m.prev) || 1 < len(m.prev) && m.prev[0] == '/' {
		m.w.Write(spaceBytes)
	}
}

func (m *jsMinifier) writeSpaceBeforeIdent() {
	m.needsSpace = true
}

func (m *jsMinifier) requireSemicolon() {
	m.needsSemicolon = true
}

func (m *jsMinifier) writeSemicolon() {
	if m.needsSemicolon {
		m.w.Write(semicolonBytes)
		m.needsSemicolon = false
		m.needsSpace = false
	}
}

func (m *jsMinifier) minifyStmt(i js.IStmt) {
	i = m.stmtToExpr(i)

	switch stmt := i.(type) {
	case *js.ExprStmt:
		// prefix ! to function or group to class to remain expressions
		expr := stmt.Value
		commaExpr, ok := expr.(*js.BinaryExpr)
		for ok && commaExpr.Op == js.CommaToken {
			expr = commaExpr.X
			commaExpr, ok = expr.(*js.BinaryExpr)
		}
		if group, isGroup := expr.(*js.GroupExpr); isGroup {
			if _, isFunc := group.X.(*js.FuncDecl); isFunc {
				m.write([]byte("!"))
			} else if _, isClass := group.X.(*js.ClassDecl); isClass {
				m.write([]byte("!"))
			} else if call, isCall := group.X.(*js.CallExpr); isCall {
				if _, isFunc := call.X.(*js.FuncDecl); isFunc {
					m.write([]byte("!"))
				}
			}
		} else if call, isCall := expr.(*js.CallExpr); isCall {
			if group, isGroup := call.X.(*js.GroupExpr); isGroup {
				if _, isFunc := group.X.(*js.FuncDecl); isFunc {
					m.write([]byte("!"))
				}
			}
		}
		m.minifyExpr(stmt.Value, js.OpExpr)
		m.requireSemicolon()
	case *js.VarDecl:
		m.minifyVarDecl(*stmt)
		m.requireSemicolon()
	case *js.IfStmt:
		hasIf := !isEmptyStmt(stmt.Body)
		hasElse := !isEmptyStmt(stmt.Else)

		m.write([]byte("if("))
		m.minifyExpr(stmt.Cond, js.OpExpr)
		m.write([]byte(")"))

		if !hasIf && hasElse {
			m.requireSemicolon()
		} else if hasIf {
			if block, ok := stmt.Body.(*js.BlockStmt); ok && len(block.List) == 1 {
				stmt.Body = block.List[0]
			}
			if ifStmt, ok := stmt.Body.(*js.IfStmt); ok && isEmptyStmt(ifStmt.Else) {
				m.write([]byte("{"))
				m.minifyStmt(stmt.Body)
				m.write([]byte("}"))
				m.needsSemicolon = false
			} else {
				m.minifyStmt(stmt.Body)
			}
		}
		if hasElse {
			m.writeSemicolon()
			if !hasReturnThrowStmt(stmt.Body) {
				m.write([]byte("else"))
				m.writeSpaceBeforeIdent()
				m.minifyStmt(stmt.Else)
			} else if block, ok := stmt.Else.(*js.BlockStmt); ok {
				for _, item := range block.List {
					m.writeSemicolon()
					m.minifyStmt(item)
				}
			} else {
				m.minifyStmt(stmt.Else)
			}
		}
	case *js.BlockStmt:
		m.minifyBlockStmt(*stmt, true)
	case *js.ReturnStmt:
		m.write([]byte("return"))
		m.writeSpaceBeforeIdent()
		m.minifyExpr(stmt.Value, js.OpExpr)
		m.requireSemicolon()
	case *js.LabelledStmt:
		m.write(stmt.Ref.Data(m.src))
		m.write([]byte(":"))
		m.minifyStmt(stmt.Value)
	case *js.BranchStmt:
		m.write(stmt.Type.Bytes())
		name := stmt.Ref.Data(m.src)
		if name != nil {
			m.write([]byte(" "))
			m.write(name)
		}
		m.requireSemicolon()
	case *js.WithStmt:
		m.write([]byte("with("))
		m.minifyExpr(stmt.Cond, js.OpExpr)
		m.write([]byte(")"))
		m.minifyStmt(stmt.Body)
	case *js.DoWhileStmt:
		m.write([]byte("do"))
		m.writeSpaceBeforeIdent()
		m.minifyStmt(stmt.Body)
		m.writeSemicolon()
		m.write([]byte("while("))
		m.minifyExpr(stmt.Cond, js.OpExpr)
		m.write([]byte(")"))
		m.requireSemicolon()
	case *js.WhileStmt:
		m.write([]byte("while("))
		m.minifyExpr(stmt.Cond, js.OpExpr)
		m.write([]byte(")"))
		m.minifyStmt(stmt.Body)
	case *js.ForStmt:
		m.write([]byte("for("))
		m.minifyExpr(stmt.Init, js.OpExpr)
		m.write([]byte(";"))
		m.minifyExpr(stmt.Cond, js.OpExpr)
		m.write([]byte(";"))
		m.minifyExpr(stmt.Post, js.OpExpr)
		m.write([]byte(")"))
		m.minifyStmt(stmt.Body)
	case *js.ForInStmt:
		m.write([]byte("for("))
		m.minifyExpr(stmt.Init, js.OpLHS)
		m.writeSpaceAfterIdent()
		m.write([]byte("in"))
		m.writeSpaceBeforeIdent()
		m.minifyExpr(stmt.Value, js.OpExpr)
		m.write([]byte(")"))
		m.minifyStmt(stmt.Body)
	case *js.ForOfStmt:
		if stmt.Await {
			m.write([]byte("for await("))
		} else {
			m.write([]byte("for("))
		}
		m.minifyExpr(stmt.Init, js.OpLHS)
		m.writeSpaceAfterIdent()
		m.write([]byte("of"))
		m.writeSpaceBeforeIdent()
		m.minifyExpr(stmt.Value, js.OpAssign)
		m.write([]byte(")"))
		m.minifyStmt(stmt.Body)
	case *js.SwitchStmt:
		m.write([]byte("switch("))
		m.minifyExpr(stmt.Init, js.OpExpr)
		m.write([]byte("){"))
		m.needsSemicolon = false
		for _, clause := range stmt.List {
			m.writeSemicolon()
			m.write(clause.TokenType.Bytes())
			if clause.Cond != nil {
				m.write([]byte(" "))
				m.minifyExpr(clause.Cond, js.OpExpr)
			}
			m.write([]byte(":"))
			for _, item := range clause.List {
				m.writeSemicolon()
				m.minifyStmt(item)
			}
		}
		m.write([]byte("}"))
	case *js.ThrowStmt:
		m.write([]byte("throw"))
		m.writeSpaceBeforeIdent()
		m.minifyExpr(stmt.Value, js.OpExpr)
		m.requireSemicolon()
	case *js.TryStmt:
		m.write([]byte("try"))
		m.minifyBlockStmt(stmt.Body, true)
		if len(stmt.Catch.List) != 0 || stmt.Binding != nil {
			m.write([]byte("catch"))
			m.renamer.enterScope(stmt.Catch.Scope, true)
			if stmt.Binding != nil {
				m.write([]byte("("))
				m.minifyBinding(stmt.Binding)
				m.write([]byte(")"))
			}
			m.minifyBlockStmt(stmt.Catch, false)
			m.renamer.exitScope()
		}
		if len(stmt.Finally.List) != 0 {
			m.write([]byte("finally"))
			m.minifyBlockStmt(stmt.Finally, true)
		}
	case *js.FuncDecl:
		m.minifyFuncDecl(*stmt, false)
	case *js.ClassDecl:
		m.minifyClassDecl(*stmt)
	case *js.DebuggerStmt:
	case *js.EmptyStmt:
	case *js.ImportStmt:
		m.write([]byte("import"))
		if stmt.Default != nil {
			m.write([]byte(" "))
			m.write(stmt.Default)
			if len(stmt.List) != 0 {
				m.write([]byte(","))
			}
		}
		if len(stmt.List) == 1 {
			m.writeSpaceBeforeIdent()
			m.minifyAlias(stmt.List[0])
		} else if 1 < len(stmt.List) {
			m.write([]byte("{"))
			for i, item := range stmt.List {
				if i != 0 {
					m.write([]byte(","))
				}
				m.minifyAlias(item)
			}
			m.write([]byte("}"))
		}
		if stmt.Default != nil || len(stmt.List) != 0 {
			if len(stmt.List) < 2 {
				m.write([]byte(" "))
			}
			m.write([]byte("from"))
		}
		m.write(stmt.Module)
		m.requireSemicolon()
	case *js.ExportStmt:
		m.write([]byte("export"))
		if stmt.Decl != nil {
			if stmt.Default {
				m.write([]byte(" default"))
			}
			m.writeSpaceBeforeIdent()
			m.minifyExpr(stmt.Decl, js.OpAssign)
			_, isHoistable := stmt.Decl.(*js.FuncDecl)
			_, isClass := stmt.Decl.(*js.ClassDecl)
			if !isHoistable && !isClass {
				m.requireSemicolon()
			}
		} else {
			if len(stmt.List) == 1 {
				m.writeSpaceBeforeIdent()
				m.minifyAlias(stmt.List[0])
			} else if 1 < len(stmt.List) {
				m.write([]byte("{"))
				for i, item := range stmt.List {
					if i != 0 {
						m.write([]byte(","))
					}
					m.minifyAlias(item)
				}
				m.write([]byte("}"))
			}
			if stmt.Module != nil {
				if len(stmt.List) < 2 && (len(stmt.List) != 1 || isIdentEndAlias(stmt.List[0])) {
					m.write([]byte(" "))
				}
				m.write([]byte("from"))
				m.write(stmt.Module)
			}
			m.requireSemicolon()
		}
	}
}

func groupExpr(i js.IExpr, prec js.OpPrec) js.IExpr {
	if exprPrec(i) < prec {
		return &js.GroupExpr{i}
	}
	return i
}

func condExpr(x, y, z js.IExpr) js.IExpr {
	return &js.CondExpr{groupExpr(x, js.OpCoalesce), groupExpr(y, js.OpAssign), groupExpr(z, js.OpAssign)}
}

func (m *jsMinifier) stmtToExpr(i js.IStmt) js.IStmt {
	if stmt, ok := i.(*js.IfStmt); ok {
		if unaryExpr, ok := stmt.Cond.(*js.UnaryExpr); ok && unaryExpr.Op == js.NotToken {
			stmt.Cond = unaryExpr.X
			stmt.Body, stmt.Else = stmt.Else, stmt.Body
		}
		hasIf := !isEmptyStmt(stmt.Body)
		hasElse := !isEmptyStmt(stmt.Else)
		if !hasIf && !hasElse {
			return &js.ExprStmt{stmt.Cond}
		} else if hasIf && !hasElse {
			stmt.Body = m.stmtToExpr(stmt.Body)
			if X, isExprBody := stmt.Body.(*js.ExprStmt); isExprBody {
				left := groupExpr(stmt.Cond, binaryLeftPrecMap[js.AndToken])
				right := groupExpr(X.Value, binaryRightPrecMap[js.AndToken])
				return &js.ExprStmt{&js.BinaryExpr{js.AndToken, left, right}}
			}
		} else if !hasIf && hasElse {
			stmt.Else = m.stmtToExpr(stmt.Else)
			if X, isExprElse := stmt.Else.(*js.ExprStmt); isExprElse {
				left := groupExpr(stmt.Cond, binaryLeftPrecMap[js.OrToken])
				right := groupExpr(X.Value, binaryRightPrecMap[js.OrToken])
				return &js.ExprStmt{&js.BinaryExpr{js.OrToken, left, right}}
			}
		} else if hasIf && hasElse {
			stmt.Body = m.stmtToExpr(stmt.Body)
			stmt.Else = m.stmtToExpr(stmt.Else)
			XExpr, isExprBody := stmt.Body.(*js.ExprStmt)
			YExpr, isExprElse := stmt.Else.(*js.ExprStmt)
			if isExprBody && isExprElse {
				return &js.ExprStmt{condExpr(stmt.Cond, XExpr.Value, YExpr.Value)}
			}
			// TODO: enable
			//XReturn, isReturnBody := stmt.Body.(*js.ReturnStmt)
			//YReturn, isReturnElse := stmt.Else.(*js.ReturnStmt)
			//if isReturnBody && isReturnElse {
			//	if XReturn.Value == nil {
			//		XReturn.Value = &js.UnaryExpr{js.VoidToken, &js.LiteralExpr{js.NumericToken, []byte("0")}}
			//	}
			//	if YReturn.Value == nil {
			//		YReturn.Value = &js.UnaryExpr{js.VoidToken, &js.LiteralExpr{js.NumericToken, []byte("0")}}
			//	}
			//	return &js.ReturnStmt{condExpr(stmt.Cond, XReturn.Value, YReturn.Value)}
			//}
			XThrow, isThrowBody := stmt.Body.(*js.ThrowStmt)
			YThrow, isThrowElse := stmt.Else.(*js.ThrowStmt)
			if isThrowBody && isThrowElse {
				return &js.ThrowStmt{condExpr(stmt.Cond, XThrow.Value, YThrow.Value)}
			}
		}
	} else if stmt, ok := i.(*js.BlockStmt); ok {
		// merge body and remove braces if possible from independent blocks
		stmt.List = m.mergeStmtList(stmt.List)
		if len(stmt.List) == 1 {
			varDecl, isVarDecl := stmt.List[0].(*js.VarDecl)
			_, isClassDecl := stmt.List[0].(*js.ClassDecl)
			if !isClassDecl && (!isVarDecl || varDecl.TokenType == js.VarToken) {
				return m.stmtToExpr(stmt.List[0])
			}
		}
		return js.IStmt(stmt)
	}
	return i
}

func (m *jsMinifier) mergeStmtList(list []js.IStmt) []js.IStmt {
	if len(list) < 2 {
		return list
	}
	list[0] = m.stmtToExpr(list[0])
	j := 0
	for i, _ := range list[:len(list)-1] {
		list[i+1] = m.stmtToExpr(list[i+1])
		j++
		if left, ok := list[i].(*js.ExprStmt); ok {
			// merge expression statements with expression, return, and throw statements
			if right, ok := list[i+1].(*js.ExprStmt); ok {
				right.Value = &js.BinaryExpr{js.CommaToken, left.Value, right.Value}
				j--
			} else if returnStmt, ok := list[i+1].(*js.ReturnStmt); ok && returnStmt.Value != nil {
				returnStmt.Value = &js.BinaryExpr{js.CommaToken, left.Value, returnStmt.Value}
				j--
			} else if throwStmt, ok := list[i+1].(*js.ThrowStmt); ok {
				throwStmt.Value = &js.BinaryExpr{js.CommaToken, left.Value, throwStmt.Value}
				j--
			} else if forStmt, ok := list[i+1].(*js.ForStmt); ok {
				if forStmt.Init == nil {
					forStmt.Init = left.Value
					j--
				} else if _, ok := forStmt.Init.(*js.VarDecl); !ok {
					forStmt.Init = &js.BinaryExpr{js.CommaToken, left.Value, forStmt.Init}
					j--
				}
			} else if whileStmt, ok := list[i+1].(*js.WhileStmt); ok {
				list[i+1] = &js.ForStmt{left.Value, whileStmt.Cond, nil, whileStmt.Body}
				j--
			} else if switchStmt, ok := list[i+1].(*js.SwitchStmt); ok {
				switchStmt.Init = &js.BinaryExpr{js.CommaToken, left.Value, switchStmt.Init}
				j--
			} else if withStmt, ok := list[i+1].(*js.WithStmt); ok {
				withStmt.Cond = &js.BinaryExpr{js.CommaToken, left.Value, withStmt.Cond}
				j--
			} else if ifStmt, ok := list[i+1].(*js.IfStmt); ok {
				ifStmt.Cond = &js.BinaryExpr{js.CommaToken, left.Value, ifStmt.Cond}
				j--
			}
		} else if left, ok := list[i].(*js.VarDecl); ok {
			// merge var, const, let declarations
			if right, ok := list[i+1].(*js.VarDecl); ok && left.TokenType == right.TokenType {
				right.List = append(left.List, right.List...)
				j--
			} else if left.TokenType == js.VarToken {
				if forStmt, ok := list[i+1].(*js.ForStmt); ok {
					if init, ok := forStmt.Init.(*js.VarDecl); ok && init.TokenType == js.VarToken {
						init.List = append(left.List, init.List...)
						j--
					}
				} else if whileStmt, ok := list[i+1].(*js.WhileStmt); ok {
					list[i+1] = &js.ForStmt{left, whileStmt.Cond, nil, whileStmt.Body}
					j--
				}
			}
		}
		list[j] = list[i+1]
		if 0 < j {
			// merge if/else with return/throw when followed by return/throw
			if ifStmt, ok := list[j-1].(*js.IfStmt); ok && isEmptyStmt(ifStmt.Body) != isEmptyStmt(ifStmt.Else) {
				if returnStmt, ok := list[j].(*js.ReturnStmt); ok && returnStmt.Value != nil {
					if left, ok := ifStmt.Body.(*js.ReturnStmt); ok && left.Value != nil {
						returnStmt.Value = condExpr(ifStmt.Cond, left.Value, returnStmt.Value)
						list[j-1] = returnStmt
						j--
					} else if left, ok := ifStmt.Else.(*js.ReturnStmt); ok && left.Value != nil {
						returnStmt.Value = condExpr(ifStmt.Cond, returnStmt.Value, left.Value)
						list[j-1] = returnStmt
						j--
					}
				} else if throwStmt, ok := list[j].(*js.ThrowStmt); ok {
					if left, ok := ifStmt.Body.(*js.ThrowStmt); ok {
						throwStmt.Value = condExpr(ifStmt.Cond, left.Value, throwStmt.Value)
						list[j-1] = throwStmt
						j--
					} else if left, ok := ifStmt.Else.(*js.ThrowStmt); ok {
						throwStmt.Value = condExpr(ifStmt.Cond, throwStmt.Value, left.Value)
						list[j-1] = throwStmt
						j--
					}
				}
			}
		}
	}
	return list[:j+1]
}

func (m *jsMinifier) minifyBlockStmt(stmt js.BlockStmt, enterScope bool) {
	stmt.List = m.mergeStmtList(stmt.List)
	m.write([]byte("{"))
	m.needsSemicolon = false
	if enterScope {
		m.renamer.enterScope(stmt.Scope, true)
	}
	for _, item := range stmt.List {
		m.writeSemicolon()
		m.minifyStmt(item)
		if _, ok := item.(*js.ReturnStmt); ok {
			break
		} else if _, ok := item.(*js.BranchStmt); ok {
			break
		}
	}
	if enterScope {
		m.renamer.exitScope()
	}
	m.write([]byte("}"))
	m.needsSemicolon = false
}

func (m *jsMinifier) minifyAlias(alias js.Alias) {
	if alias.Name != nil {
		m.write(alias.Name)
		if !bytes.Equal(alias.Name, starBytes) {
			m.write([]byte(" "))
		}
		m.write([]byte("as "))
	}
	m.write(alias.Binding)
}

func (m *jsMinifier) minifyParams(params js.Params) {
	m.write([]byte("("))
	for i, item := range params.List {
		if i != 0 {
			m.write([]byte(","))
		}
		m.minifyBindingElement(item)
	}
	if params.Rest != nil {
		if len(params.List) != 0 {
			m.write([]byte(","))
		}
		m.write([]byte("..."))
		m.minifyBinding(params.Rest)
	}
	m.write([]byte(")"))
}

func (m *jsMinifier) minifyArguments(args js.Arguments) {
	m.write([]byte("("))
	for i, item := range args.List {
		if i != 0 {
			m.write([]byte(","))
		}
		m.minifyExpr(item, js.OpExpr)
	}
	if args.Rest != nil {
		if len(args.List) != 0 {
			m.write([]byte(","))
		}
		m.write([]byte("..."))
		m.minifyExpr(args.Rest, js.OpExpr)
	}
	m.write([]byte(")"))
}

func (m *jsMinifier) minifyVarDecl(decl js.VarDecl) {
	m.write(decl.TokenType.Bytes())
	m.writeSpaceBeforeIdent()
	for i, item := range decl.List {
		if i != 0 {
			m.write([]byte(","))
		}
		m.minifyBindingElement(item)
	}
}

func (m *jsMinifier) minifyFuncDecl(decl js.FuncDecl, inExpr bool) {
	if decl.Async {
		m.write([]byte("async "))
	}
	m.write([]byte("function"))
	if decl.Generator {
		m.write([]byte("*"))
	}
	if inExpr {
		m.renamer.enterScope(decl.Scope, false)
	}
	if decl.Name != nil {
		if !decl.Generator {
			m.write([]byte(" "))
		}
		m.write(m.renamer.rename(decl.Name))
	}
	if !inExpr {
		m.renamer.enterScope(decl.Scope, false)
	}
	m.minifyParams(decl.Params)
	m.minifyBlockStmt(decl.Body, false)
	m.renamer.exitScope()
}

func (m *jsMinifier) minifyMethodDecl(decl js.MethodDecl) {
	if decl.Static {
		m.write([]byte("static"))
		m.writeSpaceBeforeIdent()
	}
	if decl.Async {
		m.write([]byte("async"))
		if decl.Generator {
			m.write([]byte("*"))
		} else {
			m.writeSpaceBeforeIdent()
		}
	} else if decl.Generator {
		m.write([]byte("*"))
	} else if decl.Get {
		m.write([]byte("get"))
		m.writeSpaceBeforeIdent()
	} else if decl.Set {
		m.write([]byte("set"))
		m.writeSpaceBeforeIdent()
	}
	m.minifyPropertyName(decl.Name)
	m.renamer.enterScope(decl.Scope, false)
	m.minifyParams(decl.Params)
	m.minifyBlockStmt(decl.Body, false)
	m.renamer.exitScope()
}

func (m *jsMinifier) minifyArrowFunc(decl js.ArrowFunc) {
	m.renamer.enterScope(decl.Scope, false)
	if decl.Async {
		m.write([]byte("async"))
	}
	if decl.Params.Rest == nil && len(decl.Params.List) == 1 && decl.Params.List[0].Default == nil {
		if decl.Async && isIdentStartBindingElement(decl.Params.List[0]) {
			m.write([]byte(" "))
		}
		m.minifyBindingElement(decl.Params.List[0])
	} else {
		m.minifyParams(decl.Params)
	}
	m.write([]byte("=>"))
	removeBraces := false
	if 0 < len(decl.Body.List) {
		returnStmt, isReturn := decl.Body.List[len(decl.Body.List)-1].(*js.ReturnStmt)
		if isReturn && returnStmt.Value != nil {
			// merge expression statements to final return statement, remove function body braces
			var list []js.IExpr
			removeBraces = true
			for _, item := range decl.Body.List[:len(decl.Body.List)-1] {
				if expr, isExpr := item.(*js.ExprStmt); isExpr {
					list = append(list, expr.Value)
				} else {
					removeBraces = false
					break
				}
			}
			if removeBraces {
				list = append(list, returnStmt.Value)
				expr := list[0]
				for _, right := range list[1:] {
					expr = &js.BinaryExpr{js.CommaToken, expr, right}
				}
				m.minifyExpr(expr, js.OpAssign)
			}
		} else if isReturn && returnStmt.Value == nil {
			// remove empty return
			decl.Body.List = decl.Body.List[:len(decl.Body.List)-1]
		}
	}
	if !removeBraces {
		m.minifyBlockStmt(decl.Body, false)
	}
	m.renamer.exitScope()
}

func (m *jsMinifier) minifyClassDecl(decl js.ClassDecl) {
	m.write([]byte("class"))
	if decl.Name != nil {
		m.write([]byte(" "))
		m.write(m.renamer.rename(decl.Name))
	}
	if decl.Extends != nil {
		m.write([]byte(" extends"))
		m.writeSpaceBeforeIdent()
		m.minifyExpr(decl.Extends, js.OpLHS)
	}
	m.write([]byte("{"))
	for _, item := range decl.Methods {
		m.minifyMethodDecl(item)
	}
	m.write([]byte("}"))
}

func (m *jsMinifier) minifyPropertyName(name js.PropertyName) {
	if name.Computed != nil {
		m.write([]byte("["))
		m.minifyExpr(name.Computed, js.OpAssign)
		m.write([]byte("]"))
	} else if name.Literal.TokenType == js.StringToken {
		data := name.Literal.Data(m.src)
		if _, ok := js.IsIdentifierName(data[1 : len(data)-1]); ok {
			m.write(data[1 : len(data)-1])
		} else if _, ok := js.IsNumericLiteral(data[1 : len(data)-1]); ok {
			m.write(data[1 : len(data)-1])
		} else {
			m.write(data)
		}
	} else {
		m.write(name.Literal.Data(m.src))
	}
}

func (m *jsMinifier) minifyProperty(property js.Property) {
	if property.Key != nil {
		m.minifyPropertyName(*property.Key)
		m.write([]byte(":"))
	} else if property.Spread {
		m.write([]byte("..."))
	} else if lit, ok := property.Value.(*js.LiteralExpr); ok && lit.TokenType == js.IdentifierToken && !m.renamer.inGlobalScope() {
		// add 'old-name:' before BindingName as the latter will be renamed
		m.write(lit.Data(m.src))
		m.write([]byte(":"))
	}
	m.minifyExpr(property.Value, js.OpAssign)
	if property.Init != nil {
		m.write([]byte("="))
		m.minifyExpr(property.Init, js.OpAssign)
	}
}

func (m *jsMinifier) minifyBindingElement(element js.BindingElement) {
	if element.Binding != nil {
		m.minifyBinding(element.Binding)
		if element.Default != nil {
			m.write([]byte("="))
			m.minifyExpr(element.Default, js.OpAssign)
		}
	}
}

func (m *jsMinifier) minifyBinding(i js.IBinding) {
	switch binding := i.(type) {
	case *js.BindingName:
		m.write(m.renamer.rename(binding.Data(m.src)))
	case *js.BindingArray:
		m.write([]byte("["))
		for i, item := range binding.List {
			if i != 0 {
				m.write([]byte(","))
			}
			m.minifyBindingElement(item)
		}
		if binding.Rest != nil {
			if 0 < len(binding.List) {
				m.write([]byte(","))
			}
			m.write([]byte("..."))
			m.minifyBinding(binding.Rest)
		} else if 0 < len(binding.List) && binding.List[len(binding.List)-1].Binding == nil {
			m.write([]byte(","))
		}
		m.write([]byte("]"))
	case *js.BindingObject:
		m.write([]byte("{"))
		for i, item := range binding.List {
			if i != 0 {
				m.write([]byte(","))
			}
			if item.Key != nil {
				m.minifyPropertyName(*item.Key)
				m.write([]byte(":"))
			} else if name, ok := item.Value.Binding.(*js.BindingName); ok {
				if data := name.Data(m.src); !bytes.Equal(data, m.renamer.rename(data)) {
					// add 'old-name:' before BindingName as the latter will be renamed
					m.write(data)
					m.write([]byte(":"))
				}
			}
			m.minifyBindingElement(item.Value)
		}
		if rest := binding.Rest.Data(m.src); rest != nil {
			if 0 < len(binding.List) {
				m.write([]byte(","))
			}
			m.write([]byte("..."))
			m.write(rest)
		}
		m.write([]byte("}"))
	}
}

func (m *jsMinifier) minifyExpr(i js.IExpr, prec js.OpPrec) {
	switch expr := i.(type) {
	case *js.LiteralExpr:
		data := expr.Data(m.src)
		if expr.TokenType == js.DecimalToken {
			m.write(minify.Number(data, 0))
		} else if expr.TokenType == js.BinaryToken {
			m.write(binaryNumber(data))
		} else if expr.TokenType == js.OctalToken {
			m.write(octalNumber(data))
		} else if expr.TokenType == js.TrueToken {
			if js.OpUnary < prec {
				m.write([]byte("(!0)"))
			} else {
				m.write([]byte("!0"))
			}
		} else if expr.TokenType == js.FalseToken {
			if js.OpUnary < prec {
				m.write([]byte("(!1)"))
			} else {
				m.write([]byte("!1"))
			}
		} else if expr.TokenType == js.IdentifierToken && bytes.Equal(data, []byte("undefined")) && !m.renamer.exists(data) {
			if js.OpUnary < prec {
				m.write([]byte("(void 0)"))
			} else {
				m.write([]byte("void 0"))
			}
		} else if expr.TokenType == js.IdentifierToken && bytes.Equal(data, []byte("Infinity")) && !m.renamer.exists(data) {
			if js.OpMul < prec {
				m.write([]byte("(1/0)"))
			} else {
				m.write([]byte("1/0"))
			}
		} else if expr.TokenType == js.IdentifierToken {
			m.write(m.renamer.rename(data))
		} else if expr.TokenType == js.StringToken {
			m.write(minifyString(data))
		} else {
			m.write(data)
		}
	case *js.BinaryExpr:
		precLeft := binaryLeftPrecMap[expr.Op]
		// convert (a,b)&&c into a,b&&c but not a=(b,c)&&d into a=(b,c&&d)
		if prec <= js.OpExpr {
			if group, ok := expr.X.(*js.GroupExpr); ok {
				if binary, ok := group.X.(*js.BinaryExpr); ok && binary.Op == js.CommaToken {
					expr.X = group.X
					precLeft = js.OpExpr
				}
			}
		}
		m.minifyExpr(expr.X, precLeft)
		if expr.Op == js.InstanceofToken || expr.Op == js.InToken {
			m.writeSpaceAfterIdent()
			m.write(expr.Op.Bytes())
			m.writeSpaceBeforeIdent()
		} else {
			if expr.Op == js.GtToken {
				if unary, ok := expr.X.(*js.UnaryExpr); ok && unary.Op == js.PostDecrToken {
					m.write([]byte(" "))
				}
			}
			m.write(expr.Op.Bytes())
			if expr.Op == js.AddToken {
				if unary, ok := expr.Y.(*js.UnaryExpr); ok && (unary.Op == js.PosToken || unary.Op == js.PreIncrToken) {
					m.write([]byte(" "))
				}
			} else if expr.Op == js.NegToken {
				if unary, ok := expr.X.(*js.UnaryExpr); ok && (unary.Op == js.NegToken || unary.Op == js.PreDecrToken) {
					m.write([]byte(" "))
				}
			} else if expr.Op == js.LtToken {
				if unary, ok := expr.Y.(*js.UnaryExpr); ok && unary.Op == js.NotToken {
					if unary2, ok2 := unary.X.(*js.UnaryExpr); ok2 && unary2.Op == js.PreDecrToken {
						m.write([]byte(" "))
					}
				}
			}
		}
		m.minifyExpr(expr.Y, binaryRightPrecMap[expr.Op])
	case *js.UnaryExpr:
		if expr.Op == js.PostIncrToken || expr.Op == js.PostDecrToken {
			m.minifyExpr(expr.X, unaryPrecMap[expr.Op])
			m.write(expr.Op.Bytes())
		} else {
			m.write(expr.Op.Bytes())
			if expr.Op == js.DeleteToken || expr.Op == js.VoidToken || expr.Op == js.TypeofToken || expr.Op == js.AwaitToken {
				m.writeSpaceBeforeIdent()
			} else if expr.Op == js.PosToken {
				if unary, ok := expr.X.(*js.UnaryExpr); ok && (unary.Op == js.PosToken || unary.Op == js.PreIncrToken) {
					m.write([]byte(" "))
				}
			} else if expr.Op == js.NegToken {
				if unary, ok := expr.X.(*js.UnaryExpr); ok && (unary.Op == js.NegToken || unary.Op == js.PreDecrToken) {
					m.write([]byte(" "))
				}
			} else if expr.Op == js.NotToken {
				if lit, ok := expr.X.(*js.LiteralExpr); ok && (lit.TokenType == js.StringToken || lit.TokenType == js.RegExpToken) {
					m.write([]byte("1"))
					break
				} else if ok && lit.TokenType == js.DecimalToken {
					if num := minify.Number(lit.Data(m.src), 0); len(num) == 1 && num[0] == '0' {
						m.write([]byte("0"))
					} else {
						m.write([]byte("1"))
					}
					break
				}
			}
			m.minifyExpr(expr.X, unaryPrecMap[expr.Op])
		}
	case *js.DotExpr:
		if group, ok := expr.X.(*js.GroupExpr); ok {
			if lit, ok := group.X.(*js.LiteralExpr); ok && lit.TokenType == js.DecimalToken {
				num := minify.Number(lit.Data(m.src), 0)
				isInt := true
				for _, c := range num {
					if c == '.' || c == 'e' || c == 'E' {
						isInt = false
						break
					}
				}
				if isInt {
					m.write(num)
					m.write([]byte("."))
				} else {
					m.write(num)
				}
				m.write([]byte("."))
				m.write(expr.Y.Data(m.src))
				break
			}
		}
		m.minifyExpr(expr.X, js.OpMember)
		m.write([]byte("."))
		m.write(expr.Y.Data(m.src))
	case *js.GroupExpr:
		precInside := exprPrec(expr.X)
		if prec <= precInside {
			m.minifyExpr(expr.X, prec)
		} else {
			m.write([]byte("("))
			m.minifyExpr(expr.X, js.OpExpr)
			m.write([]byte(")"))
		}
	case *js.ArrayExpr:
		m.write([]byte("["))
		for i, item := range expr.List {
			if i != 0 {
				m.write([]byte(","))
			}
			if item.Spread {
				m.write([]byte("..."))
			}
			m.minifyExpr(item.Value, js.OpAssign)
		}
		if 0 < len(expr.List) && expr.List[len(expr.List)-1].Value == nil {
			m.write([]byte(","))
		}
		m.write([]byte("]"))
	case *js.ObjectExpr:
		m.write([]byte("{"))
		for i, item := range expr.List {
			if i != 0 {
				m.write([]byte(","))
			}
			m.minifyProperty(item)
		}
		m.write([]byte("}"))
	case *js.TemplateExpr:
		if expr.Tag != nil {
			m.minifyExpr(expr.Tag, js.OpLHS)
		}
		for _, item := range expr.List {
			m.write(item.Value)
			m.minifyExpr(item.Expr, js.OpExpr)
		}
		m.write(expr.Tail)
	case *js.NewExpr:
		if expr.Args == nil && js.OpMember <= prec {
			m.write([]byte("(new"))
			m.writeSpaceBeforeIdent()
			m.minifyExpr(expr.X, js.OpMember)
			m.write([]byte(")"))
		} else {
			m.write([]byte("new"))
			m.writeSpaceBeforeIdent()
			m.minifyExpr(expr.X, js.OpMember)
			if expr.Args != nil {
				m.minifyArguments(*expr.Args)
			}
		}
	case *js.NewTargetExpr:
		m.write([]byte("new.target"))
		m.writeSpaceBeforeIdent()
	case *js.ImportMetaExpr:
		m.write([]byte("import.meta"))
		m.writeSpaceBeforeIdent()
	case *js.YieldExpr:
		m.write([]byte("yield"))
		m.writeSpaceBeforeIdent()
		if expr.X != nil {
			if expr.Generator {
				m.write([]byte("*"))
				m.minifyExpr(expr.X, js.OpAssign)
			} else if lit, ok := expr.X.(*js.LiteralExpr); !ok || lit.TokenType != js.IdentifierToken || !bytes.Equal(lit.Data(m.src), []byte("undefined")) || m.renamer.exists(lit.Data(m.src)) {
				m.minifyExpr(expr.X, js.OpAssign)
			}
		}
	case *js.CallExpr:
		m.minifyExpr(expr.X, js.OpMember)
		m.minifyArguments(expr.Args)
	case *js.IndexExpr:
		m.minifyExpr(expr.X, js.OpMember)
		if lit, ok := expr.Index.(*js.LiteralExpr); ok && lit.TokenType == js.StringToken {
			data := lit.Data(m.src)
			if _, ok := js.IsIdentifierName(data[1 : len(data)-1]); ok {
				m.write([]byte("."))
				m.write(data[1 : len(data)-1])
				break
			} else if _, ok := js.IsNumericLiteral(data[1 : len(data)-1]); ok {
				m.write([]byte("["))
				m.write(data[1 : len(data)-1])
				m.write([]byte("]"))
				break
			}
		}
		m.write([]byte("["))
		m.minifyExpr(expr.Index, js.OpExpr)
		m.write([]byte("]"))
	case *js.CondExpr:
		// remove double negative !! in condition, or switch cases for single negative !
		if unary1, ok := expr.Cond.(*js.UnaryExpr); ok && unary1.Op == js.NotToken {
			if unary2, ok := unary1.X.(*js.UnaryExpr); ok && unary2.Op == js.NotToken {
				if isBooleanExpr(unary2.X) {
					expr.Cond = unary2.X
				}
			} else {
				expr.Cond = unary1.X
				expr.X, expr.Y = expr.Y, expr.X
			}
		}
		// if value is truthy or falsy, remove false case
		// if condition and true case are equal, or true and false case, simplify
		if truthy, ok := m.isTruthy(expr.Cond); truthy && ok {
			m.minifyExpr(expr.X, prec)
		} else if !truthy && ok {
			m.minifyExpr(expr.Y, prec)
		} else if m.isEqualExpr(expr.Cond, expr.X) && prec <= js.OpOr && (exprPrec(expr.X) < js.OpAssign || binaryLeftPrecMap[js.OrToken] <= exprPrec(expr.X)) && (exprPrec(expr.Y) < js.OpAssign || binaryRightPrecMap[js.OrToken] <= exprPrec(expr.Y)) {
			// for higher prec we need to add group parenthesis, and for lower prec we have parenthesis anyways. This only is shorter if len(expr.X) >= 3. isEqualExpr only checks for literal variables, which is a name will be minified to a one or two character name.
			m.minifyExpr(expr.X, binaryLeftPrecMap[js.OrToken])
			m.write([]byte("||"))
			m.minifyExpr(expr.Y, binaryRightPrecMap[js.OrToken])
		} else if m.isEqualExpr(expr.X, expr.Y) {
			if prec <= js.OpExpr {
				m.minifyExpr(expr.Cond, binaryLeftPrecMap[js.CommaToken])
				m.write([]byte(","))
				m.minifyExpr(expr.X, binaryRightPrecMap[js.CommaToken])
			} else {
				m.write([]byte("("))
				m.minifyExpr(expr.Cond, binaryLeftPrecMap[js.CommaToken])
				m.write([]byte(","))
				m.minifyExpr(expr.X, binaryRightPrecMap[js.CommaToken])
				m.write([]byte(")"))
			}
		} else {
			// shorten if cases are true and false
			trueX, falseX := m.isTrue(expr.X), m.isFalse(expr.X)
			trueY, falseY := m.isTrue(expr.Y), m.isFalse(expr.Y)
			if trueX && falseY || falseX && trueY {
				m.minifyBooleanExpr(expr.Cond, falseX, prec)
			} else if trueX || trueY {
				// trueX != trueY
				m.minifyBooleanExpr(expr.Cond, trueY, binaryLeftPrecMap[js.OrToken])
				m.write([]byte("||"))
				if trueY {
					m.minifyExpr(expr.X, binaryRightPrecMap[js.OrToken])
				} else {
					m.minifyExpr(expr.Y, binaryRightPrecMap[js.OrToken])
				}
			} else if falseX || falseY {
				// falseX != falseY
				m.minifyBooleanExpr(expr.Cond, falseX, binaryLeftPrecMap[js.AndToken])
				m.write([]byte("&&"))
				if falseX {
					m.minifyExpr(expr.Y, binaryRightPrecMap[js.AndToken])
				} else {
					m.minifyExpr(expr.X, binaryRightPrecMap[js.AndToken])
				}
			} else {
				// regular conditional expression
				m.minifyExpr(expr.Cond, js.OpCoalesce)
				m.write([]byte("?"))
				m.minifyExpr(expr.X, js.OpAssign)
				m.write([]byte(":"))
				m.minifyExpr(expr.Y, js.OpAssign)
			}
		}
	case *js.OptChainExpr:
		m.minifyExpr(expr.X, js.OpLHS)
		m.write([]byte("?."))
		m.minifyExpr(expr.Y, js.OpMember)
	case *js.VarDecl:
		m.minifyVarDecl(*expr) // only happens for init in for statement
	case *js.FuncDecl:
		m.minifyFuncDecl(*expr, true)
	case *js.ArrowFunc:
		m.minifyArrowFunc(*expr)
	case *js.MethodDecl:
		m.minifyMethodDecl(*expr)
	case *js.ClassDecl:
		m.minifyClassDecl(*expr)
	}
}

func (m *jsMinifier) minifyBooleanExpr(expr js.IExpr, invert bool, prec js.OpPrec) {
	if invert {
		unaryExpr, isUnary := expr.(*js.UnaryExpr)
		binaryExpr, isBinary := expr.(*js.BinaryExpr)
		if isUnary && unaryExpr.Op == js.NotToken && isBooleanExpr(unaryExpr.X) {
			m.minifyExpr(&js.GroupExpr{expr}, prec)
		} else if isBinary && binaryOpPrecMap[binaryExpr.Op] == js.OpEquals {
			if binaryExpr.Op == js.EqEqToken {
				binaryExpr.Op = js.NotEqToken
			} else if binaryExpr.Op == js.NotEqToken {
				binaryExpr.Op = js.EqEqToken
			} else if binaryExpr.Op == js.EqEqToken {
				binaryExpr.Op = js.NotEqEqToken
			} else if binaryExpr.Op == js.NotEqEqToken {
				binaryExpr.Op = js.EqEqEqToken
			}
			m.minifyExpr(expr, prec)
		} else {
			m.write([]byte("!"))
			m.minifyExpr(&js.GroupExpr{expr}, js.OpUnary)
		}
	} else if isBooleanExpr(expr) {
		m.minifyExpr(&js.GroupExpr{expr}, prec)
	} else {
		m.write([]byte("!!"))
		m.minifyExpr(&js.GroupExpr{expr}, js.OpUnary)
	}
}

var fscopes, bscopes, bescopes int
var defines []int
var undefines map[string]bool = map[string]bool{}
var depth, maxdepth int

type renamerNop struct {
}

func newRenamerNop(scope js.Scope, deterministic bool) *renamerNop {
	return &renamerNop{}
}
func (r *renamerNop) enterScope(scope js.Scope, isBlock bool) {
	depth++
	if depth > maxdepth {
		maxdepth = depth
	}
	n_defines := 0
	for name, v := range scope.Vars {
		if 0 < v.Uses {
			if v.Declared {
				n_defines++
			} else {
				undefines[name] = true
			}
		}
	}
	if isBlock {
		bscopes++
		if n_defines == 0 {
			bescopes++
		}
	} else {
		fscopes++
	}
	defines = append(defines, n_defines)
	return
}
func (r *renamerNop) exitScope() {
	depth--
	return
}
func (r *renamerNop) next(name []byte) []byte {
	return nil
}
func (r *renamerNop) rename(name []byte) []byte {
	return []byte("a")
}
func (r *renamerNop) exists(name []byte) bool {
	return false
}
func (r *renamerNop) inGlobalScope() bool {
	return true
}

type renamer struct {
	reserved      map[string]struct{}
	vars          []map[string]js.Var
	renames       []map[string][]byte
	lastRename    [][]byte
	deterministic bool
}

func newRenamer(undeclared map[string]struct{}, deterministic bool) *renamer {
	fmt.Println("undeclared", undeclared)
	reserved := make(map[string]struct{}, len(js.Keywords)+len(js.Globals)+len(undeclared))
	for name, _ := range js.Keywords {
		reserved[name] = struct{}{}
	}
	for name, _ := range js.Globals {
		reserved[name] = struct{}{}
	}
	for name, _ := range undeclared {
		reserved[name] = struct{}{}
	}
	return &renamer{
		reserved:      reserved,
		vars:          []map[string]js.Var{},
		renames:       []map[string][]byte{map[string][]byte{}},
		lastRename:    [][]byte{[]byte("`")},
		deterministic: deterministic,
	}
}

func (r *renamer) enterScope(scope js.Scope, isBlock bool) {
	fmt.Println(scope.Vars)
	renames := map[string][]byte{}
	rename := []byte("`") // so that the next is 'a'

	n := len(r.renames)
	if !r.inGlobalScope() {
		parentRenames := r.renames[n-1]
		for name, v := range scope.Vars {
			if 0 < v.Uses && !v.Declared {
				if rename, ok := parentRenames[name]; ok {
					renames[name] = rename
				}
			}
		}
	}
	if r.deterministic {
		bound := []string{}
		for name, v := range scope.Vars {
			if v.Declared && 0 < v.Uses {
				bound = append(bound, name)
			}
		}
		sort.Strings(bound)
		for _, name := range bound {
			rename = r.next(rename)
			for r.isReserved(rename) {
				rename = r.next(rename)
			}
			renames[name] = parse.Copy(rename)
		}
	} else {
		for name, v := range scope.Vars {
			if v.Declared && 0 < v.Uses {
				rename = r.next(rename)
				for r.isReserved(rename) {
					rename = r.next(rename)
				}
				renames[name] = parse.Copy(rename)
			}
		}
	}
	r.vars = append(r.vars, scope.Vars)
	r.renames = append(r.renames, renames)
	r.lastRename = append(r.lastRename, rename)
}

func (r *renamer) exitScope() {
	n := len(r.vars)
	r.vars = r.vars[:n-1]
	r.renames = r.renames[:n-1]
	r.lastRename = r.lastRename[:n-1]
}

func (r *renamer) isReserved(name []byte) bool {
	if _, ok := r.reserved[string(name)]; ok {
		return true
	} else if !r.inGlobalScope() {
		if v, ok := r.vars[len(r.vars)-1][string(name)]; ok && 0 < v.Uses {
			return true
		}
	}
	return false
}

func (r *renamer) next(name []byte) []byte {
	if name[len(name)-1] == 'z' {
		name[len(name)-1] = 'A'
	} else if name[len(name)-1] == 'Z' {
		name[len(name)-1] = '_'
	} else if name[len(name)-1] == '_' {
		name[len(name)-1] = '$'
	} else if name[len(name)-1] == '$' {
		isLast := true
		for i := len(name) - 2; 0 <= i; i-- {
			if name[i] != 'Z' {
				if name[i] == 'z' {
					name[i] = 'A'
				} else {
					name[i]++
				}
				for j := i + 1; j < len(name); j++ {
					name[j] = 'a'
				}
				isLast = false
			}
		}
		if isLast {
			for j := 0; j < len(name); j++ {
				name[j] = 'a'
			}
			name = append(name, 'a')
		}
	} else {
		name[len(name)-1]++
	}
	return name
}

func (r *renamer) rename(name []byte) []byte {
	if r.inGlobalScope() {
		return name
	}
	for i := len(r.renames) - 1; 0 <= i; i-- {
		if rename, ok := r.renames[i][string(name)]; ok {
			return rename
		}
	}
	return name
}

func (r *renamer) exists(name []byte) bool {
	//if _, ok := r.globals[string(name)]; ok {
	//	return true
	//}
	//for j := len(r.renames) - 1; 0 <= j; j-- {
	//	if _, ok := r.renames[j][string(name)]; ok {
	//		return true
	//	}
	//}
	return false
}

func (r *renamer) inGlobalScope() bool {
	return len(r.vars) == 0
}
