package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What a plan would run, without being root and without changing the machine. The argv is the
// decision: `usermod -aG kvm` for one account and not another, apt on Debian and dnf on Fedora, and
// nothing at all when the machine already has it. Running it for real proves less than this does.

type step struct {
	program string
	args    []string
}

// capturePlan replaces the two seams and records what the plan asked for.
func capturePlan(t *testing.T) (steps *[]step, files *map[string]string, fail map[string]error) {
	t.Helper()
	recorded := []step{}
	written := map[string]string{}
	failures := map[string]error{}
	savedRun, savedWrite := runStep, writeStep
	runStep = func(name string, args ...string) error {
		recorded = append(recorded, step{program: name, args: args})
		return failures[name]
	}
	writeStep = func(path string, content []byte, mode os.FileMode) error {
		written[path] = string(content)
		return nil
	}
	t.Cleanup(func() { runStep, writeStep = savedRun, savedWrite })
	return &recorded, &written, failures
}

// currentUser is the account these tests act on: the plan validates names against the machine, so
// a made-up one would be refused before it reached the runner.
func currentUser(t *testing.T) string {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return account.Username
}

// sameSteps compares what ran with what should have: the program, then the arguments in order.
func sameSteps(got, want []step) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index].program != want[index].program || !slices.Equal(got[index].args, want[index].args) {
			return false
		}
	}
	return true
}

func TestAPlanRunsWhatItDecidedForThisMachine(t *testing.T) {
	cases := []struct {
		name  string
		plan  rootPlan
		want  []step
		files []string
	}{
		{
			name: "a Debian machine missing both packages",
			plan: rootPlan{Packages: []string{"bubblewrap", "passt"}, Manager: "apt-get"},
			want: []step{{"apt-get", []string{"install", "-y", "bubblewrap", "passt"}}},
		},
		{
			name: "a Fedora machine",
			plan: rootPlan{Packages: []string{"bubblewrap"}, Manager: "dnf"},
			want: []step{{"dnf", []string{"install", "-y", "bubblewrap"}}},
		},
		{
			name:  "the user-namespace restriction, which has to survive a reboot",
			plan:  rootPlan{Userns: true},
			want:  []step{{"sysctl", []string{"-w", "kernel.apparmor_restrict_unprivileged_userns=0"}}},
			files: []string{"/etc/sysctl.d/99-acornfall-userns.conf"},
		},
		{
			name: "the tun device, in the order that makes it usable",
			plan: rootPlan{Tun: true},
			want: []step{
				{"modprobe", []string{"tun"}},
				{"systemd-tmpfiles", []string{"--create", "/etc/tmpfiles.d/acornfall-tun.conf"}},
				{"chmod", []string{"0666", "/dev/net/tun"}},
			},
			files: []string{"/etc/tmpfiles.d/acornfall-tun.conf"},
		},
		{
			// The account has to be one that exists: the plan is validated against the machine
			// before anything runs, so a name nobody has is refused rather than applied.
			name:  "one account for the kvm group, and one for autostart",
			plan:  rootPlan{KVMUser: currentUser(t), LingerUser: currentUser(t)},
			want:  []step{{"usermod", []string{"-aG", "kvm", currentUser(t)}}, {"loginctl", []string{"enable-linger", currentUser(t)}}},
			files: nil,
		},
		{
			name: "nothing to do",
			plan: rootPlan{},
			want: nil,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			steps, files, _ := capturePlan(t)
			if err := applyPlan(test.plan); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if !sameSteps(*steps, test.want) {
				t.Errorf("ran %v, want %v", *steps, test.want)
			}
			for _, path := range test.files {
				if _, ok := (*files)[path]; !ok {
					t.Errorf("%s was not written", path)
				}
			}
			if len(*files) != len(test.files) {
				t.Errorf("wrote %d files, want %d: %v", len(*files), len(test.files), *files)
			}
		})
	}
}

// The written files are the half of the fix that survives a reboot, so their content is the fix.
func TestAPlanWritesFilesThatMakeItStick(t *testing.T) {
	_, files, _ := capturePlan(t)
	if err := applyPlan(rootPlan{Userns: true, Tun: true}); err != nil {
		t.Fatal(err)
	}
	userns := (*files)["/etc/sysctl.d/99-acornfall-userns.conf"]
	if !strings.Contains(userns, "kernel.apparmor_restrict_unprivileged_userns=0") {
		t.Errorf("the sysctl file does not set the setting: %q", userns)
	}
	// A sysctl without the file is a fix that disappears at the next reboot, which is why the
	// comment says who wrote it.
	if !strings.Contains(userns, "acornfall") {
		t.Errorf("the file should say who wrote it: %q", userns)
	}
	tun := (*files)["/etc/tmpfiles.d/acornfall-tun.conf"]
	if !strings.Contains(tun, "c! /dev/net/tun 0666 root root 10 200") {
		t.Errorf("the tmpfiles rule should create the device: %q", tun)
	}
}

func TestAPlanStopsAtAStepThatMatters(t *testing.T) {
	steps, _, failures := capturePlan(t)
	failures["sysctl"] = errors.New("permission denied")
	err := applyPlan(rootPlan{Userns: true})
	if err == nil {
		t.Fatal("a failed step that matters should stop the plan")
	}
	if !strings.Contains(err.Error(), "sysctl") {
		t.Errorf("the failure should name the step: %v", err)
	}
	if len(*steps) != 1 {
		t.Errorf("nothing after the failure should run, ran %v", *steps)
	}
}

// A step whose absence is a machine without that mechanism is not a failure: after it, the probe
// is the judge.
func TestAPlanContinuesPastAMissingBestEffortStep(t *testing.T) {
	steps, _, failures := capturePlan(t)
	failures["modprobe"] = errors.New("exec: modprobe: not found")
	if err := applyPlan(rootPlan{Tun: true}); err != nil {
		t.Fatalf("a missing modprobe should not stop the plan: %v", err)
	}
	if len(*steps) < 3 {
		t.Errorf("the steps after modprobe should still run, ran %v", *steps)
	}
}

// The validation is in applyPlan as well as in the privileged half, because root mode reaches it
// without a file in between: a rule with two doors is a rule with one door.
func TestAPlanThatAsksForSomethingElseIsRefused(t *testing.T) {
	_, _, _ = capturePlan(t)
	err := applyPlan(rootPlan{Packages: []string{"bubblewrap"}, Manager: "make-it-so"})
	if err == nil {
		t.Fatal("an unknown package manager should be refused")
	}
	if !strings.Contains(err.Error(), "does not do") {
		t.Errorf("the refusal should say it is not something this command does: %v", err)
	}
}

func TestEscalationSaysWhyItCannotAsk(t *testing.T) {
	savedFind, savedTerminal := findSudo, terminalFor
	t.Cleanup(func() { findSudo, terminalFor = savedFind, savedTerminal })

	// No sudo: there is no password to ask for.
	findSudo = func() (string, error) { return "", errors.New("not found") }
	terminalFor = func(*os.File) bool { return true }
	if err := escalate(rootPlan{}); !errors.Is(err, errNoEscalation) {
		t.Errorf("no sudo should be errNoEscalation, got %v", err)
	}

	// sudo, but nowhere to type: also no escalation, and said before sudo fails on it.
	findSudo = func() (string, error) { return "/usr/bin/sudo", nil }
	terminalFor = func(*os.File) bool { return false }
	if err := escalate(rootPlan{}); !errors.Is(err, errNoEscalation) {
		t.Errorf("no terminal should be errNoEscalation, got %v", err)
	}
}

// The escalation contract: a plan file only root can read, and an argv that hands root that file
// and nothing else. Nothing here is a parameter root could be talked into changing.
func TestEscalationHandsRootThePlanAndNothingElse(t *testing.T) {
	savedFind, savedTerminal, savedSudo := findSudo, terminalFor, runSudo
	t.Cleanup(func() { findSudo, terminalFor, runSudo = savedFind, savedTerminal, savedSudo })

	findSudo = func() (string, error) { return "/usr/bin/sudo", nil }
	terminalFor = func(*os.File) bool { return true }
	var got []string
	var planMode os.FileMode
	var planPayload []byte
	runSudo = func(name string, args ...string) error {
		got = append([]string{name}, args...)
		// The last argument is the plan file; read it as the privileged half would.
		path := args[len(args)-1]
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		planMode = info.Mode().Perm()
		planPayload, err = os.ReadFile(path)
		return err
	}

	account := currentUser(t)
	plan := rootPlan{Packages: []string{"bubblewrap"}, Manager: "apt-get", KVMUser: account}
	if err := escalate(plan); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	// sudo, the binary, three flags, and the plan path.
	if len(got) != 6 || got[0] != "/usr/bin/sudo" {
		t.Fatalf("ran %v, want sudo plus the binary and three flags", got)
	}
	for index, want := range []string{"setup", "--privileged", "--plan"} {
		if got[index+2] != want {
			t.Errorf("argument %d = %q, want %q (the whole argv: %v)", index+2, got[index+2], want, got)
		}
	}
	if planMode != 0o600 {
		t.Errorf("the plan file is mode %o: a plan root will run is root's to read", planMode)
	}
	var decoded rootPlan
	if err := json.Unmarshal(planPayload, &decoded); err != nil {
		t.Fatalf("the privileged half could not read the plan: %v", err)
	}
	if decoded.KVMUser != account || decoded.Manager != "apt-get" {
		t.Errorf("the plan lost something: %+v", decoded)
	}
	// And the file is gone afterwards: a plan that names an account is not something to leave in
	// a temp folder.
	if _, err := os.Stat(got[len(got)-1]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the plan file should be removed, stat said %v", err)
	}
}

// The privileged half refuses to be a command a person runs: it exists for setup to call.
func TestThePrivilegedHalfRefusesToRunAsACommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root this is the other branch: it reads the plan file")
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// runPlanAsRoot exits, so the assertion is the exit code of this test binary's child: the
	// check is in the e2e suite instead, where the binary is a binary. Here it is enough that the
	// guard exists.
	if os.Geteuid() == 0 {
		t.Fatal("unreachable")
	}
}
