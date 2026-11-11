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
	netHTTPImport = "net/http"
	clientName    = "Client"
	helperGet     = "Get"
)

// clientFindings is what the scan of one file reports. Built counts the
// http.Client values that set a redirect policy, so a scan that finds nothing
// can be told from one that checks nothing.
type clientFindings struct {
	Built      int
	Violations []string
}

// scanHTTPClients reports every way a file builds an http.Client without a
// redirect policy. A client left on Go's default policy follows redirects and
// keeps the Authorization and Cookie headers when only the port or the scheme
// changes, so every client the SDK builds sets CheckRedirect. The scan flags a
// composite literal with no CheckRedirect key or a nil one, new(http.Client), a
// zero-value http.Client variable, http.DefaultClient, and the package
// functions that use it.
func scanHTTPClients(fset *token.FileSet, file *ast.File) clientFindings {
	name := httpImportName(file)
	if name == "" {
		return clientFindings{}
	}

	var found clientFindings

	report := func(pos token.Pos, what string) {
		found.Violations = append(found.Violations, fmt.Sprintf("%s: %s", fset.Position(pos), what))
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CompositeLit:
			if !isHTTPSelector(typed.Type, name, clientName) {
				return true
			}

			if hasRedirectPolicy(typed) {
				found.Built++
			} else {
				report(typed.Pos(), "http.Client literal without a CheckRedirect policy")
			}
		case *ast.CallExpr:
			if ident, ok := typed.Fun.(*ast.Ident); ok && ident.Name == "new" && len(typed.Args) == 1 &&
				isHTTPSelector(typed.Args[0], name, clientName) {
				report(typed.Pos(), "new(http.Client) has the default redirect policy")
			}

			for _, helper := range []string{helperGet, "Head", "Post", "PostForm"} {
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

// httpImportName returns the name net/http is imported under, or an empty
// string when the file does not import it.
func httpImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != netHTTPImport {
			continue
		}

		if spec.Name != nil {
			return spec.Name.Name
		}

		return "http"
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

func hasRedirectPolicy(lit *ast.CompositeLit) bool {
	for _, element := range lit.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "CheckRedirect" {
			continue
		}

		value, isIdent := pair.Value.(*ast.Ident)

		return !isIdent || value.Name != "nil"
	}

	return false
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

	const head = "package p\nimport nethttp \"net/http\"\nvar _ = nethttp.StatusOK\n"

	bad := map[string]string{
		"literal without the field": "func f() { _ = &nethttp.Client{Timeout: 1} }",
		"empty literal":             "func f() { _ = nethttp.Client{} }",
		"nil policy":                "func f() { _ = &nethttp.Client{CheckRedirect: nil} }",
		"new":                       "func f() { _ = new(nethttp.Client) }",
		"zero value variable":       "var c nethttp.Client",
		"default client":            "func f() { _ = nethttp.DefaultClient }",
		"package function":          "func f() { _, _ = nethttp.Get(\"x\") }",
	}

	for name, body := range bad {
		if got := scanSource(t, head+body); len(got.Violations) == 0 {
			t.Errorf("%s: scan reported nothing, want a violation", name)
		}
	}

	const withPolicy = "func f(p func(*nethttp.Request, []*nethttp.Request) error) " +
		"{ _ = &nethttp.Client{CheckRedirect: p} }"

	good := scanSource(t, head+withPolicy)
	if len(good.Violations) != 0 || good.Built != 1 {
		t.Errorf("literal with a policy: %+v, want no violation and one client built", good)
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
