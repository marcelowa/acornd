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

// setupAccount is the account the question is about. "Can this machine run a workspace" is really
// "can *this account* run one", and on a machine where root installed acornfall the two are not
// the same: root can open /dev/kvm and make a user namespace whatever the policy says, so probing
// as root would report a machine that works while the service's workspaces cannot boot.
type setupAccount struct {
	Name string
	// Basis says where the name came from, so a wrong guess is visible in the output rather than
	// discovered later. Empty means "there was nothing to guess".
	Basis string
	UID   int
	GID   int
	// DataDir is the account's own home: the child gets it so a tool that reads ~/.config reads that
	// account's, not the acornfall folder's.
	DataDir string
}

// accountFor decides which account setup is about. The owner of the acornfall folder is the
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
	self.Basis = "nothing says otherwise; pass --for if another account runs acornfall"
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
	if owner, err := folderOwner(dataDir); err == nil && owner != "" {
		if account, err := lookupAccount(owner); err == nil {
			account.Basis = "owns " + dataDir
			return account, true
		}
	}
	// A machine where acornfall is a service says so itself, which is the answer when the folder
	// root would look in does not exist — the common case when acornfall was installed for an
	// account other than root's.
	if name := serviceUser(); name != "" {
		if account, err := lookupAccount(name); err == nil {
			account.Basis = "the acornfall service runs as " + name
			return account, true
		}
	}
	return setupAccount{}, false
}

// serviceUser is the account the acornfall system service runs as, or empty when there is no such
// unit or no systemd to ask. It is the machine's own record of who runs this, which beats the
// guess "root, because nothing said otherwise".
func serviceUser() string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return ""
	}
	out, err := exec.Command("systemctl", "show", "acornfall.service", "--property=User", "--value").Output()
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
// sandbox, because a sandbox is made in that folder.
func folderOwner(dataDir string) (string, error) {
	info, err := os.Stat(dataDir)
	if err != nil {
		return "", err
	}
	uid := statOwner(info)
	owner, err := user.LookupId(strconv.Itoa(int(uid)))
	if err != nil {
		return "", err
	}
	return owner.Username, nil
}

// lookupAccount resolves a name to the numbers an exec needs, with its groups: the kvm group is
// the whole point of one of the probes, and a child without it would answer "no" for a machine
// that is fine.
func lookupAccount(name string) (setupAccount, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return setupAccount{}, fmt.Errorf("no account %q here", name)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return setupAccount{}, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return setupAccount{}, err
	}
	return setupAccount{Name: account.Username, UID: uid, GID: gid, DataDir: account.HomeDir}, nil
}

// probeAs answers the probes for one account. For this process's own account that is a direct
// question; for another one it is the same binary run with that account's credentials, which is
// how root can ask without sudo, without a password, and without a shell.
func probeAs(who setupAccount, dataDir string) (setupReport, error) {
	current, err := user.Current()
	if err != nil {
		return setupReport{}, err
	}
	if current.Username == who.Name {
		return gatherSetup(dataDir), nil
	}
	if os.Geteuid() != 0 {
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
	command := exec.Command(executable, "setup", "--probe", "--data-dir", dataDir)
	if attr := credentialFor(uint32(who.UID), uint32(who.GID), groupIDs); attr != nil {
		command.SysProcAttr = attr
	}
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
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return setupReport{}, fmt.Errorf("checking as %s: %s", who.Name, strings.TrimSpace(string(exit.Stderr)))
		}
		return setupReport{}, fmt.Errorf("checking as %s: %w", who.Name, err)
	}
	return decodeProbe(output)
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
		value, err := strconv.Atoi(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uint32(value))
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
		logf("acornfall setup: %v", err)
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
