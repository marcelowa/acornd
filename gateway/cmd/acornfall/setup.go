package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/sandbox"
)

// setupCommand answers "can this machine run a workspace, and if not, what does it need" — and
// then does it, in whichever of three ways this machine allows (ADR 0026):
//
//	already root        the plan is shown and applied on a yes, with no password to type
//	an ordinary user     the plan is shown, then sudo asks for the password
//	neither             the plan is printed as commands to run
//
// One command is the whole interface: `acornfall setup`. The privileged half is the same binary
// with `--privileged --plan FILE`, which this command runs itself and nobody types.
func setupCommand(argv []string) {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder")
	forUser := flags.String("for", "", "the account that runs acornfall (default: the owner of the acornfall folder)")
	probe := flags.Bool("probe", false, "internal: print what this account can do, as JSON (setup runs this as the account)")
	privileged := flags.Bool("privileged", false, "internal: run a plan as root (setup runs this through sudo)")
	planPath := flags.String("plan", "", "internal: the plan to run with --privileged")
	assumeYes := flags.Bool("yes", false, "apply the steps without asking first (for a script that already decided)")
	// The three flags above are the interface. The rest of this command's flags are how setup
	// calls itself, and printing them would offer a person switches that exist to avoid them.
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: acornfall setup [--data-dir DIR] [--for USER] [--yes]

  --data-dir DIR  the acornfall folder (default: ~/.acornfall)
  --for USER      the account that runs acornfall (default: the owner of that folder)
  --yes           apply the steps without asking first

It checks this machine, explains each step, and asks for your password only for the steps that
need root. Run with nothing to see what this machine needs.
`)
	}
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *privileged {
		runPlanAsRoot(*planPath)
		return
	}
	folder, err := resolveDataDir(*dataDir)
	if err != nil {
		logf("acornfall setup: %v", err)
		os.Exit(2)
	}
	if *probe {
		// The hidden half of "probe as the account that runs acornfall": root re-executes this
		// binary with that account's credentials, and all it does is answer with the checks.
		writeProbe(folder)
		return
	}
	root := os.Geteuid() == 0
	account, err := accountFor(*forUser, root, folder)
	if err != nil {
		logf("acornfall setup: %v", err)
		os.Exit(2)
	}
	who := account.Name
	if root {
		fmt.Printf("checking for the account that runs acornfall: %s (%s)\n\n", who, account.Basis)
	}
	report, err := probeAs(account, folder)
	if err != nil {
		logf("acornfall setup: %v", err)
		os.Exit(2)
	}
	text, ok := renderSetup(report)
	fmt.Print(text)
	plan := planFor(report, who)
	if plan.empty() {
		if !ok {
			os.Exit(1)
		}
		return
	}
	if why := containerAdvice(report); why != "" {
		fmt.Print("\n" + why + "\n")
	}
	fmt.Printf("\n%s root, for %s:\n", stepCount(countSteps(plan)), who)
	for _, line := range plan.description() {
		fmt.Printf("  · %s\n", line)
	}
	switch {
	case root:
		// No password exists to ask for, so the plan is what gets approved.
		if !*assumeYes {
			agreed, err := confirm("apply these steps? [y/N] ")
			if err != nil {
				logf("acornfall setup: %v", err)
				os.Exit(1)
			}
			if !agreed {
				fmt.Println("\nnothing was changed. the same steps are:")
				for _, line := range printedFixes(report, who) {
					fmt.Printf("  %s\n", line)
				}
				os.Exit(1)
			}
		}
		if err := applyPlan(plan); err != nil {
			logf("acornfall setup: %v", err)
			os.Exit(1)
		}
	default:
		if err := escalate(plan); err != nil {
			if errors.Is(err, errNoEscalation) {
				fmt.Println("\nthis machine cannot ask for a password here (no sudo, or no terminal to type one into). run:")
				for _, line := range printedFixes(report, who) {
					fmt.Printf("  %s\n", line)
				}
				os.Exit(1)
			}
			logf("acornfall setup: %v", err)
			os.Exit(1)
		}
	}
	// Say what changed, from the same probes that said what was missing, asked of the same
	// account. A group added to an account is not in any running process's groups, and no command
	// can add it there: only a new session can.
	fmt.Println()
	after, err := probeAs(account, folder)
	if err != nil {
		logf("acornfall setup: %v", err)
		os.Exit(1)
	}
	final, ok := renderSetup(after)
	fmt.Print(final)
	if plan.KVMUser != "" {
		fmt.Printf("\n%s must log out and back in for the kvm group to apply, and the gateway must be\nrestarted so it starts with that group\n", who)
	}
	if !ok {
		fmt.Printf("\nstill not ready: this machine needs something root cannot give it\n")
		os.Exit(1)
	}
}

// stdinForTest is the terminal a person types a confirmation into; a test answers instead.
var stdinForTest io.Reader = os.Stdin

// confirm reads one answer. Anything but yes is no: a command that changes a machine should need
// a deliberate word, not a keystroke somebody typed on the way to something else.
func confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(stdinForTest)
	answer, err := reader.ReadString('\n')
	if err != nil && answer == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// stepCount reads as a sentence for one step and for several: "1 step needs root" is the kind of
// sentence that makes a careful reader trust the rest less.
func stepCount(count int) string {
	if count == 1 {
		return "1 step needs"
	}
	return fmt.Sprintf("%d steps need", count)
}

func countSteps(plan rootPlan) int { return len(plan.actions()) }

// planFor is what the probes imply: which machine-level steps are missing, and for whom. It is
// the only place that decides, so the description a person reads and the commands root runs
// cannot disagree.
func planFor(report setupReport, who string) rootPlan {
	var plan rootPlan
	if report.BwrapPath == "" {
		plan.Packages = append(plan.Packages, "bubblewrap")
	}
	if report.PastaPath == "" {
		plan.Packages = append(plan.Packages, "passt")
	}
	if len(plan.Packages) > 0 && slices.Contains(allowedManagers, report.PackageManager) {
		plan.Manager = report.PackageManager
	} else {
		// No package manager this command knows: the plan cannot carry it, and the printed list
		// says "install bubblewrap passt with your package manager" instead.
		plan.Packages = nil
	}
	// The user-namespace policy is a machine-wide relaxation, so it is only proposed when the
	// probe actually failed and the machine has the AppArmor restriction that causes it. Inside a
	// container the sysctl is not the answer: the container's own profile is.
	if report.BwrapPath != "" && report.BwrapErr != nil && report.UsernsRestricted && !report.Container {
		plan.Userns = true
	}
	// pasta builds its interface through the tun device; a machine whose node is missing or not
	// usable needs it present and openable, not a changed policy.
	if report.TunErr != nil && !report.Container {
		plan.Tun = true
	}
	if report.KVMErr != nil {
		plan.KVMUser = who
	}
	if report.LingerKnown && !report.Linger {
		plan.LingerUser = who
	}
	return plan
}

// containerAdvice explains what no command inside can fix. Empty when this is not a container, or
// when the container is not the reason anything is missing.
func containerAdvice(report setupReport) string {
	if !report.Container || report.BwrapErr == nil && report.TunErr == nil {
		return ""
	}
	return "this looks like a container: a sandbox needs the container started with\n" +
		"  --security-opt seccomp=unconfined --device /dev/net/tun\n" +
		"which is a decision for whoever runs the container, not something acornfall can change from inside."
}

// errNoEscalation is the machine where a password cannot be asked for: no sudo, or no terminal.
var errNoEscalation = errors.New("no way to ask for a password")

// escalate writes the plan, hands it to this same binary through sudo, and waits. sudo's own
// prompt is the password prompt: this never reads or passes a password.
func escalate(plan rootPlan) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return errNoEscalation
	}
	if !isTerminal(os.Stdin) {
		// sudo would fail with "a terminal is required"; saying so before it tries is kinder.
		return errNoEscalation
	}
	dir, err := os.MkdirTemp("", "acornfall-setup-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "plan.json")
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	fmt.Println()
	command := exec.Command(sudo, executable, "setup", "--privileged", "--plan", path)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

// setupReport is what the probes found. It is a value so that deciding, rendering, and the plan
// can be tested without a machine that has or lacks these things.
type setupReport struct {
	PackageManager string
	BwrapPath      string
	BwrapVersion   string
	BwrapErr       error
	PastaPath      string
	PastaErr       error
	// TunErr is /dev/net/tun, opened the way pasta opens it.
	TunErr error
	// UsernsRestricted is the AppArmor gate that refuses unprivileged user namespaces on
	// Ubuntu 23.10 and later: the one machine-wide cause worth naming.
	UsernsRestricted bool
	// Container is whether this process is in one, which changes who can fix what.
	Container bool
	KVMErr    error
	// LingerKnown is false when this machine has no systemd for the user: not a problem, just a
	// question that does not apply.
	LingerKnown bool
	Linger      bool
	InstallDir  string
	InstallErr  error
}

// gatherSetup asks the drivers, which is the same code a start would run: a check here that
// passes while a start fails would be worse than no check.
func gatherSetup(dataDir string) setupReport {
	bwrap := sandbox.NewBwrap(dataDir)
	hypervisor := sandbox.NewMicrosandbox(dataDir)
	report := setupReport{
		PackageManager:   packageManager(),
		BwrapPath:        bwrap.InstallDir(),
		BwrapVersion:     bwrap.Version(),
		BwrapErr:         bwrap.Usable(),
		PastaErr:         bwrap.Egress(),
		TunErr:           tunDevice(),
		UsernsRestricted: usernsRestricted(),
		Container:        inContainer(),
		KVMErr:           hypervisor.Usable(),
	}
	if path, err := exec.LookPath("pasta"); err == nil {
		report.PastaPath = path
	}
	report.LingerKnown, report.Linger = lingering()
	report.InstallDir, report.InstallErr = installFolder()
	return report
}

// tunDevice opens /dev/net/tun the way pasta does, without creating an interface: presence and
// permission are the question, and opening them is how the kernel answers it.
func tunDevice() error {
	device, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("/dev/net/tun is missing: pasta builds the sandbox's network from it")
		}
		if errors.Is(err, os.ErrPermission) {
			return errors.New("/dev/net/tun is not usable by this user: pasta builds the sandbox's network from it")
		}
		return fmt.Errorf("/dev/net/tun: %w", err)
	}
	_ = device.Close()
	return nil
}

// usernsRestricted reads the switch Ubuntu 23.10 added. A machine without the file is not
// restricted, which is every other distribution.
func usernsRestricted() bool {
	payload, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	return err == nil && strings.TrimSpace(string(payload)) == "1"
}

// inContainer reports whether this process is in one. It is not an identity check — it is enough
// to know that the kernel's policy here belongs to somebody else.
func inContainer() bool {
	for _, path := range []string{"/run/.containerenv", "/.dockerenv"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	payload, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	text := string(payload)
	for _, marker := range []string{"docker", "kubepods", "libpod", "lxc", "containerd"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// renderSetup is the table a person reads: what works, what does not, and what it means.
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
		fmt.Fprintf(&out, "  %s  sandbox network   %s\n", mark(pasta), describe(pasta, describePath(report.PastaPath, ""), pastaProblem(report)))
		if report.TunErr != nil && !pasta {
			fmt.Fprintf(&out, "  %s  tun device        %s\n", mark(false), report.TunErr)
		}
		if !usable || !pasta {
			ok = false
		}
	} else {
		ok = false
	}

	// The hypervisor is the other engine, not this one: a machine without /dev/kvm still runs
	// workspaces, on bubblewrap.
	if report.KVMErr == nil {
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
		if report.KVMErr != nil {
			out.WriteString("; the microVM engine needs the hypervisor step")
		}
		out.WriteString(".\n")
	}
	if report.UsernsRestricted && report.BwrapErr != nil && !report.Container {
		out.WriteString("\nthis machine restricts unprivileged user namespaces (Ubuntu 23.10 and later do).\n" +
			"allowing them is a machine-wide change; the alternative is an AppArmor profile for the\n" +
			"bubblewrap binary, which the distribution's own package ships.\n")
	}
	return out.String(), ok
}

// pastaProblem prefers the device over the packet: pasta's own message says it could not make a
// namespace, which is true but not the cause when the tun node is what is missing.
func pastaProblem(report setupReport) error {
	if report.PastaErr != nil && report.TunErr != nil {
		return report.TunErr
	}
	return report.PastaErr
}

// printedFixes are the same steps as copy-pasteable commands, for a machine where nobody can
// escalate: no sudo, no terminal.
func printedFixes(report setupReport, userName string) []string {
	plan := planFor(report, userName)
	var fixes []string
	for _, action := range plan.actions() {
		switch {
		case action.Run != nil:
			fixes = append(fixes, "sudo "+strings.Join(action.Run, " "))
		case action.File != "":
			fixes = append(fixes, fmt.Sprintf("sudo sh -c 'printf %s > %s'", shellQuote(action.Write), action.File))
		}
	}
	if len(fixes) == 0 && (report.BwrapPath == "" || report.PastaPath == "") {
		fixes = append(fixes, fmt.Sprintf("install %s with this distribution's package manager", strings.Join(missingPackages(report), " ")))
	}
	if report.InstallErr != nil {
		fixes = append(fixes, "reinstall into a folder you own: sh scripts/install.sh --dir ~/.acornfall/bin")
	}
	return fixes
}

func missingPackages(report setupReport) []string {
	var names []string
	if report.BwrapPath == "" {
		names = append(names, "bubblewrap")
	}
	if report.PastaPath == "" {
		names = append(names, "passt")
	}
	return names
}

// shellQuote is what makes a printed heredoc-safe command a command and not a shell surprise.
func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'"'"'`) + "'"
}

// packageManager is the command that installs a package here, from the distribution's own file.
// An unknown distribution gets no guess: the caller says so rather than inventing a command.
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
		for _, candidate := range []string{values["ID"], values["ID_LIKE"]} {
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
	case "apt-get", "dnf":
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
