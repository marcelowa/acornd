package sandbox

import (
	"os"
	"path/filepath"
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
