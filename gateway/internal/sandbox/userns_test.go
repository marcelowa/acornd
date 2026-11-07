package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProbeFile stages what the kernel would publish, so a namespace the test cannot enter can
// still be stated. The probe reads files, not the running kernel, for exactly this reason.
func writeProbeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBwrapObservesNestingFromItsUserNamespace(t *testing.T) {
	mapping := writeProbeFile(t, "uid_map", "         0          0          1\n")
	if !observesUserNamespace(mapping, filepath.Join(t.TempDir(), "no-setgroups")) {
		t.Fatal("a single id is a namespace somebody else made")
	}
}

func TestBwrapObservesTheTopOfTheMachine(t *testing.T) {
	mapping := writeProbeFile(t, "uid_map", "         0          0 4294967295\n")
	if observesUserNamespace(mapping, filepath.Join(t.TempDir(), "no-setgroups")) {
		t.Fatal("the whole range is the machine, not a namespace inside it")
	}
	denied := writeProbeFile(t, "setgroups", "deny\n")
	if !observesUserNamespace(mapping, denied) {
		t.Fatal("setgroups reading deny corroborates a namespace even when the map covers every id")
	}
}

func TestTheMarkerOverrulesTheObservation(t *testing.T) {
	top := writeProbeFile(t, "top", "0 0 4294967295\n")
	nested := writeProbeFile(t, "nested", "0 0 1\n")
	if !nestedFromMarker("1", top, "") {
		t.Fatal("ACORND_NESTED=1 must force nested over a full map")
	}
	if nestedFromMarker("0", nested, "") {
		t.Fatal("ACORND_NESTED=0 must force the top over a narrow map")
	}
}

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
