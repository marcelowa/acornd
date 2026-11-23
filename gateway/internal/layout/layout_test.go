package layout

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The install folder's files, by the name each has on disk. A name here may be spelled only in this package.
var names = map[string]string{
	"workspaces.json": Registry(""),
	"settings.json":   Settings(""),
	"secrets.json":    Secrets(""),
	"people.json":     People(""),
	"remote.json":     Remote(""),
	"update.json":     Update(""),
	"config.json":     Config(""),
	"drafts.json":     Drafts(""),
	"session.sqlite":  Session(""),
}

// A name spelled for another reason: a file that is not the install folder's, or help text that names the file
// to a reader. Each one says why.
var elsewhere = map[string]string{
	"internal/sandbox/youki/youki.go:config.json": "the OCI bundle's spec, in a sandbox's own folder",
	"internal/support/support.go:config.json":     "the same OCI spec, copied from a sandbox's folder into a bundle",
	"cmd/acornd/people.go:people.json":            "the command's help text",
	"cmd/acornd/remote.go:remote.json":            "the command's help text",
}

func TestEachPathIsItsFileInItsFolder(t *testing.T) {
	for name, path := range names {
		if path != name {
			t.Errorf("%s is the path for %s", path, name)
		}
	}
	if got := Settings("/i"); got != "/i/settings.json" {
		t.Errorf("Settings = %s", got)
	}
	if got := State("/i", "team"); got != "/i/state/team" {
		t.Errorf("State = %s", got)
	}
	if got := Drafts(State("/i", "team")); got != "/i/state/team/drafts.json" {
		t.Errorf("Drafts = %s", got)
	}
}

// A file name spelled in a second package is a second layout, and the two drift: the reason this package
// exists (#72). Tests may spell them, because a test that checks what is on disk reads the layout from outside.
func TestNoOtherPackageSpellsAFileName(t *testing.T) {
	root := filepath.Join("..", "..")
	files := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		if filepath.Dir(relative) == filepath.Join("internal", "layout") {
			return nil
		}
		parsed, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			for name := range names {
				if !strings.Contains(value, name) {
					continue
				}
				if _, known := elsewhere[filepath.ToSlash(relative)+":"+name]; known {
					continue
				}
				t.Errorf("%s spells %q: ask layout for the path", files.Position(literal.Pos()), name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
