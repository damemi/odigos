package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// detectorGlob matches every provider discovery package. Discovery lives in four separate Go modules,
// so no single compiler invocation sees them all; the files are read as source from here instead.
const detectorGlob = "../providers/*/pkg/*/detector/*.go"

// TestPruneCutoffNeverComesFromTheProcessClock guards the one invariant that ties a discovery cycle to
// this package's schema: inventory rows record last_seen_at with Postgres's now(), so the prune cutoff
// has to be read from the database too (base/store.(*Store).Now).
//
// A cutoff taken from the connector pod's own clock compares two clocks on two hosts. With Postgres even
// slightly behind the pod, every row upserted during the scan carries a last_seen_at older than the
// cutoff, and the prune soft-deletes resources that are still running. Every read path filters
// `deleted_at IS NULL`, so those resources vanish from the UI and the instrumentor stops reconciling
// them — enabling or disabling instrumentation on them then silently does nothing.
//
// This is a source-level check rather than a behavioural one because the defect it guards is a missed
// call site, not a wrong algorithm: 15454dff9 converted nine detectors onto the database clock and left
// two behind, and nothing failed. A detector's Detect holds a concrete *store.Store wrapping a live
// pool, so the cheap behavioural test does not exist; this one costs nothing and covers every provider
// at once, including the next detector somebody adds.
func TestPruneCutoffNeverComesFromTheProcessClock(t *testing.T) {
	files, err := filepath.Glob(detectorGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", detectorGlob, err)
	}
	var sources []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			sources = append(sources, f)
		}
	}
	if len(sources) == 0 {
		// Deliberately fatal rather than skipped, matching TestExpectedSchemaVersionsMatchMigrations: a
		// silent skip would switch this guard off exactly when the layout moves.
		t.Fatalf("no detector sources matched %s; this test has stopped checking anything", detectorGlob)
	}
	sort.Strings(sources)

	fset := token.NewFileSet()
	for _, path := range sources {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		// Per function, so two functions in one file that reuse a variable name stay independent.
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for _, pos := range processClockPrunes(fn) {
				t.Errorf("%s: %s takes its prune cutoff from the process clock; read it from the database "+
					"with store.Now(ctx), the clock last_seen_at is written with", fset.Position(pos), fn.Name.Name)
			}
		}
	}
}

// processClockPrunes returns the position of every Prune call in fn whose cutoff came from time.Now(),
// either through a local variable or written inline.
func processClockPrunes(fn *ast.FuncDecl) []token.Pos {
	fromProcessClock := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		var lhs, rhs []ast.Expr
		switch a := n.(type) {
		case *ast.AssignStmt:
			lhs, rhs = a.Lhs, a.Rhs
		default:
			return true
		}
		if len(lhs) != len(rhs) {
			// A multi-value call such as `a, b := f()`; no operand is time.Now() on its own.
			return true
		}
		for i, expr := range rhs {
			if !isTimeNow(expr) {
				continue
			}
			if name, ok := lhs[i].(*ast.Ident); ok {
				fromProcessClock[name.Name] = true
			}
		}
		return true
	})

	var found []token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		// Prune* only. PurgeDeleted is a retention sweep measured in days against deleted_at, where the
		// difference between two hosts' clocks cannot reach the cutoff.
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Prune") {
			return true
		}
		for _, arg := range call.Args {
			if isTimeNow(arg) {
				found = append(found, call.Pos())
				return true
			}
			if name, ok := arg.(*ast.Ident); ok && fromProcessClock[name.Name] {
				found = append(found, call.Pos())
				return true
			}
		}
		return true
	})
	return found
}

// isTimeNow reports whether expr is the call time.Now().
func isTimeNow(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Now" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}
