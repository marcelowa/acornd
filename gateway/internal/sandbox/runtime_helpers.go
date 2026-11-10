package sandbox

import (
	"os"
	"strings"
	"syscall"
)

// ProcessPath is what the sandbox's own processes find a program on. The supervisor is started by
// absolute path and reads flags, but a shell inside a sandbox is started with the policy's own PATH;
// this is the one the sandbox process itself gets before anything of the workspace exists.
const ProcessPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// LastLine keeps a probe's failure to one line, so a state frame stays readable.
func LastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// Alive reports whether a process exists. Signal 0 asks the question without sending anything.
func Alive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// IdleTimeoutOrDefault is the policy's idle timeout, or the default when the policy did not name
// one. The default normally comes from Policy.WithDefaults, which the microVM driver calls while
// rendering its file; a driver that reads the field directly has to ask, or a sandbox that nobody
// told about the timer keeps running forever.
func IdleTimeoutOrDefault(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return DefaultIdleTimeout
}
