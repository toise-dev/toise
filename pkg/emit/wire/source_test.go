package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestEveryDeclaredTypeIsListed reads the package's own source and asserts that
// every Type* / RelType* constant declared in it appears in EntityTypes() or
// RelationTypes().
//
// The direction matters. A test that walks the list and checks each entry has a
// declaration confronts the list with itself: a constant somebody declared and
// forgot to list stays invisible to it, which is exactly how a vocabulary drifts
// from the code that uses it. This one goes the other way — source to list — so
// forgetting is a build failure rather than a silent hole.
//
// It parses the whole package rather than one named file on purpose: a scan
// narrower than the thing it audits reintroduces the blind spot it exists to
// close.
func TestEveryDeclaredTypeIsListed(t *testing.T) {
	// The directory is walked by hand rather than with parser.ParseDir, which is
	// deprecated, and rather than with x/tools: this package promises to stay
	// stdlib-only, and a test dependency still lands in the module graph a
	// producer resolves.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var decls []ast.Decl
	parsed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		decls = append(decls, file.Decls...)
		parsed++
	}
	if parsed == 0 {
		t.Fatal("no package source parsed — the scan would pass vacuously")
	}

	listed := map[string]bool{}
	for _, v := range append(EntityTypes(), RelationTypes()...) {
		listed[v] = true
	}

	declared := 0
	for _, decl := range decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				// RelType itself is an attribute key, not a relation type.
				isRelation := strings.HasPrefix(name.Name, "RelType") && name.Name != "RelType"
				isEntity := strings.HasPrefix(name.Name, "Type")
				if !isRelation && !isEntity {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: cannot read its value: %v", name.Name, err)
					continue
				}
				declared++
				if !listed[value] {
					t.Errorf("%s = %q is declared but missing from EntityTypes()/RelationTypes() — a producer cannot discover it", name.Name, value)
				}
			}
		}
	}

	if declared != len(EntityTypes())+len(RelationTypes()) {
		t.Errorf("found %d declared type constants but the lists hold %d — one side gained an entry the other did not",
			declared, len(EntityTypes())+len(RelationTypes()))
	}
}

// TestStdlibOnly pins the promise this package's doc comment makes, that the
// stability policy repeats, and that the scan above already relies on: importing
// the vocabulary must never pull a protocol stack into a producer's module graph.
//
// It is not decoration. The reference producer adopted the contract precisely
// because it could take wire without taking the transport — its agent already
// owns a single OTLP rail carrying the tenant header and relay enrichment, and a
// second OTLP client would have reintroduced the drift a shared contract exists
// to remove. One convenience import here would quietly close that door for every
// producer in that position, and nothing else in the build would complain.
//
// Verified empirically alongside this test: a module importing only wire ends up,
// after go mod tidy, requiring pkg/emit and nothing else — module-graph pruning
// keeps grpc and pdata out because no imported package needs them. That holds
// only while this package imports nothing outside the standard library.
func TestStdlibOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		checked++
		for _, imp := range f.Imports {
			path, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				t.Fatalf("%s: unquoting import %s: %v", name, imp.Path.Value, uerr)
			}
			// A standard-library path has no dot in its first segment; every
			// module path starts with a domain.
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				t.Errorf("%s imports %q. This package is stdlib-only by contract: a producer "+
					"that already owns its OTLP transport must be able to take the vocabulary "+
					"without the protocol stack. Put the code needing this dependency in the "+
					"emit package instead.", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test source file found; the scan would pass vacuously")
	}
}
