package main

import (
	"errors"
	"os"
	"os/user"
	"slices"
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
		InstallDir:     "/home/me/.acornd/bin",
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
	if len(steps) == 0 || strings.Join(steps[0].Run, " ") != "apt-get install -y passt" {
		t.Fatalf("want `apt-get install -y passt`, got %+v", steps)
	}
	// The package may bring Ubuntu 26.04's profile for pasta, so the step that turns it off goes
	// with it; whether the package did is a question the step answers when it runs.
	if len(steps) != 2 || !steps[1].PastaProfile {
		t.Fatalf("the install and the profile it may bring are one plan, got %+v", steps)
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
		if action.File == "/etc/sysctl.d/99-acornd-userns.conf" && strings.Contains(action.Write, "apparmor_restrict_unprivileged_userns=0") {
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

// The pasta profile is a host grant like the sysctl: planned only where the probe refused and the
// distribution ships the profile. Ubuntu 24.04, Debian, and Fedora must see no step at all.
func TestSetupPlansThePastaProfileOnlyWhereItIsInTheWay(t *testing.T) {
	report := working()
	report.PastaProfile = true
	report.PastaErr = errors.New("sandbox unavailable: pasta cannot start the supervisor it is given: Failed to start command or shell: Permission denied")
	if plan := planFor(report, "me"); !plan.PastaProfile {
		t.Fatalf("a refused network on a machine with the profile should be the step: %+v", plan)
	}
	// A probe that passed is the machine saying the profile is not in the way: acornd may be under
	// /usr/bin, or AppArmor may not be enforcing it.
	report.PastaErr = nil
	if plan := planFor(report, "me"); plan.PastaProfile {
		t.Fatalf("a working network should not be given a security change: %+v", plan)
	}
	// A machine without the profile is every release before 26.04.
	report.PastaErr = errors.New("sandbox unavailable: pasta cannot make a network namespace here")
	report.PastaProfile = false
	if plan := planFor(report, "me"); plan.PastaProfile {
		t.Fatalf("no profile means no step: %+v", plan)
	}
}

// A fresh 26.04 install has no passt yet, so the plan cannot see the profile the package will
// bring: the step goes in with the install, and answers when it runs.
func TestSetupPlansThePastaProfileWithThePackageThatBringsIt(t *testing.T) {
	report := working()
	report.PastaPath = ""
	report.PastaErr = errors.New("sandbox unavailable: a workspace with its network on needs pasta to carry it")
	plan := planFor(report, "me")
	if !slices.Contains(plan.Packages, "passt") {
		t.Fatalf("the package should be planned: %+v", plan)
	}
	if !plan.PastaProfile {
		t.Fatalf("the profile may come with that package, so the step goes with it: %+v", plan)
	}
}

// The table says the profile is why the network does not start, because pasta's own sentence does
// not name the cause.
func TestSetupShowsThePastaProfileItCannotUse(t *testing.T) {
	report := working()
	report.PastaProfile = true
	report.PastaErr = errors.New("sandbox unavailable: pasta cannot start the supervisor it is given: Failed to start command or shell: Permission denied")
	text, ok := renderSetup(report)
	if ok {
		t.Fatalf("a machine whose network cannot start is not ready:\n%s", text)
	}
	if !strings.Contains(text, "pasta profile") {
		t.Fatalf("the row should name the profile:\n%s", text)
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
		if action.File == "/etc/tmpfiles.d/acornd-tun.conf" && strings.Contains(action.Write, "c! /dev/net/tun 0666") {
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

// Who the checks are about is a decision with a wrong answer that looks like a right one: on a
// machine where root installed acornd, root can open /dev/kvm and make a user namespace
// whatever the policy says, so probing as root reports a machine that works while the service's
// workspaces cannot boot.
func TestSetupChecksTheAccountThatRunsAcornd(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	for _, test := range []struct {
		name      string
		requested string
		root      bool
		dataDir   string
		want      string
	}{
		{name: "an ordinary user is the account", root: false, dataDir: t.TempDir(), want: current.Username},
		{name: "--for wins", requested: current.Username, root: true, dataDir: t.TempDir(), want: current.Username},
	} {
		account, err := accountFor(test.requested, test.root, test.dataDir)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if account.Name != test.want {
			t.Errorf("%s: got %q, want %q", test.name, account.Name, test.want)
		}
	}
	// An account that does not exist is a typo to refuse, not a name to grant to.
	if _, err := accountFor("no-such-account-at-all", true, t.TempDir()); err == nil {
		t.Fatal("an unknown --for should be refused")
	}
}

// Root without sudo checks the folder's owner, and says where the name came from so a wrong guess
// is visible in the output rather than discovered later. The owner is a number this machine has no
// account for, which is the case that used to fall through to "nothing says otherwise" — on the
// very machine whose folder is the evidence about who runs it.
func TestSetupTakesTheAccountFromTheInstallFolder(t *testing.T) {
	if os.Geteuid() != 0 {
		// Making a folder belong to another account is the privilege; the decision itself is
		// covered without it by TestAFolderOwnedByANumberWithNoAccountNamesTheNumber.
		t.Skip("only root can make a folder belong to another account")
	}
	dataDir := t.TempDir()
	missing := absentAccountID(t)
	if err := os.Chown(dataDir, int(missing), int(missing)); err != nil {
		t.Fatalf("cannot change the folder's owner here: %v", err)
	}
	// Neither sudo nor a service unit says anything: the folder is the only evidence there is.
	t.Setenv("SUDO_USER", "")
	saved := serviceUser
	serviceUser = func() string { return "" }
	t.Cleanup(func() { serviceUser = saved })

	account, err := accountFor("", true, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if account.Name != unknownAccount {
		t.Errorf("name = %q, want %q for a number with no account", account.Name, unknownAccount)
	}
	if account.UID != missing || account.GID != missing {
		t.Errorf("numbers = %d:%d, want the folder's %d:%d", account.UID, account.GID, missing, missing)
	}
	if want := "owns " + dataDir; account.Basis != want {
		t.Errorf("basis = %q, want %q", account.Basis, want)
	}
}

// The checks cross a process boundary as data, and every problem has to come back as the same
// sentence it was: an empty string is "fine", and the words are the whole value.
func TestASetupReportSurvivesTheProcessBoundary(t *testing.T) {
	before := working()
	before.BwrapErr = errors.New("sandbox unavailable: bubblewrap cannot create a user namespace here")
	before.KVMErr = errors.New("sandbox unavailable: /dev/kvm is not readable by me")
	before.Linger = false
	before.PastaProfile = true
	after := reportOf(wireOf(before))
	if after.BwrapErr == nil || after.BwrapErr.Error() != before.BwrapErr.Error() {
		t.Fatalf("the user-namespace problem came back as %v", after.BwrapErr)
	}
	if after.KVMErr == nil || after.KVMErr.Error() != before.KVMErr.Error() {
		t.Fatalf("the hypervisor problem came back as %v", after.KVMErr)
	}
	if after.PastaErr != nil || after.TunErr != nil {
		t.Fatalf("nothing was wrong with pasta or tun, but they came back as %v / %v", after.PastaErr, after.TunErr)
	}
	if !after.PastaProfile {
		t.Fatal("the pasta profile did not survive the process boundary")
	}
	if after.BwrapPath != before.BwrapPath || after.LingerKnown != before.LingerKnown {
		t.Fatalf("the rest of the report changed: %+v", after)
	}
}

// The confirmation is a deliberate word, not a keystroke typed on the way to something else.
func TestSetupAppliesOnlyOnAYes(t *testing.T) {
	for input, want := range map[string]bool{
		"y\n": true, "yes\n": true, "Y\n": true,
		"\n": false, "n\n": false, "no\n": false, "yeah\n": false,
	} {
		reader := strings.NewReader(input)
		original := stdinForTest
		stdinForTest = reader
		got := confirm("? ")
		stdinForTest = original
		if got != want {
			t.Errorf("%q: got %v, want %v", input, got, want)
		}
	}
}
