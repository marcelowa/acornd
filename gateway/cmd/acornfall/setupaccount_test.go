package main

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/vault"
)

// Who root is acting for, without being root: the three ways of knowing are all answers about a
// machine, and a test can arrange all three.

func TestRootActsForTheAccountTheEvidenceNames(t *testing.T) {
	dir := t.TempDir()
	// The folder belongs to this account, which is the evidence that survives a container.
	if owner, err := folderOwner(dir); err != nil {
		t.Fatalf("folderOwner: %v", err)
	} else if owner == "" {
		t.Fatal("a folder always has an owner")
	}

	// sudo knows, and what it says comes first: it is the account whose password opened the door.
	t.Setenv("SUDO_USER", currentAccount(t))
	account, found := actingFor(dir)
	if !found {
		t.Fatal("SUDO_USER should be evidence")
	}
	if account.Basis != "the account that ran sudo" {
		t.Errorf("basis = %q", account.Basis)
	}

	// A sudo account that does not exist here is not evidence: the folder is the next answer.
	t.Setenv("SUDO_USER", "nobody-at-all")
	account, found = actingFor(dir)
	if !found {
		t.Fatal("the folder should be evidence when sudo names nobody")
	}
	if !strings.Contains(account.Basis, "owns") {
		t.Errorf("basis = %q, want the folder's owner", account.Basis)
	}

	// Nothing to go on is still an answer: root, with a basis that says why.
	t.Setenv("SUDO_USER", "")
	savedLookup := serviceUserLookup
	serviceUserLookup = func() string { return "" }
	t.Cleanup(func() { serviceUserLookup = savedLookup })
	account, err := accountFor("", true, filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if account.Name == "" {
		t.Error("an account is always named")
	}
	if !strings.Contains(account.Basis, "nothing says otherwise") {
		t.Errorf("basis = %q, want it to say nothing said otherwise", account.Basis)
	}
}

// The service unit's account is the machine's own record, and it beats the guess.
func TestTheServiceAccountIsWhatItSaysItIs(t *testing.T) {
	saved := serviceUserLookup
	t.Cleanup(func() { serviceUserLookup = saved })
	serviceUserLookup = func() string { return currentAccount(t) }
	// No folder to own and no SUDO_USER: the unit is the only thing left that can say.
	t.Setenv("SUDO_USER", "")
	account, found := actingFor(filepath.Join(t.TempDir(), "missing"))
	if !found {
		t.Fatal("the service account should be evidence")
	}
	if account.Basis != "the acornfall service runs as "+currentAccount(t) {
		t.Errorf("basis = %q", account.Basis)
	}
}

// An account's groups are what a login would give it, which is what one of the probes asks.
func TestAnAccountKnowsItsGroups(t *testing.T) {
	ids, err := groupIDsFor(currentAccount(t))
	if err != nil {
		t.Fatalf("groupIDsFor: %v", err)
	}
	if len(ids) == 0 {
		t.Error("an account is in at least its own group")
	}
	if _, err := groupIDsFor("nobody-at-all"); err == nil {
		t.Error("an account that is not here has no groups to give")
	}
}

func TestNoServiceAccountWhenThereIsNoService(t *testing.T) {
	// The machine decides this, not the test: what matters is that the answer is a name or empty,
	// never a panic or "root".
	if name := serviceUserLookup(); name == "root" {
		t.Error("root is not an answer here: it is the absence of one")
	}
}

func currentAccount(t *testing.T) string {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return account.Username
}

// A workspace is found by name in the registry, and a name that is not there is told what is.
func TestAWorkspaceIsFoundByNameOrSaidToBeMissing(t *testing.T) {
	dir := t.TempDir()
	registry := control.RegistryFile{Path: filepath.Join(dir, "workspaces.json")}
	registry.Workspaces = append(registry.Workspaces,
		control.Workspace{Name: "general", Path: filepath.Join(dir, "general"), General: true},
		control.Workspace{Name: "work", Path: filepath.Join(dir, "work")},
	)
	if err := control.SaveRegistryFile(registry); err != nil {
		t.Fatal(err)
	}
	path, err := workspacePath(dir, "work")
	if err != nil {
		t.Fatalf("workspacePath: %v", err)
	}
	if path != filepath.Join(dir, "work") {
		t.Errorf("path = %q", path)
	}
	_, err = workspacePath(dir, "nope")
	if err == nil {
		t.Fatal("a workspace that is not there should be an error")
	}
	if !strings.Contains(err.Error(), "general") || !strings.Contains(err.Error(), "work") {
		t.Errorf("the answer should list what there is: %v", err)
	}
	// A data folder with no registry at all says that, rather than naming nothing.
	if _, err := workspacePath(t.TempDir(), "work"); err == nil || !strings.Contains(err.Error(), "none are registered") {
		t.Errorf("an empty install should say so: %v", err)
	}
}

// A path printed for a person to paste has to survive a shell: the quote is the whole job, and a
// path with a quote in it is the case that matters.
func TestAPrintedCommandSurvivesAShell(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"/a b/c":      "'/a b/c'",
		"it's":        `'it'"'"'s'`,
		"$(rm -rf /)": "'$(rm -rf /)'",
		"a;b":         "'a;b'",
	}
	for input, want := range cases {
		if got := shellQuote(input); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

// The effective list is what one workspace actually receives: the source of each, and the one
// note that matters — a workspace's own entry over an install-wide one.
func TestTheEffectiveListSaysWhereEachSecretCameFrom(t *testing.T) {
	empty := captureStdout(t, func() { printEffective(nil) })
	if !strings.Contains(empty, "no secrets for this workspace") {
		t.Errorf("nothing stored should say so: %q", empty)
	}
	out := captureStdout(t, func() {
		printEffective([]vault.Meta{
			{Name: "GLOBAL_TOKEN", Source: "global", Description: "install-wide", Hosts: []string{"a.example.com"}, AllowEnv: true},
			{Name: "WORK_TOKEN", Source: "workspace", Description: "this one", Overrides: true},
		})
	})
	for _, want := range []string{"NAME", "SOURCE", "GLOBAL_TOKEN", "global", "install-wide", "a.example.com", "yes", "overrides global"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list should carry %q:\n%s", want, out)
		}
	}
	// The header is the contract: a column that is not named is a column nobody reads.
	for _, column := range []string{"HOSTS", "ENV", "ROTATED", "NOTE"} {
		if !strings.Contains(out, column) {
			t.Errorf("the list should have a %s column:\n%s", column, out)
		}
	}
}
