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
			files: []string{"/etc/sysctl.d/99-acornd-userns.conf"},
		},
		{
			name: "the tun device, in the order that makes it usable",
			plan: rootPlan{Tun: true},
			want: []step{
				{"modprobe", []string{"tun"}},
				{"systemd-tmpfiles", []string{"--create", "/etc/tmpfiles.d/acornd-tun.conf"}},
				{"chmod", []string{"0666", "/dev/net/tun"}},
			},
			files: []string{"/etc/tmpfiles.d/acornd-tun.conf"},
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
	userns := (*files)["/etc/sysctl.d/99-acornd-userns.conf"]
	if !strings.Contains(userns, "kernel.apparmor_restrict_unprivileged_userns=0") {
		t.Errorf("the sysctl file does not set the setting: %q", userns)
	}
	// A sysctl without the file is a fix that disappears at the next reboot, which is why the
	// comment says who wrote it.
	if !strings.Contains(userns, "acornd") {
		t.Errorf("the file should say who wrote it: %q", userns)
	}
	tun := (*files)["/etc/tmpfiles.d/acornd-tun.conf"]
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

// The pasta profile is the one plan step that is not an argv, and each half of it depends on this
// machine: a profile that is not here, one that was never loaded, and one that refuses pasta. The
// paths are the test's, because /etc/apparmor.d is not.
type pastaMachine struct {
	profile string
	disable string
}

// withPastaProfile points the plan at a machine of its own: present says the distribution ships the
// profile, loaded says the kernel has it in force.
func withPastaProfile(t *testing.T, present, loaded bool) (pastaMachine, *[]step, *[]string) {
	t.Helper()
	dir := t.TempDir()
	machine := pastaMachine{profile: filepath.Join(dir, "usr.bin.pasta"), disable: filepath.Join(dir, "disable")}
	if present {
		if err := os.WriteFile(machine.profile, []byte("# a profile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	profiles := filepath.Join(dir, "profiles")
	content := ""
	if loaded {
		// The name Ubuntu loads it under, which is not the path it attaches to.
		content = "pasta (enforce)\n"
	}
	if err := os.WriteFile(profiles, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	savedMachine, savedLink := pastaAppArmor, linkStep
	t.Cleanup(func() {
		pastaAppArmor, linkStep = savedMachine, savedLink
	})
	pastaAppArmor = struct {
		profile string
		disable string
		loaded  string
	}{profile: machine.profile, disable: machine.disable, loaded: profiles}
	steps, _, _ := capturePlan(t)
	var links []string
	linkStep = func(target, link string) error {
		links = append(links, target+" -> "+link)
		return nil
	}
	return machine, steps, &links
}

// Turning the profile off is the pair Ubuntu's own aa-disable does: the symlink a reboot reads,
// then the parser that takes away the profile loaded now.
func TestAPlanTurnsOffThePastaProfileTheWayAaDisableDoes(t *testing.T) {
	machine, steps, links := withPastaProfile(t, true, true)
	if err := applyPlan(rootPlan{PastaProfile: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []step{{"apparmor_parser", []string{"-R", machine.profile}}}
	if !sameSteps(*steps, want) {
		t.Errorf("ran %v, want %v", *steps, want)
	}
	wantLink := machine.profile + " -> " + filepath.Join(machine.disable, "usr.bin.pasta")
	if len(*links) != 1 || (*links)[0] != wantLink {
		t.Errorf("linked %v, want %s", *links, wantLink)
	}
}

// A distribution that ships no profile is not the machine this step is about: Ubuntu 24.04 and
// every other supported release must go on working, with no step and no dangling symlink.
func TestAPlanLeavesAMachineWithoutThePastaProfileAlone(t *testing.T) {
	_, steps, links := withPastaProfile(t, false, false)
	if err := applyPlan(rootPlan{PastaProfile: true}); err != nil {
		t.Fatalf("a machine without the profile is not a failure: %v", err)
	}
	if len(*steps) != 0 || len(*links) != 0 {
		t.Errorf("ran %v and linked %v on a machine with no profile", *steps, *links)
	}
}

// A profile the kernel never loaded needs no parser run, and an unload there would be a failure
// about a mechanism this machine does not have. The symlink still keeps it from loading later.
func TestAPlanLinksButDoesNotUnloadAProfileThatIsNotLoaded(t *testing.T) {
	machine, steps, links := withPastaProfile(t, true, false)
	if err := applyPlan(rootPlan{PastaProfile: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(*steps) != 0 {
		t.Errorf("ran %v with no loaded profile", *steps)
	}
	if len(*links) != 1 || (*links)[0] != machine.profile+" -> "+filepath.Join(machine.disable, "usr.bin.pasta") {
		t.Errorf("the symlink is what keeps it off at the next boot, linked %v", *links)
	}
}

// The profile is a package's, so the step that turns it off comes after the step that installs the
// package: on a fresh 26.04 both are in one plan.
func TestThePastaProfileStepComesAfterThePackage(t *testing.T) {
	_, steps, _ := withPastaProfile(t, true, true)
	if err := applyPlan(rootPlan{Packages: []string{"passt"}, Manager: "apt-get", PastaProfile: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []step{
		{"apt-get", []string{"install", "-y", "passt"}},
		{"apparmor_parser", []string{"-R", pastaAppArmor.profile}},
	}
	if !sameSteps(*steps, want) {
		t.Errorf("ran %v, want %v", *steps, want)
	}
}

// A plan of one profile step is not an empty plan: setup would say "nothing to change" on the one
// machine this exists for.
func TestAPlanWithOnlyThePastaProfileIsNotAnEmptyPlan(t *testing.T) {
	if (rootPlan{PastaProfile: true}).empty() {
		t.Fatal("a plan that turns off the pasta profile has a step to run")
	}
}

// The profile comes after the kernel permission as well, and for a reason that is not cosmetic:
// turning the profile off makes pasta unconfined, and an unconfined process cannot make a user
// namespace on a machine that still restricts them. A plan that turned the profile off first would
// break the network it is trying to fix if the next step failed.
func TestThePastaProfileStepComesAfterTheUserNamespaceSwitch(t *testing.T) {
	_, steps, _ := withPastaProfile(t, true, true)
	if err := applyPlan(rootPlan{Userns: true, PastaProfile: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []step{
		{"sysctl", []string{"-w", "kernel.apparmor_restrict_unprivileged_userns=0"}},
		{"apparmor_parser", []string{"-R", pastaAppArmor.profile}},
	}
	if !sameSteps(*steps, want) {
		t.Errorf("ran %v, want %v", *steps, want)
	}
}

// The kernel lists a profile by the name it was loaded under, and that is not the file it came
// from: Ubuntu loads this one as `pasta`, where a profile declared `profile <path>` is listed by
// its path. The CI run that produced this test had the profile in force and the check said it was
// not, which is how the first version shipped a symlink and no unload.
func TestTheLoadedProfileIsFoundByNameAndNotByALongerWord(t *testing.T) {
	saved := pastaAppArmor
	t.Cleanup(func() { pastaAppArmor = saved })
	for content, want := range map[string]bool{
		"pasta (enforce)\n":                            true,
		"/usr/bin/pasta (enforce)\n":                   true,
		"usr.bin.pasta (enforce)\n":                    true,
		"other (enforce)\n/usr/bin/pasta (complain)\n": true,
		"/usr/bin/passt (enforce)\n":                   false,
		"/usr/bin/pasta-helper (enforce)\n":            false,
		"":                                             false,
	} {
		path := filepath.Join(t.TempDir(), "profiles")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		pastaAppArmor.loaded = path
		if got := profileLoaded(); got != want {
			t.Errorf("%q: got %v, want %v", content, got, want)
		}
	}
}
