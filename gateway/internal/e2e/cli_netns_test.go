package e2e

import (
	"strings"
	"testing"
)

// The command the sandbox starts as: it configures the namespace and then becomes the command it
// was given, so its pid, its exit status and the signals sent to it belong to what runs next.
// Every refusal here is before the exec, because a sandbox that half-configured a namespace and
// then ran anyway is a sandbox with the network nobody asked for.
func TestTheSandboxEntryCommandRefusesBeforeItRunsAnything(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// No command to become: the message says so rather than exec'ing nothing.
	if _, stderr, code := cli(t, dir, "", "internal", "netns"); code == 0 {
		t.Fatalf("netns with no command: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "no command") {
		t.Errorf("the refusal should say there is no command: %q", stderr)
	}

	// No address or gateway: the sandbox would have no network, and the message says to recreate
	// it rather than leaving a namespace that cannot reach anything.
	_, stderr, code := cli(t, dir, "", "internal", "netns", "true")
	if code == 0 {
		t.Fatalf("netns with no address: exit 0\n%s", stderr)
	}
	if !strings.Contains(stderr, "no address or gateway") {
		t.Errorf("the refusal should name what is missing: %q", stderr)
	}

	// A bad flag is the caller's mistake and exits 2, like every other command here.
	if _, _, code := cli(t, dir, "", "internal", "netns", "-nonsense", "true"); code != 2 {
		t.Errorf("an unknown flag: exit %d, want 2", code)
	}
}

// Checking another account's readiness from this one is refused with the way to do it: a report
// gathered as the wrong user would say the wrong thing about bwrap, pasta, and the folders.
func TestCheckingAnotherAccountSaysHowToDoIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, stderr, code := cli(t, dir, "", "setup", "--for", "nobody-at-all", "--data-dir", dir)
	if code == 0 {
		t.Fatalf("setup --for an account that is not here: exit 0\n%s", stderr)
	}
	if !strings.Contains(stderr, "nobody-at-all") {
		t.Errorf("the refusal should name the account: %q", stderr)
	}
}

// The sandbox's supervisor is started by control, on a socket control names, and it says what it
// takes when it is given nothing. A supervisor that guessed a socket would serve a sandbox nobody
// could reach.
func TestTheSandboxSupervisorAsksForItsSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, stderr, code := cli(t, dir, "", "internal", "sandbox-supervisor")
	if code != 2 {
		t.Fatalf("no socket: exit %d, want 2\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "--socket") {
		t.Errorf("the usage should name the flag: %q", stderr)
	}
	if _, _, code := cli(t, dir, "", "internal", "sandbox-supervisor", "-nonsense"); code != 2 {
		t.Errorf("an unknown flag: exit %d, want 2", code)
	}
	if _, _, code := cli(t, dir, "", "internal", "nonsense"); code != 2 {
		t.Errorf("an unknown internal verb: exit %d, want 2", code)
	}
}
