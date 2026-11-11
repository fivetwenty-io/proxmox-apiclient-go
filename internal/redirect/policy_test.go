package redirect_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	netHTTPImport  = "net/http"
	redirectImport = "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	clientName     = "Client"
	helperGet      = "Get"
)

// clientFindings is what the scan of one file reports. Built counts the
// http.Client values that set a redirect policy, so a scan that finds nothing
// can be told from one that checks nothing.
type clientFindings struct {
	Built      int
	Violations []string
}

// scanHTTPClients reports every way a file builds an http.Client without a
// redirect refusal. A client left on Go's default policy follows redirects and
// keeps the Authorization and Cookie headers when only the port or the scheme
// changes, so every client the SDK builds sets CheckRedirect. The scan flags a
// composite literal with no CheckRedirect key or a nil one, new(http.Client), a
// zero-value http.Client variable, http.DefaultClient, and the package
// functions that use it. A CheckRedirect value counts as a policy only when it
// is a call to redirect.Wrap or the function redirect.Refuse, because any other
// expression can hand the caller's nil function straight to the client. A
// composite literal must also set Transport to a call to redirect.Guard, which
// refuses a redirect response before net/http reads its Location header,
// because a policy alone never runs when that header does not parse.
func scanHTTPClients(fset *token.FileSet, file *ast.File) clientFindings {
	name := httpImportName(file)
	if name == "" {
		return clientFindings{}
	}

	var found clientFindings

	redirectName := importName(file, redirectImport, "redirect")

	report := func(pos token.Pos, what string) {
		found.Violations = append(found.Violations, fmt.Sprintf("%s: %s", fset.Position(pos), what))
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CompositeLit:
			if isHTTPSelector(typed.Type, name, clientName) {
				if problem := literalProblem(typed, redirectName); problem != "" {
					report(typed.Pos(), problem)
				} else {
					found.Built++
				}
			}
		case *ast.CallExpr:
			if ident, ok := typed.Fun.(*ast.Ident); ok && ident.Name == "new" && len(typed.Args) == 1 &&
				isHTTPSelector(typed.Args[0], name, clientName) {
				report(typed.Pos(), "new(http.Client) has the default redirect policy")
			}

			for _, helper := range []string{helperGet, "Head", opPost, "PostForm"} {
				if isHTTPSelector(typed.Fun, name, helper) {
					report(typed.Pos(), "http."+helper+" uses http.DefaultClient")
				}
			}
		case *ast.SelectorExpr:
			if isHTTPSelector(typed, name, "DefaultClient") {
				report(typed.Pos(), "http.DefaultClient has the default redirect policy")
			}
		case *ast.ValueSpec:
			if isHTTPSelector(typed.Type, name, clientName) {
				report(typed.Pos(), "zero-value http.Client has the default redirect policy")
			}
		}

		return true
	})

	return found
}

// literalProblem describes what an http.Client literal lacks, or returns an
// empty string when it sets a shared redirect policy and a guarded transport.
func literalProblem(lit *ast.CompositeLit, redirectName string) string {
	switch {
	case !hasRedirectPolicy(lit, redirectName):
		return "http.Client literal without a CheckRedirect policy"
	case !hasGuardedTransport(lit, redirectName):
		return "http.Client literal without a redirect.Guard transport"
	default:
		return ""
	}
}

// httpImportName returns the name net/http is imported under, or an empty
// string when the file does not import it.
func httpImportName(file *ast.File) string {
	return importName(file, netHTTPImport, "http")
}

// importName returns the name the file imports path under, which is fallback
// when the import has no explicit name, or an empty string when the file does
// not import it.
func importName(file *ast.File, path, fallback string) string {
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil || imported != path {
			continue
		}

		if spec.Name != nil {
			return spec.Name.Name
		}

		return fallback
	}

	return ""
}

func isHTTPSelector(expr ast.Expr, pkg, member string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != member {
		return false
	}

	ident, ok := selector.X.(*ast.Ident)

	return ok && ident.Name == pkg
}

func hasRedirectPolicy(lit *ast.CompositeLit, redirectName string) bool {
	for _, element := range lit.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "CheckRedirect" {
			continue
		}

		return isSharedPolicy(pair.Value, redirectName)
	}

	return false
}

// hasGuardedTransport reports whether the literal sets Transport to a call to
// redirect.Guard.
func hasGuardedTransport(lit *ast.CompositeLit, redirectName string) bool {
	if redirectName == "" {
		return false
	}

	for _, element := range lit.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "Transport" {
			continue
		}

		call, ok := pair.Value.(*ast.CallExpr)

		return ok && isHTTPSelector(call.Fun, redirectName, "Guard")
	}

	return false
}

// isSharedPolicy reports whether expr is redirect.Refuse or a call to
// redirect.Wrap, the two ways the SDK installs a policy that refuses by
// default.
func isSharedPolicy(expr ast.Expr, redirectName string) bool {
	if redirectName == "" {
		return false
	}

	if call, ok := expr.(*ast.CallExpr); ok {
		return isHTTPSelector(call.Fun, redirectName, "Wrap")
	}

	return isHTTPSelector(expr, redirectName, "Refuse")
}

func scanSource(t *testing.T, source string) clientFindings {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "sample.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}

	return scanHTTPClients(fset, file)
}

// TestScanFlagsClientsWithoutAPolicy proves the scan itself: each way to build
// a client with the default policy is reported, and a literal with a policy
// is not.
func TestScanFlagsClientsWithoutAPolicy(t *testing.T) {
	t.Parallel()

	const head = "package p\nimport nethttp \"net/http\"\nimport shared \"" + redirectImport + "\"\n" +
		"var _ = nethttp.StatusOK\nvar _ = shared.Refuse\n"

	bad := map[string]string{
		"literal without the field": "func f() { _ = &nethttp.Client{Timeout: 1} }",
		"empty literal":             "func f() { _ = nethttp.Client{} }",
		"nil policy":                "func f() { _ = &nethttp.Client{CheckRedirect: nil} }",
		"callers function passed through": "func f(p func(*nethttp.Request, []*nethttp.Request) error) " +
			"{ _ = &nethttp.Client{CheckRedirect: p} }",
		"field passed through": "func f(o struct{ CheckRedirect func(*nethttp.Request, []*nethttp.Request) error }) " +
			"{ _ = &nethttp.Client{CheckRedirect: o.CheckRedirect} }",
		"other package named Wrap": "func f() { _ = &nethttp.Client{CheckRedirect: nethttp.Wrap(nil)} }",
		"policy without a guard":   "func f() { _ = &nethttp.Client{CheckRedirect: shared.Refuse} }",
		"transport passed through": "func f(rt nethttp.RoundTripper) " +
			"{ _ = &nethttp.Client{Transport: rt, CheckRedirect: shared.Refuse} }",
		"guard from another package": "func f(rt nethttp.RoundTripper) " +
			"{ _ = &nethttp.Client{Transport: nethttp.Guard(rt, nil), CheckRedirect: shared.Refuse} }",
		"new":                 "func f() { _ = new(nethttp.Client) }",
		"zero value variable": "var c nethttp.Client",
		"default client":      "func f() { _ = nethttp.DefaultClient }",
		"package function":    "func f() { _, _ = nethttp.Get(\"x\") }",
	}

	for name, body := range bad {
		if got := scanSource(t, head+body); len(got.Violations) == 0 {
			t.Errorf("%s: scan reported nothing, want a violation", name)
		}
	}

	good := map[string]string{
		"wrapped function": "func f(rt nethttp.RoundTripper, p func(*nethttp.Request, []*nethttp.Request) error) " +
			"{ _ = &nethttp.Client{Transport: shared.Guard(rt, p), CheckRedirect: shared.Wrap(p)} }",
		"refuse": "func f(rt nethttp.RoundTripper) " +
			"{ _ = &nethttp.Client{Transport: shared.Guard(rt, nil), CheckRedirect: shared.Refuse} }",
	}

	for name, body := range good {
		got := scanSource(t, head+body)
		if len(got.Violations) != 0 || got.Built != 1 {
			t.Errorf("%s: %+v, want no violation and one client built", name, got)
		}
	}

	other := scanSource(t, "package p\ntype http struct{ Client int }\nvar _ = http{Client: 1}\n")
	if len(other.Violations) != 0 || other.Built != 0 {
		t.Errorf("a type named http: %+v, want it ignored", other)
	}
}

// TestEveryHTTPClientTheSDKBuildsRefusesRedirects scans the non-test source of
// the whole module. It fails when any client is built with Go's default
// redirect policy, which would send a token, a ticket cookie, or a form body to
// another port of the host after a redirect.
func TestEveryHTTPClientTheSDKBuildsRefusesRedirects(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("module root: %v", err)
	}

	var (
		violations []string
		built      int
		scanned    int
	)

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if base := entry.Name(); base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}

		scanned++
		found := scanHTTPClients(fset, file)
		built += found.Built

		for _, violation := range found.Violations {
			violations = append(violations, strings.TrimPrefix(violation, root+string(filepath.Separator)))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if scanned < 50 || built == 0 {
		t.Fatalf("scanned %d files and found %d clients with a policy, want a real scan", scanned, built)
	}

	slices.Sort(violations)

	for _, violation := range violations {
		t.Errorf("HTTP client without a redirect refusal policy at %s", violation)
	}
}

// TestScanFlagsLiteralsInAFileWithoutTheRedirectImport checks a file that does
// not import the redirect package cannot hold the shared policy or the guard,
// whatever it names them.
func TestScanFlagsLiteralsInAFileWithoutTheRedirectImport(t *testing.T) {
	t.Parallel()

	const unimported = "package p\nimport nethttp \"net/http\"\nvar _ = nethttp.StatusOK\n"

	for name, body := range map[string]string{
		"policy passed through": "func f(p func(*nethttp.Request, []*nethttp.Request) error) " +
			"{ _ = &nethttp.Client{CheckRedirect: p} }",
		"package named without the import": "func f(rt nethttp.RoundTripper) " +
			"{ _ = &nethttp.Client{Transport: shared.Guard(rt, nil), CheckRedirect: shared.Refuse} }",
	} {
		if got := scanSource(t, unimported+body); len(got.Violations) == 0 {
			t.Errorf("%s without the redirect import: scan reported nothing, want a violation", name)
		}
	}
}

// TestScanChecksTheRedirectImportOnItsOwn checks each part of the scan fails a
// literal in a file that does not import the redirect package, so neither the
// policy check nor the transport check depends on the other to catch it.
func TestScanChecksTheRedirectImportOnItsOwn(t *testing.T) {
	t.Parallel()

	const source = "package p\nimport nethttp \"net/http\"\n" +
		"func f(rt nethttp.RoundTripper, p func(*nethttp.Request, []*nethttp.Request) error) " +
		"{ _ = &nethttp.Client{Transport: rt, CheckRedirect: p} }"

	file, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}

	var literal *ast.CompositeLit

	ast.Inspect(file, func(node ast.Node) bool {
		if lit, ok := node.(*ast.CompositeLit); ok {
			literal = lit
		}

		return true
	})

	if literal == nil {
		t.Fatal("sample has no composite literal")
	}

	if hasRedirectPolicy(literal, importName(file, redirectImport, "redirect")) {
		t.Error("a policy passed through counts as the shared policy in a file without the import")
	}

	if hasGuardedTransport(literal, importName(file, redirectImport, "redirect")) {
		t.Error("a transport passed through counts as the guard in a file without the import")
	}

	if hasGuardedTransport(literal, "") {
		t.Error("an empty import name counts as the guard")
	}
}
