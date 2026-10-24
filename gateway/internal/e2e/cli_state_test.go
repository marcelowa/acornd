package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file this install wrote and cannot read back is an error the person can act on: the command
// names the file and says what is wrong with it, rather than printing "none" as if the setting had
// never been chosen. Silence here is how a corrupt file becomes a mystery.
func TestACommandOnACorruptFileSaysWhichFileAndWhy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A settings file that is not JSON, which is what a half-written editor buffer or a bad
	// hand-edit looks like.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := cli(t, dir, "", "model", "list", "--data-dir", dir)
	if code == 0 {
		t.Fatalf("a corrupt settings file should fail, exit 0\n%s", stderr)
	}
	if !strings.Contains(stderr, "settings.json") {
		t.Errorf("the error should name the file: %q", stderr)
	}

	// The same for the vault: its answer is an error, not an empty list.
	if err := os.WriteFile(filepath.Join(dir, "secrets.json"), []byte("not json either"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = cli(t, dir, "", "secret", "list", "--data-dir", dir)
	if code == 0 {
		t.Fatalf("a corrupt secrets file should fail, exit 0\n%s", stderr)
	}
	if !strings.Contains(stderr, "acornfall secret") {
		t.Errorf("the error should be reported as the secret command's: %q", stderr)
	}
}

// Showing one secret answers by name, refuses a name that is not there, and needs a name at all.
// The value is not printed in any of the three.
func TestShowingOneSecretAnswersByName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if _, stderr, code := cli(t, dir, "", "secret", "show", "--data-dir", dir); code != 2 {
		t.Errorf("show with no name: exit %d, want 2\n%s", code, stderr)
	} else if !strings.Contains(stderr, "usage: acornfall secret show") {
		t.Errorf("the refusal should print the usage: %q", stderr)
	}

	add := []string{"secret", "add", "SHOWN", "--global", "--description", "for the test", "--data-dir", dir}
	if _, stderr, code := cli(t, dir, "s3cret\n", add...); code != 0 {
		t.Fatalf("secret add: exit %d\n%s", code, stderr)
	}

	shown, stderr, code := cli(t, dir, "", "secret", "show", "SHOWN", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("secret show: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(shown, "SHOWN") || !strings.Contains(shown, "for the test") {
		t.Errorf("show should describe the secret:\n%s", shown)
	}
	if strings.Contains(shown, "s3cret") {
		t.Errorf("show must not print the value:\n%s", shown)
	}

	if _, _, code := cli(t, dir, "", "secret", "show", "NOT_THERE", "--data-dir", dir); code == 0 {
		t.Error("showing a secret that is not there should fail")
	}
}

// The internal verbs are how the supervisor and the namespace tier are reached, and they are not
// for people: an unknown one says so, and the ones that need an argument ask for it.
func TestTheInternalVerbsSayWhatTheyTake(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if _, stderr, code := cli(t, dir, "", "internal", "--data-dir", dir); code == 0 {
		t.Errorf("internal with no verb: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "internal") {
		t.Errorf("the refusal should name the group: %q", stderr)
	}
	if _, _, code := cli(t, dir, "", "internal", "nonsense", "--data-dir", dir); code == 0 {
		t.Error("an unknown internal verb should fail")
	}
}
