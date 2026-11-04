package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
)

// rootPlan is the privileged half of `acornd setup`, as data.
//
// It is data and not a command line on purpose. The unprivileged command works out what is
// missing and writes this; the privileged process reads it, refuses anything that is not in its
// own vocabulary, and builds the commands itself. Nothing a caller can write becomes a program
// that root runs, so the escalation is "these five machine-level steps and nothing else" rather
// than "whatever the file said".
type rootPlan struct {
	// Packages are distribution package names, from a fixed list.
	Packages []string `json:"packages,omitempty"`
	// Manager names the package manager the way a person would: apt-get, dnf, pacman, apk.
	Manager string `json:"manager,omitempty"`
	// Userns relaxes the kernel's unprivileged-user-namespace policy, and makes it survive a
	// reboot. Only set when the probe failed and this is not a container.
	Userns bool `json:"userns,omitempty"`
	// Tun makes /dev/net/tun available, which is what pasta builds its interface from.
	Tun bool `json:"tun,omitempty"`
	// PastaProfile turns off the distribution's AppArmor profile for pasta, the one Ubuntu 26.04's
	// passt package ships: it lets pasta start a program only from /usr/bin or /bin, and the
	// sandbox's first process is acornd's supervisor, which is in neither. Only set when the probe
	// refused and this machine has the profile, or when this plan installs the package that brings it.
	PastaProfile bool `json:"pastaProfile,omitempty"`
	// KVMUser and LingerUser are grants to one account. Empty means "not needed".
	KVMUser    string `json:"kvmUser,omitempty"`
	LingerUser string `json:"lingerUser,omitempty"`
}

// allowedPackages is the whole vocabulary of things this plan may install. A package name is not
// a user's choice: it is one of three engines' dependencies, and the list is short enough to say
// out loud.
var allowedPackages = []string{"bubblewrap", "passt"}

// allowedManagers are the package managers installArgs knows how to drive. An unknown name is
// refused rather than guessed at.
var allowedManagers = []string{"apt-get", "dnf", "pacman", "apk"}

func (p rootPlan) empty() bool {
	return len(p.Packages) == 0 && !p.Userns && !p.Tun && !p.PastaProfile && p.KVMUser == "" && p.LingerUser == ""
}

// validate refuses anything this plan is not allowed to ask for. It is the only thing standing
// between a file in a temporary folder and root's shell, so it checks every field.
func (p rootPlan) validate() error {
	if len(p.Packages) > 0 {
		if !slices.Contains(allowedManagers, p.Manager) {
			return fmt.Errorf("no package manager %q", p.Manager)
		}
		for _, name := range p.Packages {
			if !slices.Contains(allowedPackages, name) {
				return fmt.Errorf("no such package in this plan: %q", name)
			}
		}
	}
	for _, name := range []string{p.KVMUser, p.LingerUser} {
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, " \t\n/\\") {
			return fmt.Errorf("not a user name: %q", name)
		}
		// A grant to an account that does not exist is a typo, not a request.
		if !userExists(name) {
			return fmt.Errorf("no such user: %q", name)
		}
	}
	return nil
}

// packageReason says what the packages are for, without overclaiming when only one is missing.
func packageReason(packages []string) string {
	bubblewrap := slices.Contains(packages, "bubblewrap")
	pasta := slices.Contains(packages, "passt")
	switch {
	case bubblewrap && pasta:
		return ", the sandbox engine and the network it carries"
	case bubblewrap:
		return ", the sandbox engine"
	default:
		return ", which carries a workspace's network"
	}
}

// planAction is one step: a command to run, a file to write, or the distribution's profile for
// pasta turned off, and the reason it is there so a person reading the transcript knows what
// changed.
type planAction struct {
	Why   string
	Run   []string
	File  string
	Write string
	// PastaProfile is the one step that is not an argv: whether it applies, and which half of it
	// does, is a question about this machine that is answered when it runs — the profile may not be
	// here, and a machine that never loaded it needs no parser run.
	PastaProfile bool
}

// actions turns the plan into steps in an order that works: the engine and its network tools
// first, then the kernel permission the sandbox needs, then the distribution's profile for pasta —
// which has to come after it, because turning the profile off makes pasta unconfined and an
// unconfined process cannot make a user namespace on a machine that still restricts them — then the
// device pasta builds on, then the per-account grants: a group added before the tool exists is a
// grant with nothing to grant.
func (p rootPlan) actions() []planAction {
	var actions []planAction
	if len(p.Packages) > 0 {
		run := append([]string{p.Manager}, installArgs(p.Manager)...)
		run = append(run, p.Packages...)
		actions = append(actions, planAction{
			Why: "install " + strings.Join(p.Packages, " ") + packageReason(p.Packages),
			Run: run,
		})
	}
	if p.Userns {
		// A sysctl without the file is a fix that disappears at the next reboot, which is the
		// worst moment to discover it.
		actions = append(actions,
			planAction{
				Why:   "allow unprivileged user namespaces, and keep that across reboots",
				File:  "/etc/sysctl.d/99-acornd-userns.conf",
				Write: "# written by acornd setup: a sandbox needs an unprivileged user namespace\nkernel.apparmor_restrict_unprivileged_userns=0\n",
			},
			planAction{
				Why: "apply it now",
				Run: []string{"sysctl", "-w", "kernel.apparmor_restrict_unprivileged_userns=0"},
			})
	}
	if p.PastaProfile {
		// After the packages and after the kernel permission: the profile is the passt package's,
		// and pasta cannot make the namespace it carries without the permission above.
		actions = append(actions, planAction{
			Why:          "turn off the AppArmor profile for pasta, which lets it start a program only from /usr/bin or /bin — acornd's supervisor is in neither",
			PastaProfile: true,
		})
	}
	if p.Tun {
		// The node and its mode are restored at boot by tmpfiles, so a machine that has them now
		// still has them after a reboot.
		actions = append(actions,
			planAction{
				Why:  "make /dev/net/tun available, which is where pasta builds its interface",
				File: "/etc/tmpfiles.d/acornd-tun.conf",
				Write: "# written by acornd setup: pasta needs the tun device\n" +
					"d /dev/net 0755 root root\n" +
					"c! /dev/net/tun 0666 root root 10 200\n",
			},
			planAction{Why: "load the tun module", Run: []string{"modprobe", "tun"}},
			planAction{Why: "create the tun device from the rule", Run: []string{"systemd-tmpfiles", "--create", "/etc/tmpfiles.d/acornd-tun.conf"}},
			planAction{Why: "make sure the device is usable without root", Run: []string{"chmod", "0666", "/dev/net/tun"}},
		)
	}
	if p.KVMUser != "" {
		actions = append(actions, planAction{
			Why: "let " + p.KVMUser + " open /dev/kvm, which the microVM engine runs in",
			Run: []string{"usermod", "-aG", "kvm", p.KVMUser},
		})
	}
	if p.LingerUser != "" {
		actions = append(actions, planAction{
			Why: "keep " + p.LingerUser + "'s acornd running after logout",
			Run: []string{"loginctl", "enable-linger", p.LingerUser},
		})
	}
	return actions
}

// description is what the unprivileged side tells the person before sudo asks for a password:
// the steps, and why each one is needed.
func (p rootPlan) description() []string {
	var lines []string
	for _, action := range p.actions() {
		lines = append(lines, action.Why)
	}
	return lines
}

// userExists asks the account database, which is also the only place a grant would land.
func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

// runPlanAsRoot is the privileged half. It is reached by `sudo acornd setup --privileged
// --plan FILE`, which `acornd setup` runs itself; a person does not type it.
func runPlanAsRoot(path string) {
	if os.Geteuid() != 0 {
		logf("acornd setup: --privileged is how `acornd setup` asks for a password, not a command to run by hand")
		os.Exit(2)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		logf("acornd setup: %v", err)
		os.Exit(1)
	}
	var plan rootPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		logf("acornd setup: the plan is not readable: %v", err)
		os.Exit(1)
	}
	if err := applyPlan(plan); err != nil {
		logf("acornd setup: %v", err)
		os.Exit(1)
	}
}

// applyPlan runs a plan's steps. The validation is here as well as in the privileged half because
// root mode reaches this without a file in between, and a rule with two doors is a rule with one
// door.
func applyPlan(plan rootPlan) error {
	if err := plan.validate(); err != nil {
		return fmt.Errorf("the plan asks for something this command does not do: %w", err)
	}
	actions := plan.actions()
	if len(actions) == 0 {
		fmt.Println("nothing to change")
		return nil
	}
	for _, action := range actions {
		fmt.Printf("· %s\n", action.Why)
		if err := runAction(action); err != nil {
			return err
		}
	}
	return nil
}

// runAction is one step. The three shapes a plan has are each run the way they have to be, and a
// step that can only be a machine without that mechanism is not a failure: the probe afterwards is
// the judge.
func runAction(action planAction) error {
	if action.PastaProfile {
		if err := disablePastaProfile(); err != nil {
			return fmt.Errorf("the pasta profile: %w", err)
		}
		return nil
	}
	if action.File != "" {
		if err := writeStep(action.File, []byte(action.Write), 0o644); err != nil {
			return fmt.Errorf("%s: %w", action.File, err)
		}
		return nil
	}
	if err := runStep(action.Run[0], action.Run[1:]...); err != nil {
		// A missing program is not always a failure: a kernel with tun built in has no module to
		// load, and a machine without systemd has no tmpfiles to run.
		if bestEffort[action.Run[0]] {
			fmt.Printf("  (%s is not here; continuing)\n", action.Run[0])
			return nil
		}
		return fmt.Errorf("%s: %w", action.Run[0], err)
	}
	return nil
}

// The three ways a plan touches the machine, as variables. A test replaces them to see what a plan
// would do: the argv and the file contents are the decision worth checking, and running apt-get
// for real proves less while changing the machine it ran on.
var (
	runStep = func(name string, args ...string) error {
		command := exec.Command(name, args...)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	}
	writeStep = func(path string, content []byte, mode os.FileMode) error {
		return os.WriteFile(path, content, mode)
	}
	linkStep = func(target, link string) error { return os.Symlink(target, link) }
)

// pastaAppArmor is where the profile Ubuntu 26.04 ships for pasta is, where a disabled profile is
// recorded, and the kernel's list of profiles in force. It is a value a test replaces, because
// /etc/apparmor.d is not the test's machine.
var pastaAppArmor = struct {
	profile string
	disable string
	loaded  string
}{
	profile: "/etc/apparmor.d/usr.bin.pasta",
	disable: "/etc/apparmor.d/disable",
	loaded:  "/sys/kernel/security/apparmor/profiles",
}

// disablePastaProfile turns off the distribution's profile for pasta the way Ubuntu's own aa-disable
// does: a symlink in /etc/apparmor.d/disable, which is what a boot reads, then the parser, which
// takes away the profile loaded now. The profile is a real security boundary — pasta may start a
// program from /usr/bin or /bin and nothing else — and it is turned off because the first process
// pasta starts is acornd's supervisor, in the install folder, so every workspace with its network
// on refuses to start with "Failed to start command or shell: Permission denied". ADR 0023 has the
// measurements.
//
// Nothing here is a failure on a machine the profile is not about. A distribution that ships none
// (Ubuntu 24.04, Debian, Fedora) and a machine that never loaded it skip the half that does not
// apply, and the probe afterwards is the judge.
func disablePastaProfile() error {
	if _, err := os.Stat(pastaAppArmor.profile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("  (%s is not here: this distribution ships no pasta profile)\n", pastaAppArmor.profile)
			return nil
		}
		return err
	}
	link := filepath.Join(pastaAppArmor.disable, filepath.Base(pastaAppArmor.profile))
	if err := os.MkdirAll(pastaAppArmor.disable, 0o755); err != nil {
		return err
	}
	if err := linkStep(pastaAppArmor.profile, link); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s: %w", link, err)
	}
	fmt.Println("  a distribution security profile, off because no sandbox with a network can start with it")
	fmt.Printf("  %s -> %s\n", link, pastaAppArmor.profile)
	if !profileLoaded() {
		fmt.Printf("  (pasta is not a loaded profile here; the symlink keeps it that way)\n")
		return nil
	}
	return runStep("apparmor_parser", "-R", pastaAppArmor.profile)
}

// profileLoaded asks the kernel whether the distribution's profile for pasta is in force. It looks
// for the profile's *name*, which is neither the file nor always the path it attaches to: Ubuntu
// loads this one as `pasta`, a profile declared `profile <path>` is listed by its path, and a
// package may name it after its own file (`usr.bin.pasta`). A name is split the way names use
// separators, so all three answer yes and `passt` or `pasta-helper` do not.
//
// The list is absent when AppArmor is not enabled at all, which is the same answer for this
// question: there is nothing to unload, and a parser run there would be a failure about a mechanism
// this machine does not have.
func profileLoaded() bool {
	payload, err := os.ReadFile(pastaAppArmor.loaded)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(payload), "\n") {
		// A line is "<name> (<mode>)", so the name is what comes before the first space.
		name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		for _, word := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '.' }) {
			if word == "pasta" {
				return true
			}
		}
	}
	return false
}

// bestEffort names the steps whose absence is a machine without that mechanism, not a failure:
// after them, the probe is the judge.
var bestEffort = map[string]bool{
	"modprobe":         true,
	"systemd-tmpfiles": true,
	"chmod":            true,
	"loginctl":         true,
}
