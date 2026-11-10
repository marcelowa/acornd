package bwrap

import (
	"strings"
	"testing"
)

// A sandbox that may nest must mount a writable /proc. Binding the host's read-only is the
// failure that reads like a kernel refusal at the first nested start: "Read-only file system".
func TestABwrapThatMayNestMountsAWritableProc(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	argv := driver.mountArgs(bwrapConfig{Rootfs: "/rootfs"})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--proc /proc") {
		t.Fatalf("argv has no writable /proc: %v", argv)
	}
	if strings.Contains(joined, "--ro-bind /proc") {
		t.Fatalf("a read-only /proc fails the first nested start: %v", argv)
	}
}

// A parent that made a user namespace for pasta holds CAP_SYS_ADMIN, so bubblewrap can make the
// other namespaces inside it. A youki container holds CAP_SETFCAP and not CAP_SYS_ADMIN: there the
// nested sandbox needs a user namespace of its own, and the same CAP_SETFCAP is what lets it write
// the uid map. The bit is read from the kernel, not guessed from the container's name.
func TestTheParentCapabilitiesSayWhetherNamespacesCanBeReused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   bool
	}{
		{"a youki container has CAP_SETFCAP only", "Name:\tacornd\nCapEff:\t0000000080000000\n", false},
		{"a pasta parent has CAP_SYS_ADMIN", "CapEff:\t000001ffffffffff\n", true},
		{"CAP_SYS_ADMIN alone is enough", "CapEff:\t0000000000200000\n", true},
		{"no capability line", "Name:\tacornd\n", false},
		{"a mask that is not a number", "CapEff:\tnot-a-mask\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCapability([]byte(tc.status), capSysAdmin); got != tc.want {
				t.Fatalf("hasCapability(CAP_SYS_ADMIN) = %v, want %v", got, tc.want)
			}
		})
	}
}
