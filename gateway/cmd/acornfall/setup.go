package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/sandbox"
)

// setupCommand answers "can this machine run a workspace, and if not, what is the one step that
// changes that" — and, run as root, takes that step.
//
// It is not a doctor with a licence to change things. The probes are the user's, and they run as
// the user: root can always make a user namespace, so asking as root answers a different question
// and would report a machine that works when the person's does not. The only thing root does here
// is the part only root can do (ADR 0026).
func setupCommand(argv []string) {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	apply := flags.Bool("apply", false, "make the machine-level changes that need root (run as root, with sudo)")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	home, err := resolveDataDir(*dataDir)
	if err != nil {
		logf("acornfall setup: %v", err)
		os.Exit(2)
	}
	if *apply {
		applySetup(home)
		return
	}
	if os.Geteuid() == 0 {
		logf("acornfall setup: run this as the user who will use acornfall, not as root: the checks are about that user's access, and root has all of it")
		os.Exit(2)
	}
	report := gatherSetup(home)
	text, ok := renderSetup(report)
	fmt.Print(text)
	if !ok {
		os.Exit(1)
	}
}

// applySetup makes the changes that need root: the packages, the user-namespace policy, and the
// two grants that are per-user. It refuses without a user to apply them to, because "root can run
// a sandbox" is not the question.
func applySetup(home string) {
	userName := os.Getenv("SUDO_USER")
	if userName == "" {
		logf("acornfall setup --apply: run it with sudo from your own account (`sudo acornfall setup --apply`), so the grants land on the user who runs workspaces")
		os.Exit(2)
	}
	report := gatherSetup(home)
	steps := rootSteps(report, userName)
	if len(steps) == 0 {
		fmt.Printf("nothing to change: %s can already run a workspace\n", userName)
		return
	}
	for _, step := range steps {
		fmt.Printf("$ %s\n", step)
		command := exec.Command(step[0], step[1:]...)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			logf("acornfall setup: %s: %v", step[0], err)
			os.Exit(1)
		}
	}
	fmt.Printf("\nnow run `acornfall setup` as %s to confirm, and restart the gateway so it sees the change\n", userName)
}

// setupReport is what the probes found. It is a value so that rendering and the root steps can be
// tested without a machine that has or lacks these things.
type setupReport struct {
	PackageManager string
	BwrapPath      string
	BwrapVersion   string
	BwrapErr       error
	PastaPath      string
	PastaErr       error
	KVMErr         error
	// LingerKnown is false when this machine has no systemd for the user: not a problem, just a
	// question that does not apply.
	LingerKnown bool
	Linger      bool
	InstallDir  string
	InstallErr  error
}

// gatherSetup asks the drivers, which is the same code a start would run: a check here that
// passes while a start fails would be worse than no check.
func gatherSetup(home string) setupReport {
	bwrap := sandbox.NewBwrap(home)
	hypervisor := sandbox.NewMicrosandbox(home)
	report := setupReport{
		PackageManager: packageManager(),
		BwrapPath:      bwrap.InstallDir(),
		BwrapVersion:   bwrap.Version(),
		BwrapErr:       bwrap.Usable(),
		PastaErr:       bwrap.Egress(),
		KVMErr:         hypervisor.Usable(),
	}
	if path, err := exec.LookPath("pasta"); err == nil {
		report.PastaPath = path
	}
	report.LingerKnown, report.Linger = lingering()
	report.InstallDir, report.InstallErr = installFolder()
	return report
}

// renderSetup is the whole answer as a person reads it: what works, what does not, and the
// command that changes it. ok is whether a workspace can run at all.
func renderSetup(report setupReport) (string, bool) {
	var out strings.Builder
	out.WriteString("sandbox engines on this machine\n\n")
	ok := true

	bwrap := report.BwrapPath != ""
	fmt.Fprintf(&out, "  %s  bubblewrap        %s\n", mark(bwrap), describePath(report.BwrapPath, report.BwrapVersion))
	if bwrap {
		usable := report.BwrapErr == nil
		fmt.Fprintf(&out, "  %s  user namespaces   %s\n", mark(usable), describe(usable, "a sandbox can boot here", report.BwrapErr))
		pasta := report.PastaErr == nil
		fmt.Fprintf(&out, "  %s  sandbox network   %s\n", mark(pasta), describe(pasta, describePath(report.PastaPath, ""), report.PastaErr))
		if !usable || !pasta {
			ok = false
		}
	} else {
		ok = false
	}

	// The hypervisor is the other engine, not this one: a machine without /dev/kvm still runs
	// workspaces, on bubblewrap.
	kvm := report.KVMErr == nil
	if kvm {
		fmt.Fprintf(&out, "  %s  hypervisor        /dev/kvm is readable\n", mark(true))
	} else {
		fmt.Fprintf(&out, "  -  hypervisor        not available: only the microVM engine needs it\n")
	}

	if report.LingerKnown {
		fmt.Fprintf(&out, "  %s  autostart         %s\n", mark(report.Linger), describe(report.Linger, "this user's services survive logout", errors.New("this user's services stop when the session ends")))
	}
	updatable := report.InstallErr == nil
	fmt.Fprintf(&out, "  %s  install folder    %s\n", mark(updatable), describe(updatable, report.InstallDir+" is writable, so self-update works", report.InstallErr))

	out.WriteString("\n")
	if ok {
		out.WriteString("this machine runs workspaces on bubblewrap")
		if !kvm {
			out.WriteString("; the microVM engine needs the step below")
		}
		out.WriteString(".\n")
	}
	if fixes := printedFixes(report, currentNodeUser()); len(fixes) > 0 {
		out.WriteString("\nto change any of that, run\n")
		for _, fix := range fixes {
			fmt.Fprintf(&out, "  %s\n", fix)
		}
	}
	return out.String(), ok
}

// printedFixes are the commands a person runs, with the privilege they need: the machine-level
// ones through sudo, the user-level ones as the user.
func printedFixes(report setupReport, userName string) []string {
	var fixes []string
	if report.BwrapPath == "" || report.PastaErr != nil && report.PastaPath == "" {
		fixes = append(fixes, fmt.Sprintf("sudo %s install %s", report.PackageManager, packagesFor(report)))
	}
	if report.BwrapPath != "" && report.BwrapErr != nil {
		fixes = append(fixes, "sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0")
	}
	if report.KVMErr != nil {
		fixes = append(fixes, fmt.Sprintf("sudo usermod -aG kvm %s   # then log out and back in", userName))
	}
	if report.LingerKnown && !report.Linger {
		fixes = append(fixes, fmt.Sprintf("sudo loginctl enable-linger %s", userName))
	}
	if report.InstallErr != nil {
		fixes = append(fixes, "reinstall into a folder you own: sh scripts/install.sh --dir ~/.acornfall/bin")
	}
	return fixes
}

// packagesFor names the packages this machine is missing, so the command installs what is needed
// and nothing else.
func packagesFor(report setupReport) string {
	var names []string
	if report.BwrapPath == "" {
		names = append(names, "bubblewrap")
	}
	if report.PastaPath == "" {
		names = append(names, "passt")
	}
	if len(names) == 0 {
		names = append(names, "bubblewrap", "passt")
	}
	return strings.Join(names, " ")
}

// rootSteps is the same list as commands to run, for `--apply`: the machine-level ones plus the
// per-user grants, in the order that makes each one meaningful.
func rootSteps(report setupReport, userName string) [][]string {
	var steps [][]string
	if report.BwrapPath == "" || (report.PastaErr != nil && report.PastaPath == "") {
		step := append([]string{report.PackageManager}, installArgs(report.PackageManager)...)
		step = append(step, strings.Fields(packagesFor(report))...)
		steps = append(steps, step)
	}
	if report.BwrapPath != "" && report.BwrapErr != nil {
		steps = append(steps, []string{"sysctl", "-w", "kernel.apparmor_restrict_unprivileged_userns=0"})
	}
	if report.KVMErr != nil {
		steps = append(steps, []string{"usermod", "-aG", "kvm", userName})
	}
	if report.LingerKnown && !report.Linger {
		steps = append(steps, []string{"loginctl", "enable-linger", userName})
	}
	return steps
}

// packageManager is the command that installs a package here, from the distribution's own file.
// An unknown distribution gets no guess: the printer says so rather than inventing a command.
func packageManager() string {
	file, err := os.Open("/etc/os-release")
	if err == nil {
		defer func() { _ = file.Close() }()
		values := map[string]string{}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			key, value, found := strings.Cut(scanner.Text(), "=")
			if found {
				values[key] = strings.Trim(value, `"`)
			}
		}
		id := values["ID"]
		like := values["ID_LIKE"]
		for _, candidate := range []string{id, like} {
			switch {
			case strings.Contains(candidate, "debian"), strings.Contains(candidate, "ubuntu"):
				return "apt-get"
			case strings.Contains(candidate, "fedora"), strings.Contains(candidate, "rhel"), strings.Contains(candidate, "centos"):
				return "dnf"
			case strings.Contains(candidate, "arch"):
				return "pacman"
			case strings.Contains(candidate, "alpine"):
				return "apk"
			}
		}
	}
	for _, manager := range []string{"apt-get", "dnf", "pacman", "apk"} {
		if _, err := exec.LookPath(manager); err == nil {
			return manager
		}
	}
	return "install-with-your-package-manager"
}

// installArgs is how one package manager says "install these, do not ask".
func installArgs(manager string) []string {
	switch manager {
	case "apt-get":
		return []string{"install", "-y"}
	case "dnf":
		return []string{"install", "-y"}
	case "pacman":
		return []string{"-S", "--noconfirm"}
	case "apk":
		return []string{"add"}
	default:
		return []string{"install"}
	}
}

// lingering reports whether this user's services outlive a logout. A machine without systemd for
// the user answers "not applicable" rather than "no".
func lingering() (known, on bool) {
	current, err := user.Current()
	if err != nil {
		return false, false
	}
	if _, err := exec.LookPath("loginctl"); err != nil {
		return false, false
	}
	out, err := exec.Command("loginctl", "show-user", current.Username, "--property=Linger").Output()
	if err != nil {
		return false, false
	}
	return true, strings.Contains(string(out), "Linger=yes")
}

// installFolder reports whether this binary can be replaced: an update writes next to it, so a
// root-owned folder makes self-update a refusal rather than a surprise at swap time.
func installFolder() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(executable)
	probe, err := os.CreateTemp(dir, ".acornfall-setup-*")
	if err != nil {
		return dir, fmt.Errorf("%s is not writable by this user, so the update is refused before it downloads", dir)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return dir, nil
}

// currentNodeUser is who to name in a command that takes a user: the one running this.
func currentNodeUser() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		return sudoUser
	}
	current, err := user.Current()
	if err != nil {
		return "you"
	}
	return current.Username
}

// mark is the two-character column that makes a list scannable: yes, no, or not applicable.
func mark(ok bool) string {
	if ok {
		return "ok"
	}
	return "no"
}

func describe(ok bool, good string, problem error) string {
	if ok {
		return good
	}
	if problem == nil {
		return "missing"
	}
	// The driver's refusal begins with its own classification ("sandbox unavailable: "), which is
	// noise in a row that already says "no".
	return strings.TrimPrefix(problem.Error(), "sandbox unavailable: ")
}

func describePath(path, version string) string {
	if path == "" {
		return "not installed"
	}
	if version == "" {
		return path
	}
	return path + " (" + version + ")"
}

// logf is the one place this command writes to stderr, so a test can read it without a log
// package's timestamps in the middle of a sentence.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
