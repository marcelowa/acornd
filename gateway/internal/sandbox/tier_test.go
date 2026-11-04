package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The tier is what makes a workspace's engine its own: one manager per engine, built once, and
// an unknown name refused rather than answered with a different engine.
func TestTierGivesEachEngineItsOwnManager(t *testing.T) {
	dataDir := t.TempDir()
	tier := NewTier(dataDir, "", nil)

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
	if errors.Is(err, ErrNoEngine) {
		// No sandbox on this machine, so there is nothing here to check: the rule that produces
		// this refusal, and never the host engine, is a table in TestAMachineWithNoSandboxHasNoDefault.
		return
	}
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	chosen, err := tier.Default()
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if manager.Driver().Name() != chosen {
		t.Fatalf("empty name resolved to %q, want %q", manager.Driver().Name(), chosen)
	}
	// Whatever it chose has to be able to run a workspace here, network and all.
	driver, err := Engine("", "", chosen)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Usable(); err != nil {
		t.Fatalf("%s is the default but cannot run here: %v", chosen, err)
	}
	if err := driver.Egress(); err != nil {
		t.Fatalf("%s is the default but cannot carry a network: %v", chosen, err)
	}
	// The order is the policy: bubblewrap first, the unisolated engine last.
	names := EngineNames()
	if names[0] != "bwrap" || names[len(names)-1] != "host" {
		t.Fatalf("preference order is %v, want bwrap first and host last", names)
	}
}

// A machine that can run no sandbox has no default, and says what each engine is missing. It
// does not answer "host": that is how a machine that used to be sandboxed starts running
// unconfined after an upgrade, with nothing on screen saying so.
func TestAMachineWithNoSandboxHasNoDefault(t *testing.T) {
	engine, err := defaultEngine(func(string) error {
		return errors.New("no user namespace and no hypervisor")
	})
	if engine != "" {
		t.Fatalf("a machine with no sandbox chose %q", engine)
	}
	if !errors.Is(err, ErrNoEngine) {
		t.Fatalf("err = %v, want ErrNoEngine", err)
	}
	for _, want := range []string{"bwrap", "microsandbox", "no user namespace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "host") {
		t.Errorf("the host engine must not even be offered as a default: %v", err)
	}
}

// The preference order is the policy: the first engine in it that can run a workspace with its
// network on is the one a workspace is handed.
func TestTheDefaultIsTheFirstEngineThatCanRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		can  []string
		want string
	}{
		{"both can", []string{"bwrap", "microsandbox"}, "bwrap"},
		{"only the microVM can", []string{"microsandbox"}, "microsandbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := defaultEngine(func(name string) error {
				if slices.Contains(tc.can, name) {
					return nil
				}
				return errors.New("unavailable")
			})
			if err != nil {
				t.Fatalf("default: %v", err)
			}
			if engine != tc.want {
				t.Fatalf("default = %q, want %q", engine, tc.want)
			}
		})
	}
}

// The host engine isolates nothing, so no machine may hand one out: it is still an engine a
// workspace or an install can name, which is the half that has to keep working.
func TestTheHostEngineIsExplicitOnly(t *testing.T) {
	for _, name := range defaultEngines() {
		if name == "host" {
			t.Fatal("the host engine is in the default list")
		}
	}
	if !slices.Contains(EngineNames(), "host") || !ValidEngine("host") {
		t.Fatal("the host engine is still an engine a workspace may name")
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
