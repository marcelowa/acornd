package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

// A workspace that is added is a decision about the engine and what its commands may reach, and
// both are checked before the registry is written: an engine nobody ships and an egress nobody
// understands are refused with the values that would have worked.
func TestAddingAWorkspaceChecksItsChoices(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	folder := t.TempDir()

	refusals := []struct {
		why   string
		flags []string
		says  string
	}{
		{"an engine that does not exist", []string{"--sandbox", "qemu"}, "--sandbox is one of"},
		{"egress nobody understands", []string{"--egress", "maybe"}, "egress"},
	}
	for _, one := range refusals {
		args := append([]string{"workspace", "add", "demo", folder, "--data-dir", dir}, one.flags...)
		_, stderr, code := cli(t, dir, "", args...)
		if code == 0 {
			t.Errorf("%s: exit 0\n%s", one.why, stderr)
			continue
		}
		if !strings.Contains(stderr, one.says) {
			t.Errorf("%s: the refusal should mention %q: %q", one.why, one.says, stderr)
		}
	}
	// Nothing was recorded by either refusal.
	if list, _, _ := cli(t, dir, "", "workspace", "list", "--data-dir", dir); strings.Contains(list, "demo") {
		t.Errorf("a refused add should record nothing:\n%s", list)
	}

	// And the egress that is understood is accepted and reported back, so the answer says what the
	// workspace will be rather than only what was typed.
	out, stderr, code := cli(t, dir, "", "workspace", "add", "demo", folder, "--sandbox", "host", "--egress", "off", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("workspace add: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(out, "host") || !strings.Contains(out, "off") {
		t.Errorf("the answer should say the engine and the egress:\n%s", out)
	}
}

// The report a script reads has the same shape whether or not this machine can run a sandbox: JSON
// with the checks by name, so a script never has to read prose.
func TestTheDoctorReportIsTheSameShapeEverywhere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, stderr, code := cli(t, dir, "", "doctor", "--json", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("doctor --json: exit %d\n%s", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor --json is not JSON: %v\n%s", err, out)
	}
	// The shape a script reads: what is ready, which engines this machine has, and the checks by
	// name. The checks carry the sentence and the fix, so a report never has to be read as prose.
	for _, key := range []string{"readiness", "engines", "update", "default"} {
		if _, found := report[key]; !found {
			t.Errorf("the report should carry %q, got %v", key, reportKeys(report))
		}
	}
	checks, ok := report["readiness"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("readiness = %v", report["readiness"])
	}
	for _, entry := range checks {
		check, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("a check is not an object: %v", entry)
		}
		for _, key := range []string{"name", "ok", "detail", "fix"} {
			if _, found := check[key]; !found {
				t.Errorf("every check should carry %q: %v", key, check)
			}
		}
	}
}

// reportKeys is what a map has, for a failure message that names what was there.
func reportKeys(report map[string]any) []string {
	keys := make([]string, 0, len(report))
	for key := range report {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// A workspace's secrets are listed as what its commands actually receive: its own entries plus the
// install's, with the wider one shadowed where both name the same thing. The list is per workspace
// because that is the only place the answer is "what will this command get".
func TestASecretsListForOneWorkspaceIsWhatItReceives(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	folder := t.TempDir()
	if _, stderr, code := cli(t, dir, "", "workspace", "add", "demo", folder, "--sandbox", "host", "--data-dir", dir); code != 0 {
		t.Fatalf("workspace add: exit %d\n%s", code, stderr)
	}

	// The install's own secret, and if the working directory is a workspace, its own.
	if _, stderr, code := cli(t, dir, "v\n", "secret", "add", "SHARED", "--global", "--description", "from the install", "--hosts", "api.example.com", "--data-dir", dir); code != 0 {
		t.Fatalf("secret add: exit %d\n%s", code, stderr)
	}

	out, stderr, code := cli(t, dir, "", "secret", "list", "--workspace", "demo", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("secret list --workspace: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(out, "SHARED") {
		t.Errorf("a workspace receives the install's secrets:\n%s", out)
	}
	if strings.Contains(out, "v\n") || strings.Contains(out, "s3cret") {
		t.Errorf("the list must never carry a value:\n%s", out)
	}

	// A workspace that is not in the registry is refused by name: a list for a folder nobody has
	// would answer "nothing", which reads as "no secrets" rather than "no workspace".
	if _, stderr, code := cli(t, dir, "", "secret", "list", "--workspace", "nowhere", "--data-dir", dir); code == 0 {
		t.Errorf("an unknown workspace: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "nowhere") {
		t.Errorf("the refusal should name the workspace: %q", stderr)
	}
}

// The provider keys an install holds are listed by name and type, never by value, and a machine
// with none says so rather than printing an empty table.
func TestTheModelListNamesTheProviderKeysItHas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	out, _, code := cli(t, dir, "", "model", "list", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("model list: exit %d", code)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("with no keys the list says so:\n%s", out)
	}

	// A key is stored by `provider add`, which reads the value from stdin so it never reaches argv.
	if _, stderr, code := cli(t, dir, "sk-test-value\n", "provider", "add", "faux", "--data-dir", dir); code != 0 {
		t.Fatalf("provider add: exit %d\n%s", code, stderr)
	}
	out, _, code = cli(t, dir, "", "model", "list", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("model list: exit %d", code)
	}
	if !strings.Contains(out, "faux") {
		t.Errorf("the list should name the provider and its type:\n%s", out)
	}
	if strings.Contains(out, "sk-test-value") {
		t.Errorf("the list must never carry a key:\n%s", out)
	}
}
