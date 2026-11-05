package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

// unknownAccount is the name of a number this machine has no record of. A folder can belong to one
// — a container image that bakes files as a uid, an account deleted after the install — and that
// number is still the answer to "who does this install belong to", so it is named rather than
// dropped.
const unknownAccount = "?"

// setupAccount is the account the question is about. "Can this machine run a workspace" is really
// "can *this account* run one", and on a machine where root installed acornd the two are not
// the same: root can open /dev/kvm and make a user namespace whatever the policy says, so probing
// as root would report a machine that works while the service's workspaces cannot boot.
type setupAccount struct {
	Name string
	// Basis says where the name came from, so a wrong guess is visible in the output rather than
	// discovered later. Empty means "there was nothing to guess".
	Basis string
	// The numbers are 32 bits wide because that is the width a process is handed on exec, which is
	// what they are for here; they are parsed at that width rather than converted to it later.
	UID uint32
	GID uint32
	// DataDir is the account's own home: the child gets it so a tool that reads ~/.config reads that
	// account's, not the acornd folder's.
	DataDir string
}

// accountFor decides which account setup is about. The owner of the acornd folder is the
// honest default — it is the account the install belongs to — and --for overrides it.
func accountFor(requested string, root bool, dataDir string) (setupAccount, error) {
	if requested != "" {
		account, err := lookupAccount(requested)
		if err != nil {
			return setupAccount{}, err
		}
		account.Basis = "--for"
		return account, nil
	}
	current, err := user.Current()
	if err != nil {
		return setupAccount{}, err
	}
	self, err := lookupAccount(current.Username)
	if err != nil {
		return setupAccount{}, err
	}
	if !root {
		return self, nil
	}
	// root is acting for somebody, and three things can say who.
	if account, found := actingFor(dataDir); found {
		return account, nil
	}
	// Nothing to go on: root is the only answer, and the basis says so rather than pretending.
	self.Basis = "nothing says otherwise; pass --for if another account runs acornd"
	return self, nil
}

// actingFor is who root is acting for, in the order of how strongly each says it: sudo knows, the
// folder's owner is the next best evidence, and a service unit says outright.
func actingFor(dataDir string) (setupAccount, bool) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if account, err := lookupAccount(sudoUser); err == nil {
			account.Basis = "the account that ran sudo"
			return account, true
		}
	}
	if owner, err := folderOwner(dataDir); err == nil {
		owner.Basis = "owns " + dataDir
		return owner, true
	}
	// A machine where acornd is a service says so itself, which is the answer when the folder
	// root would look in does not exist — the common case when acornd was installed for an
	// account other than root's.
	if name := serviceUser(); name != "" {
		if account, err := lookupAccount(name); err == nil {
			account.Basis = "the acornd service runs as " + name
			return account, true
		}
	}
	return setupAccount{}, false
}

// serviceUser is the account the acornd system service runs as, or empty when there is no such
// unit or no systemd to ask: systemctl says both by failing, so its failure is the answer and there
// is nothing extra to probe for first. It is the machine's own record of who runs this, which beats
// the guess "root, because nothing said otherwise"; and it is a variable so a test can say what
// this machine says — a unit, or a machine without systemd — without installing either.
var serviceUser = func() string {
	out, err := exec.Command("systemctl", "show", "acornd.service", "--property=User", "--value").Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if name == "" || name == "root" {
		return ""
	}
	return name
}

// folderOwner is the account the install folder belongs to: the same account that will run the
// sandbox, because a sandbox is made in that folder. The owner's number is the evidence, not the
// name, because the name is the half this machine may not have.
func folderOwner(dataDir string) (setupAccount, error) {
	info, err := os.Stat(dataDir)
	if err != nil {
		return setupAccount{}, err
	}
	uid, gid := statOwner(info)
	return accountForNumbers(uid, gid), nil
}

// accountForNumbers is the account a number names here, or "?" when it names none. A folder owned
// by a number the passwd file does not mention still says who the install belongs to; resolving the
// name and dropping the number was how root fell through to "nothing says otherwise" and checked
// the machine as itself.
func accountForNumbers(uid, gid uint32) setupAccount {
	if record, err := user.LookupId(strconv.Itoa(int(uid))); err == nil {
		if account, err := accountOf(record); err == nil {
			return account
		}
	}
	return setupAccount{Name: unknownAccount, UID: uid, GID: gid}
}

// lookupAccount resolves a name to the numbers an exec needs, with its groups: the kvm group is
// the whole point of one of the probes, and a child without it would answer "no" for a machine
// that is fine.
func lookupAccount(name string) (setupAccount, error) {
	record, err := user.Lookup(name)
	if err != nil {
		return setupAccount{}, fmt.Errorf("no account %q here", name)
	}
	return accountOf(record)
}

// accountOf is one record from this machine, in the shape an exec needs: the name, the numbers, and
// the home the child gets.
func accountOf(record *user.User) (setupAccount, error) {
	uid, err := accountID(record.Uid)
	if err != nil {
		return setupAccount{}, err
	}
	gid, err := accountID(record.Gid)
	if err != nil {
		return setupAccount{}, err
	}
	return setupAccount{Name: record.Username, UID: uid, GID: gid, DataDir: record.HomeDir}, nil
}

// accountID is one of an account's numbers in the width a process is given it. The decimal comes
// from os/user, and a value that does not fit is a refusal rather than a wrap: this number decides
// which account the probe runs as, so a wrapped one would ask a different account the question.
func accountID(value string) (uint32, error) {
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("setup: %q is not an account id: %w", value, err)
	}
	return uint32(id), nil
}

// cannotCheck says why this account cannot be asked about the machine, or nil when it can. A number
// with no record has no home, no groups and no shell to be checked with; checking as root instead
// would report root's kvm and root's namespaces for workspaces that run as somebody else. Say which
// number it is, and which account to name in its place.
func cannotCheck(who setupAccount, dataDir string) error {
	if who.Name != unknownAccount {
		return nil
	}
	return fmt.Errorf("the install folder belongs to uid %d, which has no account here: pass --for, or chown %s to an account this machine has", who.UID, dataDir)
}

// probeAs answers the probes for one account. For this process's own account that is a direct
// question; for another one it is the same binary run with that account's credentials, which is
// how root can ask without sudo, without a password, and without a shell.
func probeAs(who setupAccount, dataDir string) (setupReport, error) {
	if err := cannotCheck(who, dataDir); err != nil {
		return setupReport{}, err
	}
	if report, ok := probeInProcess(who, dataDir); ok {
		return report, nil
	}
	current, err := user.Current()
	if err != nil {
		return setupReport{}, err
	}
	sameAccount := current.Username == who.Name
	if !sameAccount && os.Geteuid() != 0 {
		return setupReport{}, fmt.Errorf("cannot check %s from this account: run setup as %s", who.Name, who.Name)
	}
	executable, err := os.Executable()
	if err != nil {
		return setupReport{}, err
	}
	groupIDs, err := groupIDsFor(who.Name)
	if err != nil {
		return setupReport{}, err
	}
	command := probeCommand(executable, dataDir, sameAccount, who, groupIDs)
	// The child's environment is the account's, not root's: PATH decides which bwrap and pasta it
	// finds, and HOME decides nothing (the folder is passed) but a sessionless root's env would
	// still be a lie about whose machine this is.
	command.Env = []string{
		"HOME=" + who.DataDir,
		"USER=" + who.Name,
		"LOGNAME=" + who.Name,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	output, err := command.Output()
	if err != nil {
		return setupReport{}, fmt.Errorf("checking as %s: %w", who.Name, probeFailure(err))
	}
	return decodeProbe(output)
}

// probeFailure is a child that would not answer as itself: the sentence it printed is the whole
// value, and an exit status without one is the error it was.
func probeFailure(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return errors.New(strings.TrimSpace(string(exit.Stderr)))
	}
	return err
}

// probeInProcess answers in this process when the account is this one and the session is not missing a group the
// account has: that is the cheap path, and the only one that needs no child.
func probeInProcess(who setupAccount, dataDir string) (setupReport, bool) {
	current, err := user.Current()
	if err != nil || current.Username != who.Name {
		return setupReport{}, false
	}
	if kvmGroupNeeded() {
		return setupReport{}, false
	}
	return gatherSetup(dataDir), true
}

// probeCommand is the probe as a child: the same account but a session missing the kvm group goes through sg,
// which lends it; another account is given that account's ids and groups directly.
func probeCommand(executable, dataDir string, sameAccount bool, who setupAccount, groupIDs []uint32) *exec.Cmd {
	if sameAccount {
		line := sgQuote(executable) + " setup --probe --data-dir " + sgQuote(dataDir)
		command := exec.Command("sg", "kvm", "-c", line)
		command.Env = append(os.Environ(), "ACORND_KVM_SG=1")
		return command
	}
	command := exec.Command(executable, "setup", "--probe", "--data-dir", dataDir)
	if attr := credentialFor(who.UID, who.GID, groupIDs); attr != nil {
		command.SysProcAttr = attr
	}
	return command
}

// groupIDsFor is every group the account is in, which is what a login would give it.
func groupIDsFor(name string) ([]uint32, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	names, err := account.GroupIds()
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, 0, len(names))
	for _, id := range names {
		value, err := accountID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, value)
	}
	return ids, nil
}

// probeWire is the probe as it crosses a process boundary: problems become strings, because an
// error does not travel through JSON and the sentence inside it is the whole value.
type probeWire struct {
	PackageManager   string `json:"packageManager"`
	BwrapPath        string `json:"bwrapPath,omitempty"`
	BwrapVersion     string `json:"bwrapVersion,omitempty"`
	BwrapProblem     string `json:"bwrapProblem,omitempty"`
	PastaPath        string `json:"pastaPath,omitempty"`
	PastaProblem     string `json:"pastaProblem,omitempty"`
	TunProblem       string `json:"tunProblem,omitempty"`
	UsernsRestricted bool   `json:"usernsRestricted,omitempty"`
	PastaProfile     bool   `json:"pastaProfile,omitempty"`
	Container        bool   `json:"container,omitempty"`
	KVMProblem       string `json:"kvmProblem,omitempty"`
	LingerKnown      bool   `json:"lingerKnown,omitempty"`
	Linger           bool   `json:"linger,omitempty"`
	InstallDir       string `json:"installDir,omitempty"`
	InstallProblem   string `json:"installProblem,omitempty"`
}

// writeProbe is the hidden mode: answer with the checks and nothing else. It is reached only by
// probeAs, and it never decides anything.
func writeProbe(dataDir string) {
	payload, err := json.Marshal(wireOf(gatherSetup(dataDir)))
	if err != nil {
		logf("acornd setup: %v", err)
		os.Exit(1)
	}
	fmt.Println(string(payload))
}

func decodeProbe(payload []byte) (setupReport, error) {
	var wire probeWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		return setupReport{}, fmt.Errorf("the checks came back unreadable: %w", err)
	}
	return reportOf(wire), nil
}

func wireOf(report setupReport) probeWire {
	return probeWire{
		PackageManager:   report.PackageManager,
		BwrapPath:        report.BwrapPath,
		BwrapVersion:     report.BwrapVersion,
		BwrapProblem:     problemText(report.BwrapErr),
		PastaPath:        report.PastaPath,
		PastaProblem:     problemText(report.PastaErr),
		TunProblem:       problemText(report.TunErr),
		UsernsRestricted: report.UsernsRestricted,
		PastaProfile:     report.PastaProfile,
		Container:        report.Container,
		KVMProblem:       problemText(report.KVMErr),
		LingerKnown:      report.LingerKnown,
		Linger:           report.Linger,
		InstallDir:       report.InstallDir,
		InstallProblem:   problemText(report.InstallErr),
	}
}

func reportOf(wire probeWire) setupReport {
	return setupReport{
		PackageManager:   wire.PackageManager,
		BwrapPath:        wire.BwrapPath,
		BwrapVersion:     wire.BwrapVersion,
		BwrapErr:         problemError(wire.BwrapProblem),
		PastaPath:        wire.PastaPath,
		PastaErr:         problemError(wire.PastaProblem),
		TunErr:           problemError(wire.TunProblem),
		UsernsRestricted: wire.UsernsRestricted,
		PastaProfile:     wire.PastaProfile,
		Container:        wire.Container,
		KVMErr:           problemError(wire.KVMProblem),
		LingerKnown:      wire.LingerKnown,
		Linger:           wire.Linger,
		InstallDir:       wire.InstallDir,
		InstallErr:       problemError(wire.InstallProblem),
	}
}

func problemText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func problemError(text string) error {
	if text == "" {
		return nil
	}
	return errors.New(text)
}
