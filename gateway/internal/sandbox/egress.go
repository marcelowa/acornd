package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ProbeEgress is the network question both container engines ask: pasta carries a workspace with
// its network on, and pasta does not care which engine made the sandbox. The engine's own probe
// is a field so a test can answer without running one.
func ProbeEgress(look func(string) (string, error), pasta func(string) error) error {
	path, err := look("pasta")
	if err != nil {
		return fmt.Errorf("%w: a workspace with its network on needs pasta to carry it (install it: apt install passt)", ErrUnavailable)
	}
	if pasta != nil {
		return pasta(path)
	}
	return probePasta(path)
}

// probePasta is a real probe, not a version check: pasta makes the namespace before it carries
// anything, and a machine that refuses that refuses every network-on workspace at start. It runs
// one cheap command there, which is what a start does anyway.
func probePasta(pasta string) error {
	return ProbePastaWithin(pasta, 10*time.Second)
}

// probeTarget is the program a probe asks pasta to start: this binary, at the path a sandbox hands
// pasta in real life. `/bin/true` was the old answer, and it is exactly what an AppArmor profile
// for pasta allows — so a probe that ran it said the network worked on a machine where every
// sandbox with one then refused to start its supervisor. `help` makes this binary exit at once,
// print its usage, and touch nothing.
func probeTarget() (string, []string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("cannot find this binary to ask pasta to start it: %w", err)
	}
	return executable, []string{"help"}, nil
}

// probePastaWithin is probePasta with the wait it is allowed, so a test can ask the question in a
// second instead of ten. The wait is the context's; WaitDelay bounds the other half — pasta can
// leave a process holding the output pipe, and CombinedOutput waits for every writer to close it.
// A probe that waits forever is a sandbox that never starts, which is what a pasta stuck on a
// missing /dev/net/tun produced inside a dev workspace.
func ProbePastaWithin(pasta string, wait time.Duration) error {
	target, args, err := probeTarget()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	argv := []string{
		"-q", "-t", "none", "-u", "none", "-T", "none", "-U", "none", "--no-map-gw",
		"-a", "10.0.2.15", "-n", "24", "-g", "10.0.2.2", "--", target,
	}
	argv = append(argv, args...)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	command := exec.CommandContext(ctx, pasta, argv...)
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	detail := strings.TrimSpace(string(output))
	// pasta can print a failure on stderr and still exit 0: inside a bwrap workspace without
	// the caps nested pasta needs, it cannot write its uid map and runs the command anyway.
	// The caller must not read that as a network that works.
	if err == nil && !pastaUserNamespaceFailure(detail) && !pastaStartFailure(detail) {
		return nil
	}
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if pastaStartFailure(detail) {
		return fmt.Errorf("%w: pasta cannot start the supervisor it is given: %s (Ubuntu 26.04's passt package ships an AppArmor profile that lets pasta start a program only from /usr/bin or /bin, and acornd's supervisor is in neither; `acornd setup` turns it off)", ErrUnavailable, LastLine(detail))
	}
	return fmt.Errorf("%w: pasta cannot make a network namespace here: %s", ErrUnavailable, LastLine(detail))
}

// pastaStartFailure is pasta's sentence when it could not start the program it was given: a
// profile that refuses it, a mode that is not executable, or no such file. The three are the same
// answer — a network that carries nothing — and pasta's own sentence names no cause.
func pastaStartFailure(detail string) bool {
	return strings.Contains(detail, "Failed to start command or shell")
}

// pastaUserNamespaceFailure is the failure pasta prints when it cannot create the user
// namespace it runs the sandbox in. The exit status is 0 because the command it ran still
// ran; only the output says the namespace never existed.
func pastaUserNamespaceFailure(detail string) bool {
	return strings.Contains(detail, "Couldn't configure user mappings") ||
		strings.Contains(detail, "Couldn't write to /proc/self/uid_map")
}
