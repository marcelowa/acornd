package ready

import (
	"runtime"
	"strings"
	"testing"
)

// Sources that came with the binary satisfy the worker requirement even when the running binary
// does not name a version yet: a dev build has no tag to compare against, and refusing there
// would leave a working install with a requirement it can never meet.
func TestSourcesSatisfyAnUnnamedBuild(t *testing.T) {
	requirement := worker(Input{Worker: "1.2.3", Current: ""})
	if !requirement.OK {
		t.Errorf("sources with no current version should be satisfied: %+v", requirement)
	}
	if requirement := worker(Input{Worker: "1.2.3", Current: "1.2.4"}); requirement.OK {
		t.Error("sources that do not match the binary should not be satisfied")
	}
}

// The engine's version is shown with its name when there is one, and the name alone when there is
// not — without a dangling space, which is what makes the line look unfinished.
func TestTheEngineIsNamedWithItsVersionWhenThereIsOne(t *testing.T) {
	withVersion := engine(Input{EngineInstalled: true, EngineName: "microsandbox", EngineVersion: "1.2.3", EngineIsolated: true})
	if !strings.Contains(withVersion.Detail, "1.2.3") {
		t.Errorf("the version should be in %q", withVersion.Detail)
	}
	withoutVersion := engine(Input{EngineInstalled: true, EngineName: "microsandbox", EngineIsolated: true})
	if strings.HasSuffix(withoutVersion.Detail, " ") || !strings.Contains(withoutVersion.Detail, "microsandbox") {
		t.Errorf("no version means no trailing space: %q", withoutVersion.Detail)
	}

	host := engine(Input{EngineInstalled: true, EngineName: "host", EngineIsolated: false})
	if !host.OK || !strings.Contains(host.Detail, "unconfined") {
		t.Errorf("the host engine runs unconfined and the line has to say so: %+v", host)
	}
}

// Where there is no hypervisor to check for, the line says so rather than reporting a missing
// device: a Mac has no /dev/kvm and is not broken.
func TestTheHypervisorLineKnowsItsPlatform(t *testing.T) {
	requirement := hypervisor()
	if runtime.GOOS != "linux" {
		if !requirement.OK || !strings.Contains(requirement.Detail, "not applicable") {
			t.Errorf("off Linux the check does not apply: %+v", requirement)
		}
		return
	}
	// On Linux the answer is about /dev/kvm, whichever way it goes, and never "not applicable".
	if strings.Contains(requirement.Detail, "not applicable") {
		t.Errorf("Linux has a hypervisor to look for: %+v", requirement)
	}
}

// Disk space is measured where the data folder is, and a folder that cannot be measured falls back
// to its parent; when neither can be read the answer is "assume room" rather than "no room",
// because a requirement that cannot be checked is not evidence of a problem.
func TestDiskSpaceFallsBackAndThenAssumesRoom(t *testing.T) {
	requirement := disk(t.TempDir())
	if !requirement.OK {
		t.Errorf("a real folder has space: %+v", requirement)
	}
	unreadable := disk("/nonexistent-acornfall-check/deeper")
	if !unreadable.OK || !strings.Contains(unreadable.Detail, "assuming room") {
		t.Errorf("an unreadable path should assume room: %+v", unreadable)
	}
}
