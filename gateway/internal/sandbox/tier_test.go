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

func TestTierEmptyNameIsTheDefaultEngine(t *testing.T) {
	tier := NewTier(t.TempDir(), "", nil)
	manager, err := tier.For("")
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if manager.Driver().Name() != DefaultEngine {
		t.Fatalf("default engine is %q, want %q", manager.Driver().Name(), DefaultEngine)
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
