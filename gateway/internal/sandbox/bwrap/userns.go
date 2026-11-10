package bwrap

import (
	"os"
	"strconv"
	"strings"
)

// holdsSysAdmin reports whether this process holds CAP_SYS_ADMIN, which is what bubblewrap needs
// to make namespaces without a user namespace of its own. A parent that made one for pasta holds
// it; a youki container meets the kernel's uid-map rule with CAP_SETFCAP and nothing else, so a
// nested sandbox there has to ask for a user namespace of its own.
func holdsSysAdmin() bool {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	return hasCapability(status, capSysAdmin)
}

// capSysAdmin is CAP_SYS_ADMIN's bit in the capability mask.
const capSysAdmin = 21

// hasCapability reads one bit out of a /proc/self/status document's CapEff line. A file this
// process cannot read or understand is a no: a sandbox that guessed yes would take the reuse path
// on a parent that cannot make namespaces.
func hasCapability(status []byte, bit int) bool {
	for line := range strings.SplitSeq(string(status), "\n") {
		raw, ok := strings.CutPrefix(strings.TrimSpace(line), "CapEff:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(raw), 16, 64)
		if err != nil {
			return false
		}
		return mask&(1<<bit) != 0
	}
	return false
}
