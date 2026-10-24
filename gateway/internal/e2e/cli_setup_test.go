package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// The probe is how setup asks another account what it can do: root re-executes this binary with
// that account's credentials and reads one JSON object back. It is an internal contract, and a
// contract nobody tests is one that breaks silently in the middle of somebody's first install.
func TestTheProbeAnswersTheChecksAsJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, stderr, code := cli(t, dir, "", "setup", "--probe", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("setup --probe: exit %d\n%s", code, stderr)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("the probe is not one JSON object: %v\n%s", err, out)
	}
	// The shape the parent decodes, by the names it reads. Every field after the first is
	// omitempty, so what the contract really promises is the package manager and that the rest,
	// when present, are the ones it knows.
	if _, found := probe["packageManager"]; !found {
		t.Errorf("the probe should always name the package manager: %v", probe)
	}
	known := map[string]bool{
		"packageManager": true, "bwrapPath": true, "bwrapVersion": true, "bwrapProblem": true,
		"pastaPath": true, "pastaProblem": true, "tunProblem": true, "usernsRestricted": true,
		"container": true, "kvmProblem": true, "lingerKnown": true, "linger": true,
		"installDir": true, "installProblem": true,
	}
	for key := range probe {
		if !known[key] {
			t.Errorf("the probe answered %q, which the parent does not read: a field nobody reads is one that cannot be trusted to mean anything", key)
		}
	}
}

// The half that runs as root is not a command for hands: run by anyone else it says so and stops,
// because a person who typed it meant to run setup, and setup is the thing that asks properly.
func TestTheRootHalfRefusesToBeRunByHand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, stderr, code := cli(t, dir, "", "setup", "--privileged", "--plan", dir+"/plan.json", "--data-dir", dir)
	if code != 2 {
		t.Fatalf("setup --privileged as a user: exit %d, want 2\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "not a command to run by hand") {
		t.Errorf("the refusal should say what it is: %q", stderr)
	}
}

// A secret belongs to a scope, and the flags have to name exactly one: both is ambiguous, and
// neither is a secret nobody can find.
func TestASecretScopeMustBeNamedOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if _, stderr, code := cli(t, dir, "value\n", "secret", "add", "X", "--description", "d", "--data-dir", dir); code == 0 {
		t.Errorf("no scope: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "name one scope") {
		t.Errorf("the refusal should ask for one: %q", stderr)
	}

	if _, stderr, code := cli(t, dir, "value\n", "secret", "add", "X", "--global", "--workspace", "general", "--description", "d", "--data-dir", dir); code == 0 {
		t.Errorf("two scopes: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "not both") {
		t.Errorf("the refusal should say not both: %q", stderr)
	}

	// A workspace that is not in the registry is refused before anything is written: a secret
	// filed under a folder nobody has would never be read.
	if _, stderr, code := cli(t, dir, "value\n", "secret", "add", "X", "--workspace", "nowhere", "--description", "d", "--data-dir", dir); code == 0 {
		t.Errorf("an unknown workspace: exit 0\n%s", stderr)
	} else if !strings.Contains(stderr, "nowhere") {
		t.Errorf("the refusal should name the workspace: %q", stderr)
	}
}
