package main

import (
	"errors"
	"os"
	"os/user"
	"strings"
	"testing"
)

// working is a machine that can run a workspace, so a test can change one thing about it.
func working() setupReport {
	return setupReport{
		PackageManager: "apt-get",
		BwrapPath:      "/usr/bin/bwrap",
		BwrapVersion:   "0.11.0",
		PastaPath:      "/usr/bin/pasta",
		LingerKnown:    true,
		Linger:         true,
		InstallDir:     "/home/me/.acornfall/bin",
	}
}

// A machine that can run a workspace says so, and has no work for root.
func TestSetupSaysWhenNothingIsMissing(t *testing.T) {
	report := working()
	text, ok := renderSetup(report)
	if !ok {
		t.Fatalf("a working machine should report ok:\n%s", text)
	}
	if !strings.Contains(text, "runs workspaces on bubblewrap") {
		t.Fatalf("the summary should name the engine:\n%s", text)
	}
	if plan := planFor(report, "me"); !plan.empty() {
		t.Fatalf("nothing is missing, so there is nothing to do: %+v", plan)
	}
}

// The plan is the fix: a missing package becomes the one step that installs it, with this
// distribution's own package manager.
func TestSetupPlansTheMissingPackage(t *testing.T) {
	report := working()
	report.PastaPath = ""
	report.PastaErr = errors.New("sandbox unavailable: egress needs pasta")
	plan := planFor(report, "me")
	if len(plan.Packages) != 1 || plan.Packages[0] != "passt" {
		t.Fatalf("want passt, got %+v", plan)
	}
	if plan.Manager != "apt-get" {
		t.Fatalf("the manager should be this distribution's, got %q", plan.Manager)
	}
	steps := plan.actions()
	if len(steps) != 1 || strings.Join(steps[0].Run, " ") != "apt-get install -y passt" {
		t.Fatalf("want `apt-get install -y passt`, got %+v", steps)
	}
}

// A distribution this command does not know is not guessed at: the plan cannot carry a package,
// and the printed list says to use the machine's own package manager.
func TestSetupRefusesToGuessAPackageManager(t *testing.T) {
	report := working()
	report.PackageManager = "install-with-your-package-manager"
	report.PastaPath = ""
	report.BwrapPath = ""
	plan := planFor(report, "me")
	if len(plan.Packages) != 0 {
		t.Fatalf("an unknown package manager should not be put in a plan: %+v", plan)
	}
	fixes := strings.Join(printedFixes(report, "me"), "\n")
	if !strings.Contains(fixes, "with this distribution's package manager") {
		t.Fatalf("the printed list should say what to do:\n%s", fixes)
	}
}

// The user-namespace policy is only proposed when the probe failed and the machine has the
// AppArmor gate: a sysctl on a machine that already works would relax a policy for nothing.
func TestSetupOnlyRelaxesUserNamespacesWhenThatIsTheCause(t *testing.T) {
	report := working()
	report.BwrapErr = errors.New("sandbox unavailable: bubblewrap cannot create a user namespace here")
	if plan := planFor(report, "me"); plan.Userns {
		t.Fatal("a machine without the AppArmor gate should not be given the sysctl")
	}
	report.UsernsRestricted = true
	plan := planFor(report, "me")
	if !plan.Userns {
		t.Fatal("a refused namespace on a restricted machine should be the sysctl")
	}
	// And it stays fixed after a reboot, which is why the plan writes a file as well.
	var wrote bool
	for _, action := range plan.actions() {
		if action.File == "/etc/sysctl.d/99-acornfall-userns.conf" && strings.Contains(action.Write, "apparmor_restrict_unprivileged_userns=0") {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("the policy has to survive a reboot: %+v", plan.actions())
	}
}

// In a container the sysctl is not the answer: the policy belongs to whoever started the
// container, and the advice says so instead of proposing something that cannot work.
func TestSetupDoesNotProposeASysctlInsideAContainer(t *testing.T) {
	report := working()
	report.BwrapErr = errors.New("sandbox unavailable: bubblewrap cannot create a user namespace here")
	report.UsernsRestricted = true
	report.Container = true
	if plan := planFor(report, "me"); plan.Userns || plan.Tun {
		t.Fatalf("a container needs its host changed, not its own policy: %+v", plan)
	}
	if advice := containerAdvice(report); !strings.Contains(advice, "--security-opt seccomp=unconfined") {
		t.Fatalf("the advice should name the flags: %q", advice)
	}
}

// A missing tun device is planned as a device, and the row names it rather than blaming pasta's
// namespace setup.
func TestSetupPlansTheTunDevice(t *testing.T) {
	report := working()
	report.TunErr = errors.New("/dev/net/tun is missing: pasta builds the sandbox's network from it")
	report.PastaErr = errors.New("sandbox unavailable: pasta cannot make a network namespace here: operation not permitted")
	plan := planFor(report, "me")
	if !plan.Tun {
		t.Fatalf("a missing tun device should be planned: %+v", plan)
	}
	text, ok := renderSetup(report)
	if ok {
		t.Fatalf("a machine with no tun cannot carry a network:\n%s", text)
	}
	if !strings.Contains(text, "/dev/net/tun is missing") {
		t.Fatalf("the row should name the device, not pasta's namespace:\n%s", text)
	}
	// The rule is persistent, so the fix survives a reboot.
	var persistent bool
	for _, action := range plan.actions() {
		if action.File == "/etc/tmpfiles.d/acornfall-tun.conf" && strings.Contains(action.Write, "c! /dev/net/tun 0666") {
			persistent = true
		}
	}
	if !persistent {
		t.Fatalf("the device rule has to survive a reboot: %+v", plan.actions())
	}
}

// The machine steps run in an order that works: the tool before the grant that uses it.
func TestSetupOrdersTheMachineSteps(t *testing.T) {
	report := working()
	report.BwrapPath = ""
	report.PastaPath = ""
	report.KVMErr = errors.New("sandbox unavailable: /dev/kvm is missing")
	report.Linger = false
	plan := planFor(report, "marko")
	var runs []string
	for _, action := range plan.actions() {
		if action.Run != nil {
			runs = append(runs, strings.Join(action.Run, " "))
		}
	}
	want := []string{"apt-get install -y bubblewrap passt", "usermod -aG kvm marko", "loginctl enable-linger marko"}
	if strings.Join(runs, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v, got %v", want, runs)
	}
}

// A plan is data, and the privileged half is the only thing standing between a file and root's
// shell: anything outside its vocabulary is refused, including a user that does not exist.
func TestAPlanCanOnlyAskForWhatSetupDoes(t *testing.T) {
	for _, test := range []struct {
		name string
		plan rootPlan
	}{
		{"unknown package", rootPlan{Packages: []string{"rm"}, Manager: "apt-get"}},
		{"unknown manager", rootPlan{Packages: []string{"passt"}, Manager: "sh"}},
		{"missing user", rootPlan{KVMUser: "nobody-at-all"}},
		{"a path where a user goes", rootPlan{LingerUser: "../../etc/shadow"}},
	} {
		if err := test.plan.validate(); err == nil {
			t.Errorf("%s: the plan should be refused", test.name)
		}
	}
	current, err := user.Current()
	if err != nil {
		t.Skip("no current user to name")
	}
	if err := (rootPlan{KVMUser: current.Username, LingerUser: current.Username}).validate(); err != nil {
		t.Fatalf("a real user should be accepted: %v", err)
	}
}

// `acornfall setup --apply` is gone: the command asks for a password itself, and a flag that
// says otherwise would teach the wrong habit.
func TestSetupSaysApplyIsNotNeeded(t *testing.T) {
	if !strings.Contains(applyIsGone, "--apply") {
		t.Fatalf("the message should name the flag: %q", applyIsGone)
	}
}

// The install folder is a warning, not a sandbox problem: a workspace still runs, the update does
// not.
func TestSetupReportsAnInstallFolderItCannotUpdate(t *testing.T) {
	report := working()
	report.InstallErr = errors.New("/usr/local/bin is not writable by this user, so the update is refused before it downloads")
	text, ok := renderSetup(report)
	if !ok {
		t.Fatalf("an update problem is not a sandbox problem:\n%s", text)
	}
	if !strings.Contains(text, "is not writable") {
		t.Fatalf("the row should say it:\n%s", text)
	}
}

// Reading a machine's own distribution file is what makes the package command right rather than
// plausible.
func TestSetupReadsTheDistribution(t *testing.T) {
	path := t.TempDir()
	_ = path
	// The real answer on this machine is one of the four this command can drive.
	if _, err := os.Stat("/etc/os-release"); err == nil {
		manager := packageManager()
		if manager == "install-with-your-package-manager" {
			t.Skip("this machine's distribution is not one of the four")
		}
		if len(installArgs(manager)) == 0 {
			t.Fatalf("%q has no install arguments", manager)
		}
	}
}
