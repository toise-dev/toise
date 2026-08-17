package wire

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStdlibOnly pins the promise this package's doc comment makes and that the
// stability policy repeats: importing the vocabulary must never pull a protocol
// stack into a producer's module graph.
//
// It is not decoration. The reference producer adopted the contract precisely
// because it could take wire without taking the transport — its agent already
// owns a single OTLP rail carrying the tenant header and relay enrichment, and a
// second OTLP client would have reintroduced the drift the shared contract
// exists to remove. One convenience import here would quietly close that door
// for every producer in that position, and nothing else in the build would
// complain.
//
// Verified empirically alongside this test: a module importing only wire ends
// up, after go mod tidy, requiring pkg/emit and nothing else — module-graph
// pruning keeps grpc and pdata out because no imported package needs them. That
// holds only while this package imports nothing outside the standard library.
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
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
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
