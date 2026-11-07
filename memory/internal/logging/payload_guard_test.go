package logging

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// payloadKeys are zap field keys that carry a request body, a SQL text, query
// parameters or a value. Above Debug they are forbidden (AGENT-1093): log their size with
// Size or SizeOf, and their content with Debug or DebugValue.
var payloadKeys = map[string]bool{
	"arguments": true, "raw_arguments": true, "args": true, "sql": true, "ddl": true,
	"params": true, "query": true, "payload": true, "subject": true, "summary": true,
	"content": true, "body": true, "text": true, "data": true, "value": true, "values": true,
	"input": true, "output": true, "result": true, "response": true, "request": true,
}

// notFieldCtors are the zap functions that are not field constructors; every other
// function of the package called with a key first is a field, whatever it carries.
var notFieldCtors = map[string]bool{"Skip": true}

func isFieldCtor(name string) bool {
	return !notFieldCtors[name] && !strings.HasPrefix(name, "New") && !strings.HasPrefix(name, "Replace") && !strings.HasPrefix(name, "Redirect") && !strings.HasPrefix(name, "Must")
}

// logMethods are the logger methods of every level; the guard only accepts a payload
// field inside a call to Debug.
var logMethods = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true, "DPanic": true, "Panic": true, "Fatal": true,
	"Debugf": true, "Infof": true, "Warnf": true, "Errorf": true,
	"Debugw": true, "Infow": true, "Warnw": true, "Errorw": true,
}

type payloadLog struct{ pos, call, key string }

// TestNoPayloadAboveDebug walks the Go files of the module and fails on a zap field
// with a payload key that is not built inside a call to Debug. It keeps AGENT-1093
// (SQL, parameters and arguments at Info and above) from coming back.
func TestNoPayloadAboveDebug(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []payloadLog
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The helpers build fields from a key they are given, after their own Debug
		// check or after masking and cutting: they are the sanctioned way to log content.
		if strings.HasSuffix(filepath.ToSlash(path), "internal/logging/payload.go") {
			return nil
		}
		source, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// Key-value pairs of a sugared logger (Infow("m", "sql", x)) are not zap fields:
		// the module does not use it, and the guard keeps it that way.
		if strings.Contains(string(source), "zap.SugaredLogger") || strings.Contains(string(source), ".Sugar()") {
			found = append(found, payloadLog{pos: filepath.ToSlash(path), call: "Sugar", key: "<sugared logger>"})
			return nil
		}
		file, perr := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		found = append(found, scanFile(fset, file, filepath.ToSlash(path))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(found) == 0 {
		return
	}
	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	lines := make([]string, 0, len(found))
	for _, f := range found {
		lines = append(lines, fmt.Sprintf("  %s: %s(%q)", f.pos, f.call, f.key))
	}
	t.Fatalf("%d payload field(s) logged above Debug; log logging.Size at Info and the content with logging.Debug:\n%s",
		len(found), strings.Join(lines, "\n"))
}

func scanFile(fset *token.FileSet, file *ast.File, rel string) []payloadLog {
	var found []payloadLog
	var stack []ast.Node
	zapName := zapImportName(file)
	if zapName == "" {
		zapName = "\x00" // no zap import: the logging helpers are still checked
	}
	if zapName == "." {
		// Constructors called without a package name cannot be told from other calls.
		return []payloadLog{{pos: fmt.Sprintf("%s:%d", rel, fset.Position(file.Pos()).Line), call: "import", key: "<dot import of zap>"}}
	}
	consts := stringConsts(file)
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		key, bad := payloadField(n, zapName, consts)
		if !bad {
			key, bad = loggingTextField(n, consts)
		}
		if !bad {
			key, bad = fieldCtorValue(n, zapName, stack)
		}
		if !bad {
			return true
		}
		call := enclosingLogCall(stack[:len(stack)-1])
		if call == "Debug" {
			return true
		}
		found = append(found, payloadLog{pos: fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line), call: call, key: key})
		return true
	})
	return found
}

// loggingTextField matches logging.Text("sql", …): the helper masks secrets, it does not
// make a payload key safe above Debug.
func loggingTextField(n ast.Node, consts map[string]string) (string, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Text" {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "logging" {
		return "", false
	}
	switch key := call.Args[0].(type) {
	case *ast.BasicLit:
		value, _ := strconv.Unquote(key.Value)
		return value, payloadKeys[strings.ToLower(value)]
	case *ast.Ident:
		if value, ok := consts[key.Name]; ok {
			return value, payloadKeys[strings.ToLower(value)]
		}
	}
	return "<dynamic>", true
}

// fieldCtorValue matches a zap constructor used as a value (field := zap.String): the
// call through the variable would hide its key from the guard.
func fieldCtorValue(n ast.Node, zapName string, stack []ast.Node) (string, bool) {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok || !isFieldCtor(sel.Sel.Name) {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != zapName {
		return "", false
	}
	if len(stack) >= 2 {
		if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == n {
			return "", false // an ordinary call: payloadField looks at its key
		}
	}
	// zap.Field in a type position (l *zap.Logger, []zap.Field) is not a constructor value.
	if sel.Sel.Name == "Field" || sel.Sel.Name == "Logger" || sel.Sel.Name == "Option" {
		return "", false
	}
	return "<func value zap." + sel.Sel.Name + ">", true
}

// zapImportName returns the name the file gives to go.uber.org/zap ("" when it does not
// import it): an alias does not hide a field from the guard.
//
// The guard is a safety net against a log line coming back by accident, not a Go type
// checker: it does not follow a local variable that shadows a constant, nor a field
// built in another package. TestNoPayloadInLogsAboveDebug is the check on behaviour.
func zapImportName(file *ast.File) string {
	for _, imp := range file.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path != "go.uber.org/zap" {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "zap"
	}
	return ""
}

// stringConsts returns the string constants declared in the file, to resolve a key held in
// a constant.
func stringConsts(file *ast.File) map[string]string {
	consts := map[string]string{}
	ambiguous := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true // a variable can be reassigned: its key is dynamic
		}
		for _, spec := range decl.Specs {
			spec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range spec.Names {
				if i >= len(spec.Values) {
					break
				}
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, _ := strconv.Unquote(lit.Value)
					if previous, seen := consts[name.Name]; seen && previous != value {
						delete(consts, name.Name) // two scopes, two values: the key is ambiguous
						ambiguous[name.Name] = true
						continue
					}
					if !ambiguous[name.Name] {
						consts[name.Name] = value
					}
				}
			}
		}
		return true
	})
	return consts
}

// payloadField matches a content zap constructor whose key is a payload key, or whose
// key cannot be resolved to a literal or a constant of the file (a dynamic key could be
// anything). Only calls inside Debug are accepted by the caller.
func payloadField(n ast.Node, zapName string, consts map[string]string) (string, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !isFieldCtor(sel.Sel.Name) {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != zapName {
		return "", false
	}
	switch key := call.Args[0].(type) {
	case *ast.BasicLit:
		if key.Kind != token.STRING {
			return "", false
		}
		value, _ := strconv.Unquote(key.Value)
		return value, payloadKeys[strings.ToLower(value)]
	case *ast.Ident:
		if value, ok := consts[key.Name]; ok {
			return value, payloadKeys[strings.ToLower(value)]
		}
		return "<dynamic:" + key.Name + ">", true
	default:
		return "<dynamic>", true
	}
}

// enclosingLogCall returns the method name of the nearest enclosing log call, or "" when
// the field is built outside any (kept in a variable or a slice).
func enclosingLogCall(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		call, ok := stack[i].(*ast.CallExpr)
		if !ok {
			continue
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && logMethods[sel.Sel.Name] {
			return sel.Sel.Name
		}
	}
	return ""
}

func TestPayloadGuardDetectsKnownShapes(t *testing.T) {
	src := `package x
import (
	"go.uber.org/zap"
	"x/logging"
)
func f(l *zap.Logger, sql string, a map[string]any) {
	l.Info("a", zap.String("sql", sql))
	l.Warn("b", zap.Any("params", a))
	fields := []zap.Field{zap.String("query", sql)}
	l.Error("c", fields...)
	l.Debug("ok", zap.String("sql", sql))
	l.Info("fine", zap.Int("sql_bytes", len(sql)), zap.String("tool", "t"))
	const k = "sql"
	l.Info("const", zap.String(k, sql))
	l.Info("dynamic", zap.String(sql, sql))
	l.Info("number", zap.Int("params", 1))
	l.Info("object", zap.Object("arguments", nil))
	key := "tool"
	key = "sql"
	l.Info("reassigned", zap.String(key, sql))
	var vkey = "tool"
	l.Info("var", zap.String(vkey, sql))
	l.Info("text", logging.Text("sql", sql))
	field := zap.String
	l.Info("alias", field("sql", sql))
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := scanFile(fset, file, "x.go")
	var keys []string
	for _, g := range got {
		keys = append(keys, g.call+":"+g.key)
	}
	want := "Info:sql,Warn:params,:query,Info:sql,Info:<dynamic:sql>,Info:params,Info:arguments,Info:<dynamic:key>,Info:<dynamic:vkey>,Info:sql,:<func value zap.String>"
	sort.Strings(keys)
	w := strings.Split(want, ",")
	sort.Strings(w)
	if strings.Join(keys, ",") != strings.Join(w, ",") {
		t.Fatalf("guard found %v, want %v", keys, w)
	}
}

func TestPayloadGuardChecksLoggingTextWithoutAZapImport(t *testing.T) {
	src := `package x
import "x/logging"
func f() { _ = logging.Text("sql", "x") }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scanFile(fset, file, "x.go"); len(got) != 1 {
		t.Fatalf("guard found %v, want logging.Text(\"sql\") flagged", got)
	}
}

func TestPayloadGuardRejectsADotImportOfZap(t *testing.T) {
	src := `package x
import . "go.uber.org/zap"
func f(l *Logger) { l.Info("a", String("sql", "x")) }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scanFile(fset, file, "x.go"); len(got) != 1 {
		t.Fatalf("guard found %v, want the dot import flagged", got)
	}
}

func TestPayloadGuardSeesAnAliasedZapImport(t *testing.T) {
	src := `package x
import z "go.uber.org/zap"
func f(l *z.Logger, sql string) {
	l.Info("a", z.String("sql", sql))
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scanFile(fset, file, "x.go"); len(got) != 1 || got[0].key != "sql" {
		t.Fatalf("guard found %v, want the aliased z.String(\"sql\")", got)
	}
}

func TestPayloadGuardDoesNotTrustAConstOfAnotherScope(t *testing.T) {
	src := `package x
import "go.uber.org/zap"
func f(l *zap.Logger, sql string) { const k = "sql"; l.Info("a", zap.String(k, sql)) }
func g() { const k = "tool"; _ = k }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scanFile(fset, file, "x.go"); len(got) != 1 {
		t.Fatalf("guard found %v, want the ambiguous key flagged", got)
	}
}
