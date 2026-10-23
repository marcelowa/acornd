package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strings"
)

// rootPlan is the privileged half of `acornfall setup`, as data.
//
// It is data and not a command line on purpose. The unprivileged command works out what is
// missing and writes this; the privileged process reads it, refuses anything that is not in its
// own vocabulary, and builds the commands itself. Nothing a caller can write becomes a program
// that root runs, so the escalation is "these four machine-level steps and nothing else" rather
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
	return len(p.Packages) == 0 && !p.Userns && !p.Tun && p.KVMUser == "" && p.LingerUser == ""
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

// planAction is one step: either a command to run or a file to write, and the reason it is there
// so a person reading the transcript knows what changed.
type planAction struct {
	Why   string
	Run   []string
	File  string
	Write string
}

// actions turns the plan into steps in an order that works: the engine and its network tools
// first, then the kernel permission they need, then the device pasta builds on, then the
// per-account grants — a group added before the tool exists is a grant with nothing to grant.
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
				File:  "/etc/sysctl.d/99-acornfall-userns.conf",
				Write: "# written by acornfall setup: a sandbox needs an unprivileged user namespace\nkernel.apparmor_restrict_unprivileged_userns=0\n",
			},
			planAction{
				Why: "apply it now",
				Run: []string{"sysctl", "-w", "kernel.apparmor_restrict_unprivileged_userns=0"},
			})
	}
	if p.Tun {
		// The node and its mode are restored at boot by tmpfiles, so a machine that has them now
		// still has them after a reboot.
		actions = append(actions,
			planAction{
				Why:  "make /dev/net/tun available, which is where pasta builds its interface",
				File: "/etc/tmpfiles.d/acornfall-tun.conf",
				Write: "# written by acornfall setup: pasta needs the tun device\n" +
					"d /dev/net 0755 root root\n" +
					"c! /dev/net/tun 0666 root root 10 200\n",
			},
			planAction{Why: "load the tun module", Run: []string{"modprobe", "tun"}},
			planAction{Why: "create the tun device from the rule", Run: []string{"systemd-tmpfiles", "--create", "/etc/tmpfiles.d/acornfall-tun.conf"}},
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
			Why: "keep " + p.LingerUser + "'s acornfall running after logout",
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

// runPlanAsRoot is the privileged half. It is reached by `sudo acornfall setup --privileged
// --plan FILE`, which `acornfall setup` runs itself; a person does not type it.
func runPlanAsRoot(path string) {
	if os.Geteuid() != 0 {
		logf("acornfall setup: --privileged is how `acornfall setup` asks for a password, not a command to run by hand")
		os.Exit(2)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		logf("acornfall setup: %v", err)
		os.Exit(1)
	}
	var plan rootPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		logf("acornfall setup: the plan is not readable: %v", err)
		os.Exit(1)
	}
	if err := applyPlan(plan); err != nil {
		logf("acornfall setup: %v", err)
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
		if action.File != "" {
			if err := writeStep(action.File, []byte(action.Write), 0o644); err != nil {
				return fmt.Errorf("%s: %w", action.File, err)
			}
			continue
		}
		if err := runStep(action.Run[0], action.Run[1:]...); err != nil {
			// A missing program is not always a failure: a kernel with tun built in has no
			// module to load, and a machine without systemd has no tmpfiles to run. Those steps
			// are best-effort, and what comes after them still proves the result.
			if bestEffort[action.Run[0]] {
				fmt.Printf("  (%s is not here; continuing)\n", action.Run[0])
				continue
			}
			return fmt.Errorf("%s: %w", action.Run[0], err)
		}
	}
	return nil
}

// The two ways a plan touches the machine, as variables. A test replaces them to see what a plan
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
)

// bestEffort names the steps whose absence is a machine without that mechanism, not a failure:
// after them, the probe is the judge.
var bestEffort = map[string]bool{
	"modprobe":         true,
	"systemd-tmpfiles": true,
	"chmod":            true,
	"loginctl":         true,
}
