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
	// The host engine is one of this build's engines; a test is what turns it on.
	t.Setenv(TestHostEnv, "1")
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
	// Named, with the list: never a quiet fallback to another engine. The host engine is not in
	// the list because this process was not told to have one.
	for _, want := range []string{"podman", "microsandbox", "bwrap"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "host") {
		t.Fatalf("a normal install must not offer the host engine: %v", err)
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
// The host engine is off unless a test asks for it: not in a chooser, not a name a workspace may
// record, and not a driver this process builds. It runs commands with the user's own reach, so a
// normal install has no way to reach it at all.
func TestTheHostEngineIsOffUnlessATestAsksForIt(t *testing.T) {
	if HostEnabled() {
		t.Fatal("this test is about the gate being off, and something set it")
	}
	if slices.Contains(ChoosableEngines(), "host") || ValidEngine("host") {
		t.Fatal("a normal install offers the host engine")
	}
	if _, err := Engine(t.TempDir(), "", "host"); err == nil || !strings.Contains(err.Error(), TestHostEnv) {
		t.Fatalf("Engine should refuse the host engine and name the switch: %v", err)
	}
	// It is still an engine this build has: doctor says what it is, and says it is off.
	if !slices.Contains(EngineNames(), "host") {
		t.Fatal("the build lost the host engine instead of switching it off")
	}

	// The switch is the whole difference, and it is what the test tiers set.
	t.Setenv(TestHostEnv, "1")
	if !HostEnabled() || !slices.Contains(ChoosableEngines(), "host") || !ValidEngine("host") {
		t.Fatal("a process told to have the host engine did not get it")
	}
	if _, err := Engine(t.TempDir(), "", "host"); err != nil {
		t.Fatalf("Engine(host) with the switch on: %v", err)
	}
}

// A workspace may name only an engine this install offers, which is what keeps a record from
// pointing at a sandbox nothing can make.
func TestOnlyImplementedEnginesAreValidForAWorkspace(t *testing.T) {
	for _, name := range append(ChoosableEngines(), "") {
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

// youki is selectable per workspace once its client exists, but the default order does not move
// until its tier is green on a machine with and without systemd (issue #47 R12).
func TestYoukiIsChoosableButNotADefaultYet(t *testing.T) {
	if !slices.Contains(EngineNames(), "youki") || !ValidEngine("youki") {
		t.Fatal("youki should be an engine a workspace may name")
	}
	if slices.Contains(defaultEngines(), "youki") {
		t.Fatalf("youki is in the default order before its tier is green: %v", defaultEngines())
	}
	engine, err := defaultEngine(func(name string) error {
		if name == "youki" {
			return nil
		}
		return errors.New("unavailable")
	})
	if engine != "" || !errors.Is(err, ErrNoEngine) {
		t.Fatalf("a machine with only youki must not hand it out as a default: %q %v", engine, err)
	}
}
