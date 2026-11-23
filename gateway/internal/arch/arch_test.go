package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestImportsStayInsideTheBoundary fails on any internal import the package's rows do not
// allow, on a package with no row, and on a row that has outlived its import. Production files
// and test files are read apart: a production file may import only what Allowed lists, and a
// test file may import what Allowed lists or AllowedInTests adds. Every violation is reported at
// once: a boundary is easier to fix as a list than one round trip per file.
func TestImportsStayInsideTheBoundary(t *testing.T) {
	root, module := moduleRoot(t)
	imports := internalImports(t, root, module)

	for _, dir := range sorted(imports) {
		allowed, ok := Allowed[dir]
		if !ok {
			t.Errorf("%s holds Go files but has no row in arch.Allowed: say where it belongs (ADR 0002)", dir)
			continue
		}
		testOnly := AllowedInTests[dir]
		for _, imported := range imports[dir].production {
			if !contains(allowed, imported) {
				t.Errorf("%s imports %s in a production file, which its arch.Allowed row does not allow; a new edge is a decision, so add it to the row and say why", dir, imported)
			}
		}
		for _, imported := range imports[dir].tests {
			if !contains(allowed, imported) && !contains(testOnly, imported) {
				t.Errorf("%s imports %s in a test file, which neither arch.Allowed nor arch.AllowedInTests allows; a test-only edge is a decision too, so add it to AllowedInTests and say why", dir, imported)
			}
		}
		for _, stale := range allowed {
			if !contains(imports[dir].production, stale) {
				t.Errorf("arch.Allowed[%q] lists %s, which no production file imports: drop the row entry, or move it to arch.AllowedInTests if only a test uses it", dir, stale)
			}
		}
		for _, stale := range testOnly {
			if !contains(imports[dir].tests, stale) {
				t.Errorf("arch.AllowedInTests[%q] lists %s, which no test file imports: drop the row entry", dir, stale)
			}
		}
	}
	for _, dir := range sorted(Allowed) {
		if _, ok := imports[dir]; !ok {
			t.Errorf("arch.Allowed has a row for %q, which holds no Go files: a rename leaves one behind", dir)
		}
	}
	for _, dir := range sorted(AllowedInTests) {
		if _, ok := imports[dir]; !ok {
			t.Errorf("arch.AllowedInTests has a row for %q, which holds no Go files: a rename leaves one behind", dir)
		}
	}
}

// packageImports is what one directory's Go files import from inside this module, split by
// whether the file is a test. A boundary that holds only for the production half is not a
// boundary, so the test half is checked too; the split is what lets a row say which is which.
type packageImports struct {
	production []string
	tests      []string
}

// internalImports maps each directory to the packages inside this module it imports. Every
// directory holding Go files is registered, even one with no internal imports: the row is
// where the package says which layer it belongs to, and an empty row says something too.
func internalImports(t *testing.T, root, module string) map[string]packageImports {
	t.Helper()
	type split struct{ production, tests map[string]bool }
	found := map[string]split{}
	files := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "testdata", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		dir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		dir = filepath.ToSlash(dir)
		if _, ok := found[dir]; !ok {
			found[dir] = split{production: map[string]bool{}, tests: map[string]bool{}}
		}
		into := found[dir].production
		if strings.HasSuffix(path, "_test.go") {
			into = found[dir].tests
		}
		file, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			relative, inside := strings.CutPrefix(imported, module+"/")
			if !inside || relative == dir {
				continue
			}
			into[relative] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	out := make(map[string]packageImports, len(found))
	for dir, set := range found {
		out[dir] = packageImports{production: sorted(set.production), tests: sorted(set.tests)}
	}
	return out
}

// moduleRoot is the gateway directory and the module path in its go.mod, read rather than
// repeated here: a hardcoded module path would be a second copy that drifts silently.
func moduleRoot(t *testing.T) (root, module string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root = filepath.Dir(filepath.Dir(wd))
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			module = strings.TrimSpace(rest)
			break
		}
	}
	if module == "" {
		t.Fatal("go.mod has no module line")
	}
	return root, module
}

func sorted[V any](set map[string]V) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, value string) bool {
	return slices.Contains(list, value)
}
