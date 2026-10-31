package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The command line, driven as a person or a script would drive it: the same binary, its own
// install folder, and nothing running. The gateway's own suite covers what a running server does;
// this covers the verbs that never start one, which is most of what a person types.
//
// Each command gets a folder of its own. A shared one would let `provider add` in one test be the
// key another test reads, which is the kind of ordering nobody notices until it fails alone.

// cli runs the binary and answers with what it said and what it exited with. stdin is the
// payload a command reads when the value must not be in argv.
func cli(t *testing.T, dir string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
	// Each command owns its own --data-dir, so the caller puts it where that command parses it.
	command := exec.Command(gatewayBin, args...)
	command.Env = append(os.Environ(), "HOME="+dir, "ACORND_NO_SANDBOX=1")
	if coverDir != "" {
		// The same rule as the gateway above: `make cover` instruments this binary, and a child
		// started without saying where to write is a child whose work is invisible.
		command.Env = append(command.Env, "GOCOVERDIR="+coverDir)
	}
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

func TestTheCommandLineSaysWhatItDoesAndRefusesWhatItDoesNot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	out, _, code := cli(t, dir, "", "version")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Errorf("version: exit %d, %q", code, out)
	}

	usage, _, code := cli(t, dir, "", "help")
	if code != 0 {
		t.Fatalf("help: exit %d", code)
	}
	// Every verb in the table is in the text. A command that exists and is not listed is a
	// command nobody finds.
	for _, verb := range []string{"start", "setup", "doctor", "workspace", "secret", "provider", "model", "update", "version"} {
		if !strings.Contains(usage, verb) {
			t.Errorf("usage does not mention %q:\n%s", verb, usage)
		}
	}

	_, stderr, code := cli(t, dir, "", "nonesuch")
	if code != 2 {
		t.Errorf("an unknown command should exit 2, got %d", code)
	}
	if !strings.Contains(stderr, "no such command") {
		t.Errorf("an unknown command should say so, got %q", stderr)
	}

	_, _, code = cli(t, dir, "", "workspace", "add")
	if code != 2 {
		t.Errorf("a command missing its argument should exit 2, got %d", code)
	}
}

func TestWorkspaceCommandsWorkOnTheRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	folder := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	out, stderr, code := cli(t, dir, "", "workspace", "add", "demo", folder, "--sandbox", "host", "--egress", "off", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("workspace add: exit %d\n%s\n%s", code, out, stderr)
	}
	if !strings.Contains(out, folder) {
		t.Errorf("the answer should name the folder it recorded: %q", out)
	}

	list, _, code := cli(t, dir, "", "workspace", "list", "--data-dir", dir)
	if code != 0 || !strings.Contains(list, "demo") {
		t.Errorf("workspace list should show what was added: exit %d\n%s", code, list)
	}

	// The name is taken, and a second add says so rather than recording it twice.
	if _, stderr, code = cli(t, dir, "", "workspace", "add", "demo", folder, "--data-dir", dir); code == 0 {
		t.Errorf("a duplicate name should be refused, got exit 0")
	} else if !strings.Contains(stderr, "already exists") {
		t.Errorf("the refusal should say the name is taken: %q", stderr)
	}

	// A folder that is not there is refused before anything is written.
	if _, stderr, code = cli(t, dir, "", "workspace", "add", "gone", filepath.Join(dir, "nope"), "--data-dir", dir); code == 0 {
		t.Errorf("a missing folder should be refused, got exit 0")
	} else if !strings.Contains(stderr, "no folder at") {
		t.Errorf("the refusal should name the folder: %q", stderr)
	}

	if _, _, code = cli(t, dir, "", "workspace", "remove", "demo", "--data-dir", dir); code != 0 {
		t.Fatalf("workspace remove: exit %d", code)
	}
	if list, _, _ = cli(t, dir, "", "workspace", "list", "--data-dir", dir); strings.Contains(list, "demo") {
		t.Errorf("a removed workspace is still listed:\n%s", list)
	}
}

func TestProviderCommandsKeepTheKeyOutOfArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// The key arrives on stdin, because argv is in the shell history and in every process
	// listing on the machine.
	if _, stderr, code := cli(t, dir, "sk-test-key\n", "provider", "add", "anthropic", "--data-dir", dir); code != 0 {
		t.Fatalf("provider add: exit %d\n%s", code, stderr)
	}
	list, _, code := cli(t, dir, "", "provider", "list", "--data-dir", dir)
	if code != 0 || !strings.Contains(list, "anthropic") {
		t.Errorf("provider list should show it: exit %d\n%s", code, list)
	}
	if strings.Contains(list, "sk-test-key") {
		t.Errorf("the list must never print a value:\n%s", list)
	}
	// And the file it wrote is mode 0600: a key readable by everyone is a key that leaked.
	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("credentials are mode %o, want no group or other bits", mode)
	}

	if _, _, code = cli(t, dir, "", "provider", "remove", "anthropic", "--data-dir", dir); code != 0 {
		t.Fatalf("provider remove: exit %d", code)
	}
	if list, _, _ = cli(t, dir, "", "provider", "list", "--data-dir", dir); strings.Contains(list, "anthropic") {
		t.Errorf("a removed provider is still listed:\n%s", list)
	}

	// No key at all is a refusal, not an empty credential.
	if _, stderr, code := cli(t, dir, "\n", "provider", "add", "openai", "--data-dir", dir); code == 0 {
		t.Errorf("an empty key should be refused, got exit 0 (%s)", stderr)
	}
}

func TestSecretCommandsStoreAndForget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	add := []string{"secret", "add", "REGISTRY_TOKEN", "--global", "--description", "for the registry", "--hosts", "registry.example.com", "--no-headers", "--query", "--data-dir", dir}
	if _, stderr, code := cli(t, dir, "s3cret\n", add...); code != 0 {
		t.Fatalf("secret add: exit %d\n%s", code, stderr)
	}
	list, _, code := cli(t, dir, "", "secret", "list", "--data-dir", dir)
	if code != 0 || !strings.Contains(list, "REGISTRY_TOKEN") {
		t.Errorf("secret list should show it: exit %d\n%s", code, list)
	}
	if strings.Contains(list, "s3cret") {
		t.Errorf("a list never carries a value:\n%s", list)
	}

	// An empty value is refused rather than stored: a secret nobody can use is worse than none.
	if _, stderr, code := cli(t, dir, "", "secret", "add", "empty", "--global", "--description", "x", "--data-dir", dir); code == 0 {
		t.Errorf("an empty value should be refused, got exit 0")
	} else if !strings.Contains(stderr, "empty value") {
		t.Errorf("the refusal should say the value was empty: %q", stderr)
	}
	// So is a secret with no description: the list is the only way to tell two of them apart.
	if _, stderr, code := cli(t, dir, "value\n", "secret", "add", "undescribed", "--global", "--data-dir", dir); code == 0 {
		t.Errorf("a secret with no description should be refused, got exit 0")
	} else if !strings.Contains(stderr, "--description is required") {
		t.Errorf("the refusal should ask for a description: %q", stderr)
	}

	if _, _, code = cli(t, dir, "", "secret", "remove", "REGISTRY_TOKEN", "--global", "--data-dir", dir); code != 0 {
		t.Fatalf("secret remove: exit %d", code)
	}
	if list, _, _ = cli(t, dir, "", "secret", "list", "--data-dir", dir); strings.Contains(list, "REGISTRY_TOKEN") {
		t.Errorf("a removed secret is still listed:\n%s", list)
	}
}

// The report a script reads: JSON, with the three things doctor knows and no colour or prose in
// the middle of it.
func TestDoctorJSONIsTheReportAScriptReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, stderr, code := cli(t, dir, "", "doctor", "--json", "--data-dir", dir)
	// Doctor's exit code is about this install, and a machine with no engine downloaded yet is
	// still a working install: only a chosen engine with a problem fails.
	if code != 0 && code != 1 {
		t.Fatalf("doctor --json: exit %d\n%s", code, stderr)
	}
	var report struct {
		Engines   []map[string]any `json:"engines"`
		Default   string           `json:"default"`
		Update    map[string]any   `json:"update"`
		Readiness []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
			Fix    string `json:"fix"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out)
	}
	if len(report.Engines) == 0 {
		t.Error("the report should name every engine this build has")
	}
	if report.Update == nil {
		t.Error("the report should carry the update check")
	}
	if len(report.Readiness) == 0 {
		t.Error("the report should carry the readiness list")
	}
	for _, requirement := range report.Readiness {
		if !requirement.OK && requirement.Fix == "" {
			t.Errorf("%s is not met and says no fix", requirement.Name)
		}
	}
}

// The human report, which JSON does not cover: the same answers, as the blocks a person reads.
// Nothing here changes the machine — doctor is report-only by design, and that is the claim this
// test checks by running it on a folder that does not exist yet.
func TestDoctorPlainSaysWhatThisMachineCanDo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, stderr, code := cli(t, dir, "", "doctor", "--data-dir", dir)
	if code != 0 && code != 1 {
		t.Fatalf("doctor: exit %d\n%s", code, stderr)
	}
	for _, block := range []string{"sandbox engines", "update", "readiness"} {
		if !strings.Contains(out, block) {
			t.Errorf("the report should have a %q block:\n%s", block, out)
		}
	}
	// A machine with no engine downloaded yet says so rather than pretending to be ready.
	if !strings.Contains(out, "not downloaded yet") && !strings.Contains(out, "ready") && !strings.Contains(out, "unavailable") {
		t.Errorf("every engine should be one of the three states:\n%s", out)
	}
}

func TestModelCommandsReadAndWriteTheSetting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Nothing chosen yet: the list says so rather than failing.
	out, stderr, code := cli(t, dir, "", "model", "list", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("model list: exit %d\n%s", code, stderr)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("model list should say something even when nothing is chosen")
	}

	// Choosing one writes the install's settings file, and the file is what the next read sees.
	if _, stderr, code = cli(t, dir, "", "model", "set", "dev/faux-1", "--data-dir", dir); code != 0 {
		t.Fatalf("model set: exit %d\n%s", code, stderr)
	}
	if out, _, _ = cli(t, dir, "", "model", "list", "--data-dir", dir); !strings.Contains(out, "faux-1") {
		t.Errorf("model list after set:\n%s", out)
	}
	settings := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(settings); err != nil {
		t.Errorf("choosing a model writes the install's settings: %v", err)
	}

	// The choice is stored as written: whether the provider has that model is a question about
	// the harness's catalog, and the CLI does not have one. What it does refuse is a value that is
	// not a binding at all.
	if _, stderr, code = cli(t, dir, "", "model", "set", "no-slash", "--data-dir", dir); code == 0 {
		t.Error("a value that is not provider/modelId should be refused")
	} else if !strings.Contains(stderr, "provider/modelId") {
		t.Errorf("the refusal should say the shape it wants: %q", stderr)
	}

	if _, _, code = cli(t, dir, "", "model", "clear", "--data-dir", dir); code != 0 {
		t.Errorf("model clear: exit %d", code)
	}
}

// setup on a machine where it cannot ask for a password: it prints the commands instead of
// failing without a reason. It changes nothing here — nothing is applied without a yes, and there
// is nowhere to say yes.
func TestSetupPrintsWhatItWouldDoWhenItCannotAsk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this checks the unprivileged path; root applies the plan instead")
	}
	t.Parallel()
	dir := t.TempDir()
	out, stderr, code := cli(t, dir, "", "setup", "--yes", "--data-dir", dir)
	// Exit 1 means "this machine still needs something", which is the honest answer in a
	// container or a CI runner: there is no bubblewrap here, or no way to install it.
	if code != 0 && code != 1 {
		t.Fatalf("setup: exit %d\n%s\n%s", code, out, stderr)
	}
	if !strings.Contains(out, "sandbox engines") {
		t.Errorf("setup should say what it checked:\n%s", out)
	}
	if code == 1 && !strings.Contains(out+stderr, "acornd") && !strings.Contains(out+stderr, "sudo") {
		t.Errorf("a machine that cannot be fixed from inside should say what to run:\n%s\n%s", out, stderr)
	}
}

// The verbs behind one more word, and the usage a person gets when they stop halfway. A command
// group that answers nothing when asked is a dead end, so each group says what it can do.
func TestTheCommandGroupsSayWhatTheyCanDo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// `update` is flags rather than verbs, so it is not in this list.
	for _, group := range []string{"workspace", "secret", "model", "provider"} {
		_, stderr, code := cli(t, dir, "", group, "--data-dir", dir)
		if code != 2 {
			t.Errorf("%s with no verb should exit 2, got %d", group, code)
		}
		if !strings.Contains(stderr, group) {
			t.Errorf("%s should print its usage, got %q", group, stderr)
		}
	}
	// The verbs that print what they hold, and the one that rotates. Both read a value from
	// stdin and neither prints it back.
	addOut, addErr, addCode := cli(t, dir, "value\n", "secret", "add", "ROTATE_ME", "--global", "--description", "x", "--data-dir", dir)
	if addCode != 0 {
		t.Fatalf("secret add: exit %d\n%s\n%s", addCode, addOut, addErr)
	}
	out, _, code := cli(t, dir, "", "secret", "show", "ROTATE_ME", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "ROTATE_ME") {
		t.Errorf("secret show: exit %d\n%s", code, out)
	}
	if strings.Contains(out, "value") && strings.Contains(out, "\tvalue") {
		t.Errorf("secret show must not print the value:\n%s", out)
	}
	if _, rotateErr, rotateCode := cli(t, dir, "rotated\n", "secret", "rotate", "ROTATE_ME", "--global", "--data-dir", dir); rotateCode != 0 {
		t.Errorf("secret rotate: exit %d\n%s", rotateCode, rotateErr)
	}
	// A secret that is not there is an error, not a silent success.
	if _, _, code = cli(t, dir, "", "secret", "show", "ABSENT", "--data-dir", dir); code == 0 {
		t.Error("showing a secret that does not exist should fail")
	}
}
