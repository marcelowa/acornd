package sandbox

import (
	"context"
	"errors"
	"testing"
)

// How a value reaches a command is a promise made to the person who typed it, so the two names
// are pinned: "substitute" is the one where the guest never sees it.
func TestDeliverySaysWhichPromiseItIs(t *testing.T) {
	cases := map[Delivery]string{
		DeliverySubstitute:  "substitute",
		DeliveryEnvironment: "environment",
		Delivery(99):        "unknown",
	}
	for delivery, want := range cases {
		if got := delivery.String(); got != want {
			t.Errorf("Delivery(%d).String() = %q, want %q", int(delivery), got, want)
		}
	}
}

// The host engine is the one that is not isolated, and everything it says follows from that: no
// artifacts to install, no state of its own, no environment to find it by — and every tool secret
// in the worker's own process, because there is no guest to keep it out of. A surface that
// describes the sandbox reads these answers, so a wrong one is a lie in the interface.
func TestTheHostEngineSaysWhatItIs(t *testing.T) {
	host := &Host{}
	if host.Isolated() {
		t.Error("the host engine is not isolated")
	}
	if host.InstallDir() != "" || host.StateDir() != "" {
		t.Error("the host engine has no artifacts and no state folder")
	}
	if host.Env() != nil {
		t.Errorf("the host engine has no store to find, env = %v", host.Env())
	}
	if !host.Installed() || host.Install(context.Background(), nil) != nil {
		t.Error("the host engine is always installed and installing it does nothing")
	}
	if host.Usable() != nil || host.Egress() != nil {
		t.Error("the host engine runs where the gateway already runs")
	}
	if err := host.Create(context.Background(), Policy{}, "w"); err != nil {
		t.Errorf("there is nothing to create for a workspace on the host: %v", err)
	}
	if host.SecretDelivery() != DeliveryEnvironment {
		t.Error("a secret for a host command is an environment variable, and the interface must say so")
	}
	if !host.SecretEnvInWorker() {
		t.Error("a host command reads the worker's own environment")
	}
	change, err := host.SecretsChanged(context.Background(), "w", nil)
	if err != nil || !change.RestartRequired {
		t.Errorf("a running process cannot grow an environment: %v, %+v", err, change)
	}
	for name, call := range map[string]error{
		"start": host.Start(context.Background(), "w", nil),
		"stop":  host.Stop(context.Background(), "w"),
		"rm":    host.Remove(context.Background(), "w"),
	} {
		if call != nil {
			t.Errorf("%s on the host engine = %v", name, call)
		}
	}
}

// An environment-delivered secret is the engine's answer, not this package's: the manager asks the
// engine and reports what it says, and with no engine there is nothing in a guest to substitute,
// so the refusal is the safe one.
func TestTheManagerReportsWhatTheEngineCanDo(t *testing.T) {
	driver := &capabilityDriver{}
	manager := openWith(t, driver)
	if manager.SecretDelivery() != DeliveryEnvironment {
		t.Error("the engine said environment")
	}
	if !manager.SecretEnvInWorker() {
		t.Error("the engine said the worker's own environment is where this belongs")
	}
	if got := manager.Nesting(context.Background()); !got.Available || got.Reason != "the CPU has it" {
		t.Errorf("nesting = %+v, want the engine's answer passed through", got)
	}

	withoutDriver := &Manager{}
	if withoutDriver.SecretDelivery() != DeliverySubstitute {
		t.Error("with no engine there is no guest to put a value in")
	}
	if withoutDriver.SecretEnvInWorker() {
		t.Error("with no engine there is no worker environment to write")
	}
}

// A manager that has recorded why sandboxes cannot run refuses to change secrets, and never asks
// the engine: the answer would be about a sandbox that does not exist.
func TestSecretsAreRefusedWhenSandboxesCannotRunHere(t *testing.T) {
	driver := &capabilityDriver{}
	manager := openWith(t, driver)
	manager.problem = errors.New("no hypervisor")
	if _, err := manager.SecretsChanged(context.Background(), "w", []Secret{{Name: "K", Value: "v"}}); err == nil {
		t.Fatal("a machine that cannot sandbox should refuse the change")
	}
	if driver.ran("secrets") {
		t.Error("the engine should not be asked about a sandbox that cannot run")
	}

	manager.problem = nil
	if _, err := manager.SecretsChanged(context.Background(), "w", nil); err != nil {
		t.Fatal(err)
	}
	if !driver.ran("secrets") {
		t.Error("the engine should be asked once the machine can sandbox")
	}
}

// capabilityDriver is the engine the capability questions are asked of: its answers are the
// interesting ones, so they are not the zero values the other fake returns.
type capabilityDriver struct {
	fakeDriver
}

func (c *capabilityDriver) SecretDelivery() Delivery { return DeliveryEnvironment }
func (c *capabilityDriver) SecretEnvInWorker() bool  { return true }
func (c *capabilityDriver) Nesting(context.Context) NestingSupport {
	return NestingSupport{Available: true, Reason: "the CPU has it"}
}
func (c *capabilityDriver) SecretsChanged(context.Context, string, []Secret) (SecretChange, error) {
	c.calls = append(c.calls, "secrets")
	return SecretChange{}, nil
}
