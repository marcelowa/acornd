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

// TestImportsStayInsideTheBoundary fails on any internal import the package's row does not
// allow, on a package with no row, and on a row that has outlived its import. Every violation
// is reported at once: a boundary is easier to fix as a list than one round trip per file.
func TestImportsStayInsideTheBoundary(t *testing.T) {
	root, module := moduleRoot(t)
	imports := internalImports(t, root, module)

	for _, dir := range sorted(imports) {
		allowed, ok := Allowed[dir]
		if !ok {
			t.Errorf("%s holds Go files but has no row in arch.Allowed: say where it belongs (ADR 0002)", dir)
			continue
		}
		for _, imported := range imports[dir] {
			if !contains(allowed, imported) {
				t.Errorf("%s imports %s, which its arch.Allowed row does not allow; a new edge is a decision, so add it to the row and say why", dir, imported)
			}
		}
		for _, stale := range allowed {
			if !contains(imports[dir], stale) {
				t.Errorf("arch.Allowed[%q] lists %s, which the package no longer imports: drop the row entry", dir, stale)
			}
		}
	}
	for _, dir := range sorted(Allowed) {
		if _, ok := imports[dir]; !ok {
			t.Errorf("arch.Allowed has a row for %q, which holds no Go files: a rename leaves one behind", dir)
		}
	}
}

// internalImports maps each directory to the packages inside this module it imports, test files
// included: a boundary that only holds for the non-test half is not a boundary.
func internalImports(t *testing.T, root, module string) map[string][]string {
	t.Helper()
	seen := map[string]map[string]bool{}
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
		// Every directory holding Go files is registered, even one with no internal imports:
		// the row is where the package says which layer it belongs to, and an empty row says
		// something too.
		if seen[dir] == nil {
			seen[dir] = map[string]bool{}
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
			seen[dir][relative] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	out := make(map[string][]string, len(seen))
	for dir, set := range seen {
		out[dir] = sorted(set)
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
