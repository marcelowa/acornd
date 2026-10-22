package main

import (
	"errors"
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

// A machine that can run a workspace says so, and prints no work for anybody.
func TestSetupSaysWhenNothingIsMissing(t *testing.T) {
	text, ok := renderSetup(working())
	if !ok {
		t.Fatalf("a working machine should report ok:\n%s", text)
	}
	if !strings.Contains(text, "runs workspaces on bubblewrap") {
		t.Fatalf("the summary should name the engine:\n%s", text)
	}
	if strings.Contains(text, "to change any of that") {
		t.Fatalf("nothing is missing, so there is nothing to change:\n%s", text)
	}
}

// The missing package is named, with the distribution's own package manager: a person should be
// able to copy the line rather than work out what "no pasta" means.
func TestSetupNamesTheMissingPackage(t *testing.T) {
	report := working()
	report.PastaPath = ""
	report.PastaErr = errors.New("sandbox unavailable: egress needs pasta (install it: apt install passt)")
	text, ok := renderSetup(report)
	if ok {
		t.Fatalf("a machine with no pasta cannot run a workspace:\n%s", text)
	}
	if !strings.Contains(text, "sudo apt-get install passt") {
		t.Fatalf("the fix should install pasta with this distribution's package manager:\n%s", text)
	}
	if strings.Contains(text, "sandbox unavailable: ") {
		t.Fatalf("the row should read as a sentence, not a wrapped error code:\n%s", text)
	}
}

// A machine with no bubblewrap is told to install both tools in one command.
func TestSetupAsksForBothToolsWhenNeitherIsInstalled(t *testing.T) {
	report := working()
	report.BwrapPath = ""
	report.BwrapVersion = ""
	report.PastaPath = ""
	text, _ := renderSetup(report)
	if !strings.Contains(text, "sudo apt-get install bubblewrap passt") {
		t.Fatalf("one command should install what is missing:\n%s", text)
	}
}

// A refused user namespace is a policy the person can change, and the line says how.
func TestSetupNamesTheUserNamespaceFix(t *testing.T) {
	report := working()
	report.BwrapErr = errors.New("sandbox unavailable: bubblewrap cannot create a user namespace here: Permission denied")
	text, ok := renderSetup(report)
	if ok {
		t.Fatal("a refused user namespace means no sandbox can boot")
	}
	if !strings.Contains(text, "sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0") {
		t.Fatalf("the fix should be the sysctl:\n%s", text)
	}
}

// A machine without a hypervisor still runs workspaces: the row says the microVM is what needs
// it, rather than reporting the machine as broken.
func TestSetupDoesNotCallAHypervisorRequired(t *testing.T) {
	report := working()
	report.KVMErr = errors.New("sandbox unavailable: /dev/kvm is missing")
	text, ok := renderSetup(report)
	if !ok {
		t.Fatalf("bubblewrap does not need /dev/kvm:\n%s", text)
	}
	if !strings.Contains(text, "only the microVM engine needs it") {
		t.Fatalf("the row should say what the hypervisor is for:\n%s", text)
	}
	if !strings.Contains(text, "sudo usermod -aG kvm") {
		t.Fatalf("the fix should add the user to the group:\n%s", text)
	}
}

// An install folder this user cannot write refuses the update later; saying so now is the
// difference between a sentence and a failure in the middle of an update.
func TestSetupReportsAnInstallFolderItCannotUpdate(t *testing.T) {
	report := working()
	report.InstallErr = errors.New("/usr/local/bin is not writable by this user, so the update is refused before it downloads")
	text, ok := renderSetup(report)
	if !ok {
		t.Fatalf("an update problem is not a sandbox problem:\n%s", text)
	}
	if !strings.Contains(text, "sh scripts/install.sh --dir ~/.acornfall/bin") {
		t.Fatalf("the fix should be the installer that writes a folder the user owns:\n%s", text)
	}
}

// The root steps come in an order that works: packages first, then the policy, then the per-user
// grants — a group added before the engine is installed is a grant with nothing to grant.
func TestSetupAppliesTheMachineStepsInOrder(t *testing.T) {
	report := working()
	report.BwrapPath = ""
	report.PastaPath = ""
	report.Linger = false
	report.KVMErr = errors.New("sandbox unavailable: /dev/kvm is missing")
	steps := rootSteps(report, "marko")
	if len(steps) != 3 {
		t.Fatalf("want three steps, got %v", steps)
	}
	if strings.Join(steps[0], " ") != "apt-get install -y bubblewrap passt" {
		t.Fatalf("packages come first, got %v", steps[0])
	}
	if strings.Join(steps[1], " ") != "usermod -aG kvm marko" {
		t.Fatalf("the hypervisor grant is a group, got %v", steps[1])
	}
	if strings.Join(steps[2], " ") != "loginctl enable-linger marko" {
		t.Fatalf("autostart is lingering, got %v", steps[2])
	}
}
