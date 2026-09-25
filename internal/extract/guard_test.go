// guard_test.go — the ADR-14 fitness function: the ONLY go-enriche package
// this module may import is go-enriche/structured (pure schema.org parsing).
// The root package and go-enriche/fetch (and any other sibling that opens
// connections) would bypass the wowa-only egress rule — every byte of
// third-party traffic must leave via go-kit/wowa. go-grad's
// import_guard_test.go is the precedent; this one scans the whole module
// instead of one package dir, walking up from internal/extract to the repo
// root.
package extract_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// enrichePrefix is the module whose non-allow-listed packages are banned.
const enrichePrefix = "github.com/anatolykoptev/go-enriche"

// allowedEnricheImports is the closed allow-list. Add an entry ONLY with a
// reviewed change that needs the package and only when that package does
// no network egress of its own.
var allowedEnricheImports = map[string]bool{
	enrichePrefix + "/structured": true,
}

// bannedEnricheImports walks every .go file under root (vendor excluded)
// and returns "path: import" violations.
func bannedEnricheImports(root string) ([]string, error) {
	var viol []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fileViol, perr := fileBannedImports(fset, path)
		if perr != nil {
			return perr
		}
		viol = append(viol, fileViol...)
		return nil
	})
	return viol, err
}

// fileBannedImports returns the banned go-enriche imports declared by one
// Go source file.
func fileBannedImports(fset *token.FileSet, path string) ([]string, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		if p != enrichePrefix && !strings.HasPrefix(p, enrichePrefix+"/") {
			continue
		}
		if allowedEnricheImports[p] {
			continue
		}
		out = append(out, path+": "+p)
	}
	return out, nil
}

// TestImportGuard_EnricheStructuredOnly scans the whole module for
// go-enriche imports outside the allow-list. RED-on-plant (falsification):
// add `import _ "github.com/anatolykoptev/go-enriche"` (or /fetch) anywhere
// and this fails; TestBannedEnricheImportsDetectsViolation proves the
// walker itself sees planted violations.
func TestImportGuard_EnricheStructuredOnly(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not found at %s: %v", root, err)
	}
	viol, err := bannedEnricheImports(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(viol) > 0 {
		t.Errorf("banned go-enriche imports (only %s is allowed — ADR-14 wowa-only egress): %s",
			enrichePrefix+"/structured", strings.Join(viol, ", "))
	}
}

// TestImportGuard_StructuredAnchor keeps the guard anchored to reality:
// internal/extract really does import go-enriche/structured, so the
// allow-list is not vacuous and a refactor dropping the import fails here.
func TestImportGuard_StructuredAnchor(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, perr := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == enrichePrefix+"/structured" {
				found = true
			}
		}
	}
	if !found {
		t.Error("no file in internal/extract imports go-enriche/structured — guard is unanchored")
	}
}

// TestBannedEnricheImportsDetectsViolation is the guard's self-test: a
// planted violation in a temp tree must be reported.
func TestBannedEnricheImportsDetectsViolation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.go")
	if err := os.WriteFile(src, []byte(
		"package evil\n\nimport _ \"github.com/anatolykoptev/go-enriche/fetch\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	// A vendored-path file must NOT be scanned.
	vend := filepath.Join(dir, "vendor", "x.go")
	if err := os.MkdirAll(filepath.Dir(vend), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vend, []byte(
		"package x\n\nimport _ \"github.com/anatolykoptev/go-enriche\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	viol, err := bannedEnricheImports(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(viol) != 1 || !strings.Contains(viol[0], "evil.go") {
		t.Fatalf("expected exactly the planted violation, got %v", viol)
	}
}
