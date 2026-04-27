package run_test

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/run"
)

// Test_RunOptions_StructContract pins the RunOptions plumbing contract:
// ctx, stdin, stdout, stderr, and logger fields MUST all be present, and
// no stale `WithIO(args, stdout, stderr)` signature may remain anywhere
// in the tree.
func Test_RunOptions_StructContract(t *testing.T) {
	// 1. RunOptions struct has the required fields.
	type requiredField struct {
		name string
		kind string // short type-name check; exact type may differ by import
	}
	want := []requiredField{
		{"Args", "[]string"},
		{"Ctx", "context.Context"},
		{"Stdin", "io.Reader"},
		{"Stdout", "io.Writer"},
		{"Stderr", "io.Writer"},
		{"Logger", "*logx.Logger"},
	}

	typ := reflect.TypeOf(run.RunOptions{})
	for _, w := range want {
		f, ok := typ.FieldByName(w.name)
		if !ok {
			t.Errorf("RunOptions is missing required field %q", w.name)
			continue
		}
		gotKind := f.Type.String()
		// A loose contains check; Go prints io.Reader etc. with package
		// prefixes that vary, but the short name is stable.
		if !strings.Contains(gotKind, shortKind(w.kind)) {
			t.Errorf("RunOptions.%s = %s; want something matching %q",
				w.name, gotKind, w.kind)
		}
	}

	// 2. run.Run accepts []string and returns int (entrypoint stays stable
	// for os.Args callers).
	runFn := reflect.ValueOf(run.Run)
	if runFn.Kind() != reflect.Func {
		t.Fatalf("run.Run is not a function")
	}
	rt := runFn.Type()
	if rt.NumIn() != 1 || rt.In(0).String() != "[]string" {
		t.Errorf("run.Run signature changed: got in=%v, want ([]string)", paramTypes(rt))
	}
	if rt.NumOut() != 1 || rt.Out(0).Kind() != reflect.Int {
		t.Errorf("run.Run signature changed: got out=%v, want (int)", returnTypes(rt))
	}

	// 3. run.Execute accepts a RunOptions and returns int. This is the
	// new in-process entrypoint that replaces the old WithIO(args, stdout,
	// stderr) three-argument form.
	exec := reflect.ValueOf(run.Execute)
	if exec.Kind() != reflect.Func {
		t.Fatalf("run.Execute is not a function — RunOptions-based entrypoint is required")
	}
	et := exec.Type()
	if et.NumIn() != 1 || et.In(0).String() != "run.RunOptions" {
		t.Errorf("run.Execute signature: got in=%v, want (run.RunOptions)", paramTypes(et))
	}
	if et.NumOut() != 1 || et.Out(0).Kind() != reflect.Int {
		t.Errorf("run.Execute signature: got out=%v, want (int)", returnTypes(et))
	}

	// 4. Sanity: Execute with minimal options and a bad arg count exits
	// 1 and writes nothing crazy. This is a plumbing-check, not a
	// behavioural one.
	var stdout, stderr bytes.Buffer
	opts := run.RunOptions{
		Args:   []string{}, // no file — should be an error, exit 1
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	}
	code := run.Execute(opts)
	if code == 0 {
		t.Errorf("run.Execute with no file should exit non-zero, got 0")
	}

	// 5. Static check: no stale `WithIO(args, stdout, stderr)` signature
	// lingers anywhere under the module root. Scans the Go AST for a
	// FuncDecl named WithIO with exactly three parameters (args, stdout,
	// stderr). We intentionally scan the whole tree — both internal and
	// cmd — so any accidental resurrection fails CI.
	assertNoStaleWithIO(t)
}

func shortKind(s string) string {
	// For e.g. "context.Context" we want "Context" to survive
	// package-qualifier variations. For "[]string" / "io.Reader" etc.,
	// take the substring after the last "." if any.
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

func paramTypes(rt reflect.Type) []string {
	out := make([]string, rt.NumIn())
	for i := range out {
		out[i] = rt.In(i).String()
	}
	return out
}

func returnTypes(rt reflect.Type) []string {
	out := make([]string, rt.NumOut())
	for i := range out {
		out[i] = rt.Out(i).String()
	}
	return out
}

// assertNoStaleWithIO walks ../../ (module root relative to internal/run)
// and fails if any .go file defines a function named WithIO with three
// parameters — the old STORY-0002 signature. We tolerate WithIO as a
// method or variable; only the free function signature is forbidden.
func assertNoStaleWithIO(t *testing.T) {
	t.Helper()
	// Module root: walk up until we find go.mod.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := findModuleRoot(t, wd)

	fset := token.NewFileSet()
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(p)
			if base == "vendor" || base == ".git" || base == "bin" || base == "site" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		if strings.HasSuffix(p, "_test.go") {
			return nil // test files may reference the name in assertions
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // don't fail the audit on parse errors in unrelated files
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name != "WithIO" {
				continue
			}
			if fd.Recv != nil { // method is fine
				continue
			}
			if fd.Type == nil || fd.Type.Params == nil {
				continue
			}
			// Count actual parameter positions (each Field may declare
			// multiple names — count names, not fields).
			n := 0
			for _, field := range fd.Type.Params.List {
				if len(field.Names) == 0 {
					n++
				} else {
					n += len(field.Names)
				}
			}
			if n == 3 {
				t.Errorf("stale WithIO(args, stdout, stderr) signature found at %s — STORY-0003 refactor requires RunOptions", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func findModuleRoot(t *testing.T, start string) string {
	t.Helper()
	p := start
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	t.Fatalf("could not find go.mod walking up from %s", start)
	return ""
}

// Compile-time assertions that the option field types are interface-
// level what the contract says they are. These won't build if the
// struct fields aren't the right interface types.
var _ = func() bool {
	var o run.RunOptions
	var _ context.Context = o.Ctx //nolint:staticcheck // intentional: compile-time interface-assignability check
	var _ io.Reader = o.Stdin     //nolint:staticcheck // intentional: compile-time interface-assignability check
	var _ io.Writer = o.Stdout    //nolint:staticcheck // intentional: compile-time interface-assignability check
	var _ io.Writer = o.Stderr    //nolint:staticcheck // intentional: compile-time interface-assignability check
	var _ *logx.Logger = o.Logger //nolint:staticcheck // intentional: compile-time interface-assignability check
	return true
}
