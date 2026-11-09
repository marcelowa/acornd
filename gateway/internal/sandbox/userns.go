package sandbox

import (
	"os"
	"strconv"
	"strings"
)

// NestedFromProcess says whether this acornd is already inside a sandbox. The environment can
// answer for it in both directions; when it does not, the kernel is asked.
func NestedFromProcess() bool {
	return nestedFromMarker(os.Getenv(nestedEnv), "/proc/self/uid_map", "/proc/self/setgroups")
}

// nestedFromMarker lets a caller state the marker and the namespace it should overrule. "1" and
// "0" are answers; anything else defers to the namespace.
func nestedFromMarker(marker, uidMap, setgroups string) bool {
	switch marker {
	case "1":
		return true
	case "0":
		return false
	}
	return observesUserNamespace(uidMap, setgroups)
}

// observesUserNamespace reads where this process sits. The initial user namespace maps every
// id; one somebody else made maps less, because an unprivileged creator can only map the ids it
// owns. The map is the fact; /proc/self/setgroups corroborates it and is read only when the
// kernel publishes it, which is only inside a user namespace.
func observesUserNamespace(uidMap, setgroups string) bool {
	mapping, err := os.ReadFile(uidMap)
	if err != nil {
		// Nothing to read: assume the top of the machine. Guessing "nested" would take the
		// reuse path with no namespace to reuse.
		return false
	}
	if !mapsEveryID(mapping) {
		return true
	}
	groups, err := os.ReadFile(setgroups)
	return err == nil && strings.TrimSpace(string(groups)) == "deny"
}

// mapsEveryID reports whether the mapping is the whole 32-bit range in one line. Several lines
// or a gap mean the process is not in the namespace that owns the machine.
func mapsEveryID(mapping []byte) bool {
	fields := strings.Fields(string(mapping))
	if len(fields) != 3 || fields[0] != "0" || fields[1] != "0" {
		return false
	}
	width, err := strconv.ParseUint(fields[2], 10, 64)
	return err == nil && width == (1<<32)-1
}
