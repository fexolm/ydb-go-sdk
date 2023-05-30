package main

import (
	"bufio"
	"container/list"
	"fmt"
	"go/build"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//nolint:maligned
type Writer struct {
	Output  io.Writer
	Context build.Context

	once sync.Once
	bw   *bufio.Writer

	atEOL bool
	depth int
	scope *list.List

	pkg *types.Package
	std map[string]bool
}

func (w *Writer) Write(p Package) error {
	w.pkg = p.Package

	w.init()
	w.line(`// Code generated by gtrace. DO NOT EDIT.`)

	for i, line := range p.BuildConstraints {
		if i == 0 {
			w.line()
		}
		w.line(line)
	}
	w.line()
	w.line(`package `, p.Name())
	w.line()

	var deps []dep
	for _, trace := range p.Traces {
		deps = w.traceImports(deps, trace)
	}
	w.importDeps(deps)

	w.newScope(func() {
		for _, trace := range p.Traces {
			w.options(trace)
			w.compose(trace)
			if trace.Nested {
				w.isZero(trace)
			}
			for _, hook := range trace.Hooks {
				w.hook(trace, hook)
			}
		}
		for _, trace := range p.Traces {
			for _, hook := range trace.Hooks {
				w.hookShortcut(trace, hook)
			}
		}
	})

	return w.bw.Flush()
}

func (w *Writer) init() {
	w.once.Do(func() {
		w.bw = bufio.NewWriter(w.Output)
		w.scope = list.New()
	})
}

func (w *Writer) mustDeclare(name string) {
	s := w.scope.Back().Value.(*scope)
	if !s.set(name) {
		where := s.where(name)
		panic(fmt.Sprintf(
			"gtrace: can't declare identifier: %q: already defined at %q",
			name, where,
		))
	}
}

func (w *Writer) declare(name string) string {
	if isPredeclared(name) {
		name = firstChar(name)
	}
	s := w.scope.Back().Value.(*scope)
	for i := 0; ; i++ {
		v := name
		if i > 0 {
			v += strconv.Itoa(i)
		}
		if token.IsKeyword(v) {
			continue
		}
		if w.isGlobalScope() && w.pkg.Scope().Lookup(v) != nil {
			continue
		}
		if s.set(v) {
			return v
		}
	}
}

func isPredeclared(name string) bool {
	return types.Universe.Lookup(name) != nil
}

func (w *Writer) isGlobalScope() bool {
	return w.scope.Back().Prev() == nil
}

func (w *Writer) capture(vars ...string) {
	s := w.scope.Back().Value.(*scope)
	for _, v := range vars {
		if !s.set(v) {
			panic(fmt.Sprintf("can't capture variable %q", v))
		}
	}
}

type dep struct {
	pkgPath string
	pkgName string
	typName string
}

func (w *Writer) typeImports(dst []dep, t types.Type) []dep {
	if p, ok := t.(*types.Pointer); ok {
		return w.typeImports(dst, p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return dst
	}
	var (
		obj = n.Obj()
		pkg = obj.Pkg()
	)
	if pkg != nil && pkg.Path() != w.pkg.Path() {
		return append(dst, dep{
			pkgPath: pkg.Path(),
			pkgName: pkg.Name(),
			typName: obj.Name(),
		})
	}
	return dst
}

func forEachField(s *types.Struct, fn func(*types.Var)) {
	for i := 0; i < s.NumFields(); i++ {
		fn(s.Field(i))
	}
}

func unwrapStruct(t types.Type) (n *types.Named, s *types.Struct) {
	var ok bool
	n, ok = t.(*types.Named)
	if ok {
		s, _ = n.Underlying().(*types.Struct)
	}
	return
}

func (w *Writer) funcImports(dst []dep, fn *Func) []dep {
	for i := range fn.Params {
		dst = w.typeImports(dst, fn.Params[i].Type)
		if _, s := unwrapStruct(fn.Params[i].Type); s != nil {
			forEachField(s, func(v *types.Var) {
				if v.Exported() {
					dst = w.typeImports(dst, v.Type())
				}
			})
		}
	}
	for _, x := range fn.Result {
		if fn, ok := x.(*Func); ok {
			dst = w.funcImports(dst, fn)
		}
	}
	return dst
}

func (w *Writer) traceImports(dst []dep, t *Trace) []dep {
	for _, h := range t.Hooks {
		dst = w.funcImports(dst, h.Func)
	}
	return dst
}

func (w *Writer) importDeps(deps []dep) {
	seen := map[string]bool{}
	for i := 0; i < len(deps); {
		d := deps[i]
		if seen[d.pkgPath] {
			n := len(deps)
			deps[i], deps[n-1] = deps[n-1], deps[i]
			deps = deps[:n-1]
			continue
		}
		seen[d.pkgPath] = true
		i++
	}
	if len(deps) == 0 {
		return
	}
	sort.Slice(deps, func(i, j int) bool {
		var (
			d0   = deps[i]
			d1   = deps[j]
			std0 = w.isStdLib(d0.pkgPath)
			std1 = w.isStdLib(d1.pkgPath)
		)
		if std0 != std1 {
			return std0
		}
		return d0.pkgPath < d1.pkgPath
	})
	w.line(`import (`)
	var lastStd bool
	for i := range deps {
		if w.isStdLib(deps[i].pkgPath) {
			lastStd = true
		} else if lastStd {
			lastStd = false
			w.line()
		}
		w.line("\t", `"`, deps[i].pkgPath, `"`)
	}
	w.line(`)`)
	w.line()
}

func (w *Writer) isStdLib(pkg string) bool {
	w.ensureStdLibMapping()
	s := strings.Split(pkg, "/")[0]
	return w.std[s]
}

func (w *Writer) ensureStdLibMapping() {
	if w.std != nil {
		return
	}
	w.std = make(map[string]bool)

	src := filepath.Join(w.Context.GOROOT, "src")
	files, err := os.ReadDir(src)
	if err != nil {
		panic(fmt.Sprintf("can't list GOROOT's src: %v", err))
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		name := filepath.Base(file.Name())
		switch name {
		case "cmd", "internal":
			// Ignored.

		default:
			w.std[name] = true
		}
	}
}

func (w *Writer) call(args []string) {
	w.code(`(`)
	for i, name := range args {
		if i > 0 {
			w.code(`, `)
		}
		w.code(name)
	}
	w.line(`)`)
}

func (w *Writer) isZero(trace *Trace) {
	w.newScope(func() {
		t := w.declare("t")
		w.line(`// isZero checks whether `, t, ` is empty`)
		w.line(`func (`, t, ` `, trace.Name, `) isZero() bool {`)
		w.block(func() {
			for _, hook := range trace.Hooks {
				w.line(`if `, t, `.`, hook.Name, ` != nil {`)
				w.block(func() {
					w.line(`return false`)
				})
				w.line(`}`)
			}
			w.line(`return true`)
		})
		w.line(`}`)
	})
}

func (w *Writer) compose(trace *Trace) {
	w.newScope(func() {
		t := w.declare("t")
		x := w.declare("x")
		ret := w.declare("ret")
		w.line(`// Compose returns a new `, trace.Name, ` which has functional fields composed both from `,
			t, ` and `, x, `.`,
		)
		w.code(`func (`, t, ` *`, trace.Name, `) Compose(`, x, ` *`, trace.Name, `, opts ...`+trace.Name+`ComposeOption) `)
		w.line(`*`, trace.Name, ` {`)
		w.block(func() {
			w.line(`var `, ret, ` `, trace.Name, ``)
			if len(trace.Hooks) > 0 {
				w.line(`options := `, unexported(trace.Name), `ComposeOptions{}`)
				w.line(`for _, opt := range opts {`)
				w.block(func() {
					w.line(`if opt != nil {`)
					w.block(func() {
						w.line(`opt(&options)`)
					})
					w.line(`}`)
				})
				w.line(`}`)
			}
			for _, hook := range trace.Hooks {
				w.composeHook(hook, t, x, ret+"."+hook.Name)
			}
			w.line(`return &`, ret)
		})
		w.line(`}`)
	})
}

func (w *Writer) composeHook(hook Hook, t1, t2, dst string) {
	w.line(`{`)
	w.block(func() {
		h1 := w.declare("h1")
		h2 := w.declare("h2")
		w.line(h1, ` := `, t1, `.`, hook.Name)
		w.line(h2, ` := `, t2, `.`, hook.Name)
		w.code(dst, ` = `)
		w.composeHookCall(hook.Func, h1, h2)
	})
	w.line(`}`)
}

func (w *Writer) composeHookCall(fn *Func, h1, h2 string) {
	w.newScope(func() {
		w.capture(h1, h2)
		w.block(func() {
			w.capture(h1, h2)
			w.code(`func`)
			args := w.funcParams(fn.Params)
			if fn.HasResult() {
				w.code(` `)
			}
			w.funcResults(fn)
			w.line(` {`)
			w.line(`if options.panicCallback != nil {`)
			w.block(func() {
				w.line("defer func() {")
				w.block(func() {
					w.line("if e := recover(); e != nil {")
					w.block(func() {
						w.line(`options.panicCallback(e)`)
					})
					w.line("}")
				})
				w.line("}()")
			})
			w.line("}")
			var (
				r1 string
				r2 string
				rs []string
			)
			if fn.HasResult() {
				r1 = w.declare("r")
				r2 = w.declare("r")
				rs = []string{r1, r2}
				w.code("var " + r1 + ", " + r2 + " ")
				w.funcResults(fn)
				_ = w.bw.WriteByte('\n')
				w.atEOL = true
			}
			for i, h := range []string{h1, h2} {
				w.line("if " + h + " != nil {")
				w.block(func() {
					if fn.HasResult() {
						w.code(rs[i], ` = `)
					}
					w.code(h)
					w.call(args)
				})
				w.line("}")
			}
			if fn.HasResult() {
				w.code(`return `)
				switch x := fn.Result[0].(type) {
				case *Func:
					w.composeHookCall(x, r1, r2)
				case *Trace:
					w.line(r1, `.Compose(`, r2, `)`)
				default:
					panic("unknown result type")
				}
			}
		})
		w.line(`}`)
	})
}

func (w *Writer) options(trace *Trace) {
	w.newScope(func() {
		w.line(fmt.Sprintf(`// %sComposeOptions is a holder of options`, unexported(trace.Name)))
		w.line(fmt.Sprintf(`type %sComposeOptions struct {`, unexported(trace.Name)))
		w.block(func() {
			w.line(`panicCallback func(e interface{})`)
		})
		w.line(`}`)
		_ = w.bw.WriteByte('\n')
	})
	w.newScope(func() {
		w.line(fmt.Sprintf(`// %sOption specified %s compose option`, trace.Name, trace.Name))
		w.line(fmt.Sprintf(`type %sComposeOption func(o *%sComposeOptions)`, trace.Name, unexported(trace.Name)))
		_ = w.bw.WriteByte('\n')
	})
	w.newScope(func() {
		w.line(fmt.Sprintf(`// With%sPanicCallback specified behavior on panic`, trace.Name))
		w.line(fmt.Sprintf(`func With%sPanicCallback(cb func(e interface{})) %sComposeOption {`, trace.Name, trace.Name))
		w.block(func() {
			w.line(fmt.Sprintf(`return func(o *%sComposeOptions) {`, unexported(trace.Name)))
			w.block(func() {
				w.line(`o.panicCallback = cb`)
			})
			w.line(`}`)
		})
		w.line(`}`)
		_ = w.bw.WriteByte('\n')
	})
}

func (w *Writer) hook(trace *Trace, hook Hook) {
	w.newScope(func() {
		t := w.declare("t")
		fn := w.declare("fn")

		w.code(`func (`, t, ` *`, trace.Name, `) `, unexported(hook.Name))

		w.code(`(`)
		var args []string
		for i := range hook.Func.Params {
			if i > 0 {
				w.code(`, `)
			}
			args = append(args, w.funcParam(&hook.Func.Params[i]))
		}
		w.code(`)`)
		if hook.Func.HasResult() {
			w.code(` `)
		}
		w.funcResultsFlags(hook.Func, docs)
		w.line(` {`)
		w.block(func() {
			w.line(fn, ` := `, t, `.`, hook.Name)
			w.line(`if `, fn, ` == nil {`)
			w.block(func() {
				w.zeroReturn(hook.Func)
			})
			w.line(`}`)

			w.hookFuncCall(hook.Func, fn, args)
		})
		w.line(`}`)
	})
}

func (w *Writer) hookFuncCall(fn *Func, name string, args []string) {
	var res string
	if fn.HasResult() {
		res = w.declare("res")
		w.code(res, ` := `)
	}

	w.code(name)
	w.call(args)

	if !fn.HasResult() {
		return
	}

	r, isFunc := fn.Result[0].(*Func)
	if isFunc {
		w.line(`if `, res, ` == nil {`)
		w.block(func() {
			w.zeroReturn(fn)
		})
		w.line(`}`)

		if r.HasResult() {
			w.newScope(func() {
				w.code(`return func`)
				args := w.funcParams(r.Params)
				w.code(` `)
				w.funcResults(r)
				w.line(` {`)
				w.block(func() {
					w.hookFuncCall(r, res, args)
				})
				w.line(`}`)
			})
			return
		}
	}

	w.line(`return `, res)
}

func nameParam(p *Param) (s string) {
	s = p.Name
	if s == "" {
		s = firstChar(ident(typeBasename(p.Type)))
	}
	return unexported(s)
}

func (w *Writer) declareParams(src []Param) (names []string) {
	names = make([]string, len(src))
	for i := range src {
		names[i] = w.declare(nameParam(&src[i]))
	}
	return names
}

func flattenParams(params []Param) (dst []Param) {
	for i := range params {
		_, s := unwrapStruct(params[i].Type)
		if s != nil {
			dst = flattenStruct(dst, s)
			continue
		}
		dst = append(dst, params[i])
	}
	return dst
}

func typeBasename(t types.Type) (name string) {
	lo, name := rsplit(t.String(), '.')
	if name == "" {
		name = lo
	}
	return name
}

func flattenStruct(dst []Param, s *types.Struct) []Param {
	forEachField(s, func(f *types.Var) {
		if !f.Exported() {
			return
		}
		var (
			name = f.Name()
			typ  = f.Type()
		)
		if name == typeBasename(typ) {
			// NOTE: field name essentially be empty for embedded structs or
			// fields called exactly as type.
			name = ""
		}
		dst = append(dst, Param{
			Name: name,
			Type: typ,
		})
	})
	return dst
}

func (w *Writer) constructParams(params []Param, names []string) (res []string) {
	for i := range params {
		n, s := unwrapStruct(params[i].Type)
		if s != nil {
			var v string
			v, names = w.constructStruct(n, s, names)
			res = append(res, v)
			continue
		}
		name := names[0]
		names = names[1:]
		res = append(res, name)
	}
	return res
}

func (w *Writer) constructStruct(n *types.Named, s *types.Struct, vars []string) (string, []string) {
	p := w.declare("p")
	// maybe skip pointers from flattening to not allocate anyhing during trace.
	w.line(`var `, p, ` `, w.typeString(n))
	for i := 0; i < s.NumFields(); i++ {
		v := s.Field(i)
		if !v.Exported() {
			continue
		}
		name := vars[0]
		vars = vars[1:]
		w.line(p, `.`, v.Name(), ` = `, name)
	}
	return p, vars
}

func (w *Writer) hookShortcut(trace *Trace, hook Hook) {
	name := exported(tempName(trace.Name, hook.Name))

	w.mustDeclare(name)

	w.newScope(func() {
		t := w.declare("t")
		w.code(`func `, name)
		w.code(`(`)
		var ctx string
		w.code(t, ` *`, trace.Name)

		var (
			params = flattenParams(hook.Func.Params)
			names  = w.declareParams(params)
		)
		for i := range params {
			w.code(`, `)
			w.code(names[i], ` `, w.typeString(params[i].Type))
		}
		w.code(`)`)
		if hook.Func.HasResult() {
			w.code(` `)
		}
		w.shortcutFuncResultsFlags(hook.Func, docs)
		w.line(` {`)
		w.block(func() {
			for _, name := range names {
				w.capture(name)
			}
			vars := w.constructParams(hook.Func.Params, names)
			var res string
			if hook.Func.HasResult() {
				res = w.declare("res")
				w.code(res, ` := `)
			}
			w.code(t, `.`, unexported(hook.Name))
			if ctx != "" {
				vars = append([]string{ctx}, vars...)
			}
			w.call(vars)
			if hook.Func.HasResult() {
				w.code(`return `)
				r := hook.Func.Result[0]
				switch x := r.(type) {
				case *Func:
					w.hookFuncShortcut(x, res)
				case *Trace:
					w.line(res)
				default:
					panic("unexpected result type")
				}
			}
		})
		w.line(`}`)
	})
}

func (w *Writer) hookFuncShortcut(fn *Func, name string) {
	w.newScope(func() {
		w.code(`func(`)
		var (
			params = flattenParams(fn.Params)
			names  = w.declareParams(params)
		)
		for i := range params {
			if i > 0 {
				w.code(`, `)
			}
			w.code(names[i], ` `, w.typeString(params[i].Type))
		}
		w.code(`)`)
		if fn.HasResult() {
			w.code(` `)
		}
		w.shortcutFuncResults(fn)
		w.line(` {`)
		w.block(func() {
			for _, name := range names {
				w.capture(name)
			}
			params := w.constructParams(fn.Params, names)
			var res string
			if fn.HasResult() {
				res = w.declare("res")
				w.code(res, ` := `)
			}
			w.code(name)
			w.call(params)
			if fn.HasResult() {
				r := fn.Result[0]
				w.code(`return `)
				switch x := r.(type) {
				case *Func:
					w.hookFuncShortcut(x, res)
				case *Trace:
					w.line(res)
				default:
					panic("unexpected result type")
				}
			}
		})
		w.line(`}`)
	})
}

func (w *Writer) zeroReturn(fn *Func) {
	if !fn.HasResult() {
		w.line(`return`)
		return
	}
	w.code(`return `)
	switch x := fn.Result[0].(type) {
	case *Func:
		w.funcSignature(x)
		w.line(` {`)
		w.block(func() {
			w.zeroReturn(x)
		})
		w.line(`}`)
	case *Trace:
		w.line(x.Name, `{}`)
	default:
		panic("unexpected result type")
	}
}

func (w *Writer) funcParams(params []Param) (vars []string) {
	w.code(`(`)
	for i := range params {
		if i > 0 {
			w.code(`, `)
		}
		vars = append(vars, w.funcParam(&params[i]))
	}
	w.code(`)`)
	return
}

func (w *Writer) funcParam(p *Param) (name string) {
	name = w.declare(nameParam(p))
	w.code(name, ` `)
	w.code(w.typeString(p.Type))
	return name
}

func (w *Writer) funcParamSign(p *Param) {
	name := nameParam(p)
	if len(name) == 1 || isPredeclared(name) {
		name = "_"
	}
	w.code(name, ` `)
	w.code(w.typeString(p.Type))
}

type flags uint8

func (f flags) has(x flags) bool {
	return f&x != 0
}

const (
	zeroFlags flags = 1 << iota >> 1
	docs
)

func (w *Writer) funcResultsFlags(fn *Func, flags flags) {
	for _, r := range fn.Result {
		switch x := r.(type) {
		case *Func:
			w.funcSignatureFlags(x, flags)
		case *Trace:
			w.code(x.Name, ` `)
		default:
			panic("unexpected result type")
		}
	}
}

func (w *Writer) funcResults(fn *Func) {
	w.funcResultsFlags(fn, 0)
}

func (w *Writer) funcSignatureFlags(fn *Func, flags flags) {
	haveNames := haveNames(fn.Params)
	w.code(`func(`)
	for i := range fn.Params {
		if i > 0 {
			w.code(`, `)
		}
		if flags.has(docs) && haveNames {
			w.funcParamSign(&fn.Params[i])
		} else {
			w.code(w.typeString(fn.Params[i].Type))
		}
	}
	w.code(`)`)
	if fn.HasResult() {
		if fn.isFuncResult() {
			w.code(` `)
		}
		w.funcResultsFlags(fn, flags)
	}
}

func (w *Writer) funcSignature(fn *Func) {
	w.funcSignatureFlags(fn, 0)
}

func (w *Writer) shortcutFuncSignFlags(fn *Func, flags flags) {
	var (
		params    = flattenParams(fn.Params)
		haveNames = haveNames(params)
	)
	w.code(`func(`)
	for i := range params {
		if i > 0 {
			w.code(`, `)
		}
		if flags.has(docs) && haveNames {
			w.funcParamSign(&params[i])
		} else {
			w.code(w.typeString(params[i].Type))
		}
	}
	w.code(`)`)
	if fn.HasResult() {
		if fn.isFuncResult() {
			w.code(` `)
		}
		w.shortcutFuncResultsFlags(fn, flags)
	}
}

func (w *Writer) shortcutFuncResultsFlags(fn *Func, flags flags) {
	for _, r := range fn.Result {
		switch x := r.(type) {
		case *Func:
			w.shortcutFuncSignFlags(x, flags)
		case *Trace:
			w.code(x.Name, ` `)
		default:
			panic("unexpected result type")
		}
	}
}

func (w *Writer) shortcutFuncResults(fn *Func) {
	w.shortcutFuncResultsFlags(fn, 0)
}

func haveNames(params []Param) bool {
	for i := range params {
		name := nameParam(&params[i])
		if len(name) > 1 && !isPredeclared(name) {
			return true
		}
	}
	return false
}

func (w *Writer) typeString(t types.Type) string {
	return types.TypeString(t, func(pkg *types.Package) string {
		if pkg.Path() == w.pkg.Path() {
			return "" // same package; unqualified
		}
		return pkg.Name()
	})
}

func (w *Writer) block(fn func()) {
	w.depth++
	w.newScope(fn)
	w.depth--
}

func (w *Writer) newScope(fn func()) {
	w.scope.PushBack(new(scope))
	fn()
	w.scope.Remove(w.scope.Back())
}

func (w *Writer) line(args ...string) {
	w.code(args...)
	_ = w.bw.WriteByte('\n')
	w.atEOL = true
}

func (w *Writer) code(args ...string) {
	if w.atEOL {
		for i := 0; i < w.depth; i++ {
			_ = w.bw.WriteByte('\t')
		}
		w.atEOL = false
	}
	for _, arg := range args {
		_, _ = w.bw.WriteString(arg)
	}
}

func exported(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		panic("invalid string")
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

func unexported(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		panic("invalid string")
	}
	return string(unicode.ToLower(r)) + s[size:]
}

func firstChar(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		panic("invalid string")
	}
	return string(r)
}

func ident(s string) string {
	// Identifier must not begin with number.
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError {
			panic("invalid string")
		}
		if !unicode.IsNumber(r) {
			break
		}
		s = s[size:]
	}

	// Filter out non letter/number/underscore characters.
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '_' ||
			unicode.IsLetter(r) ||
			unicode.IsNumber(r):

			return r
		default:
			return -1
		}
	}, s)

	if !token.IsIdentifier(s) {
		s = "_" + s
	}

	return s
}

func tempName(names ...string) string {
	var sb strings.Builder
	for i, name := range names {
		if i == 0 {
			name = unexported(name)
		} else {
			name = exported(name)
		}
		sb.WriteString(name)
	}
	return sb.String()
}

type decl struct {
	where string
}

type scope struct {
	vars map[string]decl
}

func (s *scope) set(v string) bool {
	if s.vars == nil {
		s.vars = make(map[string]decl)
	}
	if _, has := s.vars[v]; has {
		return false
	}
	_, file, line, _ := runtime.Caller(2)
	s.vars[v] = decl{
		where: fmt.Sprintf("%s:%d", file, line),
	}
	return true
}

func (s *scope) where(v string) string {
	d := s.vars[v]
	return d.where
}
