package sandbox

import (
	"strings"
	"testing"
)

// The tier is what makes a workspace's engine its own: one manager per engine, built once, and
// an unknown name refused rather than answered with a different engine.
func TestTierGivesEachEngineItsOwnManager(t *testing.T) {
	home := t.TempDir()
	tier := NewTier(home, "", nil)

	host, err := tier.For("host")
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	if host.Driver().Name() != "host" {
		t.Fatalf("host manager runs %q", host.Driver().Name())
	}
	bwrap, err := tier.For("bwrap")
	if err != nil {
		t.Fatalf("bwrap: %v", err)
	}
	if bwrap.Driver().Name() != "bwrap" {
		t.Fatalf("bwrap manager runs %q", bwrap.Driver().Name())
	}
	if host == bwrap {
		t.Fatal("two engines share one manager")
	}
	// Built once: a second ask is the same manager, so its install and probe state is shared.
	again, err := tier.For("bwrap")
	if err != nil || again != bwrap {
		t.Fatalf("second ask: %v, same manager: %v", err, again == bwrap)
	}
}

func TestTierRefusesAnEngineItDoesNotImplement(t *testing.T) {
	tier := NewTier(t.TempDir(), "", nil)
	_, err := tier.For("podman")
	if err == nil {
		t.Fatal("an unknown engine was accepted")
	}
	// Named, with the list: never a quiet fallback to another engine.
	for _, want := range []string{"podman", "microsandbox", "bwrap", "host"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// An empty engine name is the machine's answer, not a constant: the first engine in the
// preference order that can run a workspace with a network.
func TestTierEmptyNameIsWhatTheMachineCanRun(t *testing.T) {
	tier := NewTier(t.TempDir(), "", nil)
	manager, err := tier.For("")
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if manager.Driver().Name() != tier.Default() {
		t.Fatalf("empty name resolved to %q, want %q", manager.Driver().Name(), tier.Default())
	}
	// Whatever it chose has to be able to run a workspace here, network and all.
	driver, err := Engine("", "", tier.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Usable(); err != nil {
		t.Fatalf("%s is the default but cannot run here: %v", tier.Default(), err)
	}
	if err := driver.Egress(); err != nil {
		t.Fatalf("%s is the default but cannot carry a network: %v", tier.Default(), err)
	}
	// The order is the policy: bubblewrap first, the unisolated engine last.
	names := EngineNames()
	if names[0] != "bwrap" || names[len(names)-1] != "host" {
		t.Fatalf("preference order is %v, want bwrap first and host last", names)
	}
}

// A workspace may name only an engine this build has, which is what keeps a record from
// pointing at a sandbox nothing can make.
func TestOnlyImplementedEnginesAreValidForAWorkspace(t *testing.T) {
	for _, name := range append(EngineNames(), "") {
		if !ValidEngine(name) {
			t.Fatalf("%q should be valid", name)
		}
	}
	for _, name := range []string{"podman", "Microsandbox", "docker"} {
		if ValidEngine(name) {
			t.Fatalf("%q should not be valid", name)
		}
	}
}
