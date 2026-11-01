package identity

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A code is read off a terminal and typed into a browser, often on another device, so its shape is a
// promise to the person typing: four and four, and not one character they have to squint at.
func TestASetupCodeIsTwoHalvesOfUnambiguousCharacters(t *testing.T) {
	// The alphabet is Crockford's base32 without I, L, O, U, 0 and 1 — the pairs a person confuses when
	// they read a code and retype it.
	shape := regexp.MustCompile(`^[23456789ABCDEFGHJKMNPQRSTVWXYZ]{4}-[23456789ABCDEFGHJKMNPQRSTVWXYZ]{4}$`)
	for range 50 {
		code, err := RotateSetupCode(t.TempDir())
		if err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if !shape.MatchString(code) {
			t.Fatalf("%q is not the shape a person is asked to read", code)
		}
	}
}

// EnsureSetupCode is called on every boot of an install nobody owns yet, so it must be the same code
// each time and must never mint a second one over a working credential.
func TestEnsureSetupCodeMintsOnceAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	first, minted, err := EnsureSetupCode(dir)
	if err != nil || !minted {
		t.Fatalf("first: code=%q minted=%v err=%v", first, minted, err)
	}
	second, minted, err := EnsureSetupCode(dir)
	if err != nil || minted {
		t.Fatalf("second: code=%q minted=%v err=%v", second, minted, err)
	}
	if first != second {
		t.Error("a second boot replaced the code the first one printed")
	}
}

// A missing code is the ordinary state of an install with an owner, and it is reported as that rather
// than as a broken file: the caller prints the command that mints one.
func TestReadSetupCodeWithoutOneIsItsOwnReason(t *testing.T) {
	if _, err := ReadSetupCode(t.TempDir()); !errors.Is(err, ErrNoSetupCode) {
		t.Fatalf("err = %v, want ErrNoSetupCode", err)
	}
}

// Rotation is the answer to a code that was read over somebody's shoulder: the old one stops working at
// once, and the printed one is what the install holds afterwards.
func TestRotateSetupCodeReplacesTheOldOne(t *testing.T) {
	dir := t.TempDir()
	first, err := RotateSetupCode(dir)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	second, err := RotateSetupCode(dir)
	if err != nil {
		t.Fatalf("rotate again: %v", err)
	}
	if first == second {
		t.Fatal("rotation produced the same code")
	}
	fromFile, err := ReadSetupCode(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if fromFile != second {
		t.Errorf("the install holds %q, want %q", fromFile, second)
	}
	if matched, err := MatchSetupCode(dir, first); err != nil || matched {
		t.Errorf("the old code still matches: %v, %v", matched, err)
	}
}

// Disabling removes the file, and doing it twice is not an error: a command a script runs must not fail
// because somebody already did what it asks.
func TestDisableSetupCodeRemovesItAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := RotateSetupCode(dir); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := DisableSetupCode(dir); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := ReadSetupCode(dir); !errors.Is(err, ErrNoSetupCode) {
		t.Fatalf("the code survived disabling: %v", err)
	}
	if err := DisableSetupCode(dir); err != nil {
		t.Fatalf("disable twice: %v", err)
	}
}

// The form's compare, which is where a person's typing meets the credential: case and the whitespace a
// paste leaves behind are forgiven, everything else is not.
func TestMatchSetupCode(t *testing.T) {
	dir := t.TempDir()
	code, err := RotateSetupCode(dir)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	cases := []struct {
		name  string
		given string
		want  bool
	}{
		{"as printed", code, true},
		{"typed in lower case", "  " + strings.ToLower(code) + "\n", true},
		{"one character short", code[:8], false},
		{"one character changed", changeFirst(code), false},
		{"empty", "", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			matched, err := MatchSetupCode(dir, test.given)
			if err != nil {
				t.Fatalf("match: %v", err)
			}
			if matched != test.want {
				t.Errorf("MatchSetupCode(%q) = %v, want %v", test.given, matched, test.want)
			}
		})
	}

	// A code that was disabled or rotated away is no match, and not an error: the form says the same
	// sentence for both, and a person reading it does the same thing.
	if err := DisableSetupCode(dir); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if matched, err := MatchSetupCode(dir, code); err != nil || matched {
		t.Errorf("a disabled code still matches: %v, %v", matched, err)
	}
}

// The code is a credential on a shared filesystem; 0600 is the difference between a secret and a public
// file. It sits in a 0700 folder for the same reason.
func TestTheSetupCodeIsNotReadableByAnyoneElse(t *testing.T) {
	// A folder the write creates itself, so the mode is this code's and not the test's temp directory.
	dir := filepath.Join(t.TempDir(), "install")
	if _, err := RotateSetupCode(dir); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, SetupCodeFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 600", mode)
	}
	folder, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat folder: %v", err)
	}
	if mode := folder.Mode().Perm(); mode != 0o700 {
		t.Errorf("folder mode = %o, want 700", mode)
	}
}

// A file that exists and cannot be parsed is a problem to report, not a licence to mint over it: the
// console may be about to accept exactly that code.
func TestEnsureSetupCodeDoesNotOverwriteAFileItCannotUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SetupCodeFile)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := EnsureSetupCode(dir); err == nil {
		t.Fatal("an empty code file was silently replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "\n" {
		t.Error("the file was rewritten")
	}
}

// A path that exists and is not a readable file is reported as itself. Reading it as "no code yet" would
// mint over whatever is there and send the person looking in the wrong place.
func TestASetupCodePathThatIsNotAFileIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, SetupCodeFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := ReadSetupCode(dir); err == nil || errors.Is(err, ErrNoSetupCode) {
		t.Fatalf("err = %v, want a problem rather than a first run", err)
	}
	if _, _, err := EnsureSetupCode(dir); err == nil {
		t.Error("EnsureSetupCode minted over a directory")
	}
}

// An install folder that cannot be created is a refusal with a cause, not a panic: the folder is named
// by the caller, and a name under a file is a typo the tool can point at.
func TestAnInstallFolderThatCannotBeCreatedIsReportedForACode(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "acornd")
	if err := os.WriteFile(file, []byte("not a folder"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := RotateSetupCode(file); err == nil {
		t.Fatal("a code was minted under a file")
	}
}

// changeFirst is the input a person produces when they mistype one character of a code.
func changeFirst(code string) string {
	replacement := "2"
	if code[0] == '2' {
		replacement = "3"
	}
	return replacement + code[1:]
}

// A code file that exists and cannot be parsed is reported by the compare too: the form says the file is
// broken rather than that the code is wrong, because the two have different fixes.
func TestMatchSetupCodeReportsABrokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SetupCodeFile), nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := MatchSetupCode(dir, "ABCD-EFGH"); err == nil {
		t.Fatal("an empty code file matched or was read as no code")
	}
}

// Removing a code that something else is in the way of is an error, not a silent success: the command says
// what it could not remove.
func TestDisableSetupCodeReportsWhatItCannotRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SetupCodeFile)
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := DisableSetupCode(dir); err == nil {
		t.Fatal("a directory that shares the code's name was reported as removed")
	}
}
