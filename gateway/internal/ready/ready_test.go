package ready

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testInput(dataDir string) Input {
	return Input{
		DataDir:          dataDir,
		Current:          "v0.9.0",
		Worker:           "v0.9.0",
		BunPath:          filepath.Join(dataDir, "tools", "bun-1.4.4", "bun"),
		BunOK:            true,
		EngineName:       "microsandbox",
		EngineVersion:    "0.7.6",
		EngineInstalled:  true,
		EngineIsolated:   true,
		Latest:           "v0.9.0",
		HasCredential:    true,
		HypervisorOK:     true,
		HypervisorDetail: "/dev/kvm present",
	}
}

func byName(requirements []Requirement, name string) Requirement {
	for _, requirement := range requirements {
		if requirement.Name == name {
			return requirement
		}
	}
	return Requirement{}
}

// allOK is the question the tests ask of a list: it lives here because nothing in the package
// needs it, and a helper nobody calls is the thing this pass was looking for.
func allOK(requirements []Requirement) bool {
	for _, requirement := range requirements {
		if !requirement.OK {
			return false
		}
	}
	return true
}

func TestSatisfiedInstallIsQuiet(t *testing.T) {
	dataDir := t.TempDir()
	requirements := Check(testInput(dataDir))
	if !allOK(requirements) {
		for _, requirement := range requirements {
			if !requirement.OK {
				t.Errorf("%s: %s (fix: %s)", requirement.Name, requirement.Detail, requirement.Fix)
			}
		}
	}
}

func TestMissingPiecesNameFixes(t *testing.T) {
	dataDir := t.TempDir()
	input := testInput(dataDir)
	input.Worker = ""
	input.BunOK = false
	input.HasCredential = false
	input.Latest = ""
	input.ReleasesNote = "unreachable: refused"
	requirements := Check(input)
	if allOK(requirements) {
		t.Fatal("missing pieces must not satisfy")
	}
	for _, name := range []string{"worker", "bun", "credentials", "releases"} {
		requirement := byName(requirements, name)
		if requirement.OK {
			t.Errorf("%s should fail", name)
		}
		if requirement.Fix == "" {
			t.Errorf("%s must name a fix", name)
		}
	}
}

func TestEngineRefusalIsStated(t *testing.T) {
	dataDir := t.TempDir()
	input := testInput(dataDir)
	input.EngineInstalled = false
	input.EngineProblem = ""
	input.EngineDisabled = "ACORND_NO_SANDBOX is set"
	requirement := byName(Check(input), "engine")
	if !requirement.OK {
		t.Fatal("a stated refusal is an answer, not a failure")
	}
}

func TestEngineProblemFails(t *testing.T) {
	dataDir := t.TempDir()
	input := testInput(dataDir)
	input.EngineInstalled = false
	input.EngineProblem = "no hypervisor"
	if requirement := byName(Check(input), "engine"); requirement.OK {
		t.Fatal("an engine problem must fail")
	}
}

func TestCredentialsReadTheFile(t *testing.T) {
	dataDir := t.TempDir()
	if HasCredential(dataDir) {
		t.Fatal("no file, no credential")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "credentials.json"), []byte(`{"x":{"type":"api_key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasCredential(dataDir) {
		t.Fatal("a stored key must count")
	}
}

func TestWorkerSkewAgainstBinarySatisfies(t *testing.T) {
	dataDir := t.TempDir()
	input := testInput(dataDir)
	input.Current = "v0.9.8"
	input.RuntimeTag = "v0.9.9"
	input.Worker = "v0.9.9"
	if requirement := byName(Check(input), "worker"); !requirement.OK {
		t.Fatalf("sources matching the binary must satisfy: %+v", requirement)
	}
}

// Both answers name the device, and the machine decides which one is right: the check reports
// the fact it read rather than a boolean a screen has to explain on its own. The empty detail is
// the caller saying "I gathered no sentence", which is what a boot does before a sandbox exists.
//
// This is the one mutant pair the mutation floor had been carrying: a test called Hypervisor()
// and never looked at what came back.
func TestHypervisorSaysWhatTheDeviceSays(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the device is a Linux fact")
	}
	_, err := os.Stat("/dev/kvm")
	ok, detail := Hypervisor()
	if ok != (err == nil) {
		t.Errorf("Hypervisor() = %v, but /dev/kvm exists: %v", ok, err == nil)
	}
	if !strings.Contains(detail, "kvm") {
		t.Errorf("the detail should name the device, got %q", detail)
	}
	input := testInput(t.TempDir())
	input.HypervisorOK = false
	input.HypervisorDetail = ""
	if requirement := byName(Check(input), "hypervisor"); requirement.Detail != "no /dev/kvm" {
		t.Errorf("a refusal with no detail should name the device, got %q", requirement.Detail)
	}
	// A caller that gathered its own sentence keeps it: the check reports the machine, it does
	// not rewrite the answer it was handed.
	input = testInput(t.TempDir())
	input.HypervisorDetail = "/dev/kvm present, nested virtualisation"
	if requirement := byName(Check(input), "hypervisor"); requirement.Detail != input.HypervisorDetail {
		t.Errorf("the caller's sentence should be kept, got %q", requirement.Detail)
	}
}

// A data folder that does not exist yet is judged by its parent, which is where creating it would
// happen and where the space would come from. The answer still carries the number: a person reads
// "30G free" and not "unreadable", and that number is the parent's. Whether 90M of it is enough
// is the machine's question and `TestSatisfiedInstallIsQuiet` asks it; this one asks where the
// number came from.
func TestDiskJudgesAFolderThatIsNotThereYet(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-created")
	requirement := byName(Check(testInput(dataDir)), "disk")
	if !strings.HasSuffix(requirement.Detail, "M free") {
		t.Errorf("the detail should be the parent's free space, got %q", requirement.Detail)
	}
}
