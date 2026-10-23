package ready

import (
	"os"
	"path/filepath"
	"testing"
)

func testInput(dataDir string) Input {
	return Input{
		DataDir:         dataDir,
		Current:         "v0.9.0",
		Worker:          "v0.9.0",
		BunPath:         filepath.Join(dataDir, "tools", "bun-1.4.3", "bun"),
		BunOK:           true,
		EngineName:      "microsandbox",
		EngineVersion:   "0.7.6",
		EngineInstalled: true,
		EngineIsolated:  true,
		Latest:          "v0.9.0",
		HasCredential:   true,
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
	input.EngineDisabled = "ACORNFALL_NO_SANDBOX is set"
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
