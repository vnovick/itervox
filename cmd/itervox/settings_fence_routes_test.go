package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// parseNonTestGo parses every non-test .go file in dir.
func parseNonTestGo(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	return files
}

// methodsOf returns name -> body for methods whose receiver type (pointer or
// not) is recvType, plus each method's receiver identifier.
func methodsOf(files []*ast.File, recvType string) (map[string]*ast.BlockStmt, map[string]string) {
	bodies := map[string]*ast.BlockStmt{}
	recvs := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 || fd.Body == nil {
				continue
			}
			typ := fd.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); !ok || id.Name != recvType {
				continue
			}
			bodies[fd.Name.Name] = fd.Body
			if len(fd.Recv.List[0].Names) == 1 {
				recvs[fd.Name.Name] = fd.Recv.List[0].Names[0].Name
			}
		}
	}
	return bodies, recvs
}

// fixpoint returns the methods that call a seed method or, transitively, a
// method already in the set, where "call" is decided by calls(body, recv, set).
func fixpoint(bodies map[string]*ast.BlockStmt, recvs map[string]string, seed map[string]bool,
	calls func(body *ast.BlockStmt, recv string, set map[string]bool) bool) map[string]bool {
	set := map[string]bool{}
	for k := range seed {
		set[k] = true
	}
	for changed := true; changed; {
		changed = false
		for name, body := range bodies {
			if !set[name] && calls(body, recvs[name], set) {
				set[name] = true
				changed = true
			}
		}
	}
	return set
}

// fencedFuncsInMain returns the method names (any receiver type) of package
// main functions that reach beginSettingsSave: a function or method is fenced
// when it calls beginSettingsSave(...) / x.beginSettingsSave(), or — closed
// transitively — a fenced package function f(...) or a fenced method x.M(...).
// Matching methods by name across receivers is deliberately conservative: a
// false positive can only demand an extra 503 table row.
func fencedFuncsInMain(files []*ast.File) map[string]bool {
	type fn struct {
		key    string // "M" for a method (by name), "func:f" for a function
		body   *ast.BlockStmt
		method bool
	}
	var fns []fn
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name.Name == "beginSettingsSave" {
				continue
			}
			if fd.Recv != nil {
				fns = append(fns, fn{fd.Name.Name, fd.Body, true})
			} else {
				fns = append(fns, fn{"func:" + fd.Name.Name, fd.Body, false})
			}
		}
	}
	set := map[string]bool{"func:beginSettingsSave": true, "beginSettingsSave": true}
	calls := func(body *ast.BlockStmt) bool {
		found := false
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				found = found || set["func:"+f.Name]
			case *ast.SelectorExpr:
				found = found || set[f.Sel.Name]
			}
			return !found
		})
		return found
	}
	for changed := true; changed; {
		changed = false
		for _, f := range fns {
			if !set[f.key] && calls(f.body) {
				set[f.key] = true
				changed = true
			}
		}
	}
	out := map[string]bool{}
	for k := range set {
		if !strings.HasPrefix(k, "func:") && k != "beginSettingsSave" {
			out[k] = true
		}
	}
	return out
}

// TestEveryFencedSettingsRouteIsInReloadFenceTable makes the route coverage of
// internal/server's TestSettingsSaveRefusedByReloadFenceIs503 mechanical
// (M1-close E1: the hand-written table missed POST /skills/fix, which reaches
// the fence through ApplyFix -> UpsertProfile). From source, not a list:
//
//  1. fenced methods = every package main method (any receiver) or function
//     that calls beginSettingsSave, closed over calls to a fenced one
//     (fencedFuncsInMain);
//  2. fenced handlers = every *Server method calling X.M(...) with M fenced on
//     any expression X other than the Server itself (s.client, s.skills,
//     s.projectManager, a type-asserted capability), closed over *Server
//     methods calling a fenced one;
//  3. each fenced handler's routes, from the r.<Verb>("path", s.handleX)
//     registrations under /api/v1;
//  4. every such route must match a row of the 503 table.
//
// A new fenced setter, a new handler reaching one, or a handler registered in
// a form this test cannot see (no route found) fails here until the table —
// which proves the 503 — covers it.
func TestEveryFencedSettingsRouteIsInReloadFenceTable(t *testing.T) {
	// 1. fenced cmd/itervox functions and methods (M4-close D5: not only
	// *orchestratorAdapter methods — linearProjectManager.SetProjectFilter
	// reaches the fence through the package function saveProjectFilter).
	fencedAdapter := fencedFuncsInMain(parseNonTestGo(t, "."))
	for _, must := range []string{"SetWorkers", "UpsertProfile", "ApplyFix", "SetFailedState", "SetProjectFilter", "RefreshAvailableModels"} {
		if !fencedAdapter[must] {
			t.Fatalf("fenced method set is missing %s — the AST walk is broken: %v", must, fencedAdapter)
		}
	}

	// 2. fenced handlers
	serverDir := filepath.Join("..", "..", "internal", "server")
	serverFiles := parseNonTestGo(t, serverDir)
	srvBodies, srvRecvs := methodsOf(serverFiles, "Server")
	callsClient := func(body *ast.BlockStmt, recv string, set map[string]bool) bool {
		found := false
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// s.<any field>.M(...) with M fenced — s.client, s.skills,
			// s.projectManager (M4-close D5) — and x.M(...) on any other
			// expression, which covers a capability obtained by type
			// assertion (refresher := s.client.(ModelRefresher);
			// refresher.RefreshAvailableModels(...), handleRefreshModels).
			if fencedAdapter[sel.Sel.Name] {
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != recv {
					found = true
				}
			}
			// s.helper(...) where helper is already a fenced handler/helper
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv && set[sel.Sel.Name] {
				found = true
			}
			return !found
		})
		return found
	}
	fencedHandlers := fixpoint(srvBodies, srvRecvs, map[string]bool{}, callsClient)
	if len(fencedHandlers) < 17 {
		t.Fatalf("only %d fenced handlers found — the AST walk is broken: %v", len(fencedHandlers), fencedHandlers)
	}

	// 3. routes of fenced handlers
	type route struct{ method, pattern string }
	var routes []route
	routed := map[string]bool{}
	for _, f := range serverFiles {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			verb := strings.ToUpper(sel.Sel.Name)
			switch verb {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			h, ok := call.Args[1].(*ast.SelectorExpr)
			if !ok || !fencedHandlers[h.Sel.Name] {
				return true
			}
			routes = append(routes, route{verb, "/api/v1" + strings.Trim(lit.Value, `"`)})
			routed[h.Sel.Name] = true
			return true
		})
	}
	for h := range fencedHandlers {
		if strings.HasPrefix(h, "handle") && !routed[h] {
			t.Errorf("fenced handler %s has no r.<Verb>(path, s.%s) registration this test can see — register it that way or extend the test", h, h)
		}
	}

	// 4. table rows
	table, err := os.ReadFile(filepath.Join(serverDir, "settings_errors_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	rowRE := regexp.MustCompile(`\{http\.Method(\w+), "([^"]+)"`)
	rows := rowRE.FindAllStringSubmatch(string(table), -1)
	var missing []string
	for _, r := range routes {
		pat := regexp.MustCompile("^" + regexp.MustCompile(`\\\{[^}]+\\\}`).ReplaceAllString(regexp.QuoteMeta(r.pattern), `[^/]+`) + "$")
		covered := false
		for _, row := range rows {
			if strings.EqualFold(row[1], r.method) && pat.MatchString(row[2]) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, r.method+" "+r.pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("fenced routes missing from TestSettingsSaveRefusedByReloadFenceIs503's table in internal/server/settings_errors_test.go:\n  %s",
			strings.Join(missing, "\n  "))
	}
	t.Logf("%d fenced adapter methods, %d fenced handlers, %d routes, all in the 503 table", len(fencedAdapter), len(fencedHandlers), len(routes))
}
