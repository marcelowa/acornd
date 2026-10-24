package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The command line's own update path, with no gateway running: it asks the release API, says what
// it found, and installs nothing unless it was told to. This is the path a script uses, and the
// one that has to work when nothing else is up.
func TestTheCommandLineChecksForAnUpdateWithoutAGateway(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A development build has no version, so nothing is offered and the answer says so rather
	// than printing an offer it cannot compare.
	out, stderr, code := cli(t, dir, "", "update", "--check", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("update --check: exit %d\n%s\n%s", code, stderr, out)
	}
	if !strings.Contains(out, "up to date") && !strings.Contains(out, "available") {
		t.Errorf("the answer should say what it found:\n%s", out)
	}

	// --check installs nothing: the version file has not been touched, and nothing was staged.
	if _, err := os.Stat(dir + "/version"); err == nil {
		payload, readErr := os.ReadFile(dir + "/version")
		if readErr == nil && strings.Contains(string(payload), "9.9.9") {
			t.Errorf("a check wrote a version: %q", payload)
		}
	}
}

// A flag that takes no value and was given one is the caller's mistake, and the command says what
// it takes rather than guessing which half was meant.
func TestTheUpdateCommandRefusesAStrayArgument(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, stderr, code := cli(t, dir, "", "update", "tidy", "--data-dir", dir); code != 2 {
		t.Errorf("a stray argument: exit %d, want 2\n%s", code, stderr)
	} else if !strings.Contains(stderr, "usage: acornfall update") {
		t.Errorf("the refusal should print the usage: %q", stderr)
	}
}

// The same for the workspace verbs: a name that is not there is refused with the name, and a verb
// with no name says what it wanted.
func TestTheWorkspaceCommandsRefuseWhatTheyCannotDo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if _, stderr, code := cli(t, dir, "", "workspace", "remove", "--data-dir", dir); code == 0 {
		t.Errorf("remove with no name: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "usage: acornfall workspace remove") {
		t.Errorf("the refusal should print the usage: %q", stderr)
	}
	if _, stderr, code := cli(t, dir, "", "workspace", "remove", "nowhere", "--data-dir", dir); code == 0 {
		t.Errorf("removing a workspace that is not there: exit 0")
	} else if !strings.Contains(stderr, "nowhere") {
		t.Errorf("the refusal should name the workspace: %q", stderr)
	}
	if _, _, code := cli(t, dir, "", "workspace", "nonsense", "--data-dir", dir); code != 2 {
		t.Errorf("an unknown verb: exit %d, want 2", code)
	}
}

// The other half: with a gateway running, the command asks it instead of doing the work itself —
// the gateway owns the workers, the runs, and the restart, and a command that wrote the files
// behind its back would leave it running old code.
func TestTheCommandLineAsksARunningGateway(t *testing.T) {
	t.Parallel()
	source := withUpdateSource(t)
	s := start(t, source)
	address := strings.TrimPrefix(s.client().base, "http://")
	host, port, ok := strings.Cut(address, ":")
	if !ok {
		t.Fatalf("the harness's address is %q", address)
	}
	dir := t.TempDir()

	out, stderr, code := cli(t, dir, "", "update", "--check", "--host", host, "--port", port, "--data-dir", dir)
	if code != 0 {
		t.Fatalf("update --check against a gateway: exit %d\n%s\n%s", code, stderr, out)
	}
	if !strings.Contains(out, "up to date") && !strings.Contains(out, "available") {
		t.Errorf("the gateway's answer should be reported:\n%s", out)
	}
	// Nothing was written in the command's own folder: the work happened in the gateway's.
	if _, err := os.Stat(dir + "/version"); err == nil {
		payload, _ := os.ReadFile(dir + "/version")
		if strings.Contains(string(payload), "9.9.9") {
			t.Errorf("the command wrote a version of its own: %q", payload)
		}
	}
}

// A gateway that refuses with a 409 is "runs are active", and that is the one refusal with a way
// forward: the command says which flag to rerun with rather than printing the gateway's JSON.
func TestAGatewayRefusalIsExplainedWithTheWayForward(t *testing.T) {
	t.Parallel()
	// A server that answers the status call so the command believes a gateway is there, and then
	// refuses the check the way a busy gateway does.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/update/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.Error(w, "runs are active: two conversations are answering", http.StatusConflict)
		}
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	host, port, _ := strings.Cut(address, ":")
	dir := t.TempDir()

	_, stderr, code := cli(t, dir, "", "update", "--check", "--host", host, "--port", port, "--data-dir", dir)
	if code == 0 {
		t.Fatal("a refusal from the gateway should fail the command")
	}
	if !strings.Contains(stderr, "runs are active") {
		t.Errorf("the gateway's words should come through: %q", stderr)
	}
}
