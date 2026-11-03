package main

// The guard for wall-clock times (the same rule as Rubi core's internal/wallclock guard): TestNoMonotonicClockOnWallDeadlines
// parses every non-test Go file of the plugin and fails on a time.Now() (with its monotonic reading) stored where the time must follow the
// wall clock.
//
// The rule (syntactic, no type information, so it stays fast and has no dependencies):
//
//   - A "monotonic expression" is a call time.Now(), or time.Now() followed only by .Add(...) or .AddDate(...)
//     (they keep the reading). .UTC(), .Local(), .In(), .Round(0), .Truncate(0), .Unix*() and .Format() drop
//     it, so time.Now().UTC() and time.Now().Unix() are fine. The function value time.Now (not called) counts
//     too, because a clock field set to it hands out times with the reading.
//   - A "wall name" is a variable, struct field or map/slice element whose name, lower-cased, contains one of
//     expir, until, deadline, valid, lease, since, ttl, now, clock, fresh, window, or has "at" as a word of its
//     camelCase or snake_case name (At, CreatedAt, lastAt, at_time; not "state" or "format").
//   - It is a finding when a monotonic expression is the value assigned or declared (=, :=, var) to a wall
//     name, is the value of a keyed field of a composite literal with a wall name, or is any element of a
//     positional composite literal (the field's name is not known there).
//   - Not covered: a time passed as a call argument (the callee's parameter is checked where it is stored).
//
// Allowed only through the list below: one entry per "file:function:name" with the reason it measures elapsed
// run time inside one process. An entry that no longer matches anything fails the test, so the list can't rot.
// Everything else uses wallClock() (binding.go), which drops the monotonic reading.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
)

// Each entry is a local "give up after N seconds" budget inside one process: it measures elapsed run time,
// nothing is stored, shown or compared with another computer's time. None yet.
var allowed = map[string]string{}

type finding struct {
	pos  token.Position
	fn   string
	name string
}

func (f finding) key(root string) string {
	rel, _ := filepath.Rel(root, f.pos.Filename)
	return filepath.ToSlash(rel) + ":" + f.fn + ":" + f.name
}

var wallWords = []string{"expir", "until", "deadline", "valid", "lease", "since", "ttl", "now", "clock", "fresh", "window"}

func isWallName(name string) bool {
	l := strings.ToLower(name)
	for _, w := range wallWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	// words of the name: split at '_' and before an upper-case letter that follows a lower-case one
	var words []string
	cur := []rune{}
	rs := []rune(name)
	for i, r := range rs {
		if r == '_' {
			words, cur = append(words, string(cur)), cur[:0]
			continue
		}
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(rs[i-1]) {
			words, cur = append(words, string(cur)), cur[:0]
		}
		cur = append(cur, r)
	}
	words = append(words, string(cur))
	for _, w := range words {
		if strings.EqualFold(w, "at") {
			return true
		}
	}
	return false
}

// isMono reports whether e is a time that still has the monotonic reading.
func isMono(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return isMono(x.X)
	case *ast.SelectorExpr: // the function value time.Now
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "time" && x.Sel.Name == "Now"
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && sel.Sel.Name == "Now" {
			return true
		}
		if sel.Sel.Name == "Add" || sel.Sel.Name == "AddDate" {
			return isMono(sel.X)
		}
	}
	return false
}

func targetName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return targetName(x.X)
	case *ast.StarExpr:
		return targetName(x.X)
	case *ast.ParenExpr:
		return targetName(x.X)
	}
	return ""
}

func scan(fset *token.FileSet, f *ast.File) []finding {
	var out []finding
	add := func(pos token.Pos, fn, name string) {
		out = append(out, finding{fset.Position(pos), fn, name})
	}
	var visit func(n ast.Node, fn string)
	visit = func(n ast.Node, fn string) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Body != nil {
					visit(x.Body, x.Name.Name)
				}
				return false
			case *ast.AssignStmt:
				if len(x.Lhs) == len(x.Rhs) {
					for i := range x.Lhs {
						if name := targetName(x.Lhs[i]); name != "" && isWallName(name) && isMono(x.Rhs[i]) {
							add(x.Pos(), fn, name)
						}
					}
				}
			case *ast.ValueSpec:
				if len(x.Names) == len(x.Values) {
					for i := range x.Names {
						if isWallName(x.Names[i].Name) && isMono(x.Values[i]) {
							add(x.Pos(), fn, x.Names[i].Name)
						}
					}
				}
			case *ast.CompositeLit:
				for _, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if name := targetName(kv.Key); name != "" && isWallName(name) && isMono(kv.Value) {
							add(kv.Pos(), fn, name)
						}
					} else if isMono(el) {
						add(el.Pos(), fn, "(positional)")
					}
				}
			}
			return true
		})
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			visit(fd, fd.Name.Name)
		} else {
			visit(d, "(package)")
		}
	}
	return out
}

func scanSource(t *testing.T, src string) []finding {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return scan(fset, f)
}

// The guard itself, checked on snippets: what it must flag and what it must leave alone.
func TestGuardRule(t *testing.T) {
	bad := []string{
		`package p; import "time"; func f() { expires := time.Now().Add(time.Hour); _ = expires }`,
		`package p; import "time"; type s struct{ Expires time.Time }; func f() { _ = s{Expires: time.Now().Add(1)} }`,
		`package p; import "time"; func f(x *struct{ CreatedAt time.Time }) { x.CreatedAt = time.Now() }`,
		`package p; import "time"; func f() { var deadline = time.Now(); _ = deadline }`,
		`package p; import "time"; func f(lastAt map[string]time.Time) { lastAt["a"] = time.Now() }`,
		`package p; import "time"; type pin struct{ k string; t time.Time }; func f() { _ = pin{"k", time.Now()} }`,
		`package p; import "time"; type o struct{ Now func() time.Time }; func f() { _ = o{Now: time.Now} }`,
		`package p; import "time"; var processStart = time.Now()` + "\n" + `var validUntil = time.Now().Add(1)`,
	}
	for _, src := range bad {
		if len(scanSource(t, src)) == 0 {
			t.Errorf("not flagged: %s", src)
		}
	}
	good := []string{
		`package p; import "time"; func f() { expires := time.Now().Add(time.Hour).UTC(); _ = expires }`,
		`package p; import "time"; func f() { expires := time.Now().Round(0); _ = expires }`,
		`package p; import "time"; func f() { deadline := wallClock().Add(1); _ = deadline }`,
		`package p; import "time"; func f() { start := time.Now(); _ = start }`,
		`package p; import "time"; func f() { state := time.Now(); _ = state }`,
		`package p; import "time"; func f() { _ = time.Now().Unix() }`,
		`package p; import "time"; func f(c interface{ SetDeadline(time.Time) error }) { _ = c.SetDeadline(time.Now().Add(1)) }`,
	}
	for _, src := range good {
		if fs := scanSource(t, src); len(fs) != 0 {
			t.Errorf("flagged by mistake: %s -> %v", src, fs)
		}
	}
}

func TestNoMonotonicClockOnWallDeadlines(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var found []finding
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "docs", "scripts":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, b, 0)
		if err != nil {
			return err
		}
		found = append(found, scan(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	var bad []string
	for _, f := range found {
		k := f.key(root)
		if _, ok := allowed[k]; ok {
			used[k] = true
			continue
		}
		bad = append(bad, fmt.Sprintf("%s:%d: %s gets a time.Now() with its monotonic reading; use wallClock() (or add %q to the allow-list with the reason it measures elapsed run time)", filepath.ToSlash(strings.TrimPrefix(f.pos.Filename, root+"/")), f.pos.Line, f.name, k))
	}
	for k := range allowed {
		if !used[k] {
			bad = append(bad, "stale allow-list entry (matches nothing): "+k)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("%d problem(s):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// The plugin's clock has no monotonic reading, and a trust or mandate checked against it follows the wall
// clock: an hour of wall time passing (as in a sleep) expires it.
func TestWallClockHasNoMonotonicReadingAndExpiryFollowsIt(t *testing.T) {
	now := wallClock()
	if strings.Contains(time.Now().String(), "m=") && strings.Contains(now.String(), "m=") {
		t.Fatalf("wallClock() carries a monotonic reading: %s", now)
	}
	m := mandate{Expires: now.Add(time.Hour), MaxUses: 1}
	if !m.live(now) {
		t.Fatal("a fresh mandate should be live")
	}
	if m.live(now.Add(time.Hour + time.Second)) {
		t.Fatal("a mandate past its expiry should not be live")
	}
}
