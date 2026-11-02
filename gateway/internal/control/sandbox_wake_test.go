package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
	"github.com/marcelowa/acornd/gateway/internal/sandbox"
	"github.com/marcelowa/acornd/gateway/internal/vault"
)

// wakeDriver is a substitute engine with one stopped sandbox. It embeds the host engine for
// everything a wake does not touch, so the test can assert the one fact that matters: control
// starts the sandbox with the vault's values before the worker is told to attach.
type wakeDriver struct {
	*sandbox.Host
	workspace string
	starts    []wakeStart
	startErr  error
}

type wakeStart struct {
	name    string
	secrets []sandbox.Secret
}

func (d *wakeDriver) Name() string { return "microsandbox" }
func (d *wakeDriver) List(context.Context) ([]sandbox.Instance, error) {
	return []sandbox.Instance{{Name: sandbox.Name(d.workspace), Status: "stopped"}}, nil
}
func (d *wakeDriver) Start(_ context.Context, name string, secrets []sandbox.Secret) error {
	d.starts = append(d.starts, wakeStart{name: name, secrets: secrets})
	return d.startErr
}
func (d *wakeDriver) SecretDelivery() sandbox.Delivery { return sandbox.DeliverySubstitute }
func (d *wakeDriver) SecretEnvInWorker() bool          { return false }
func (d *wakeDriver) SecretsChanged(context.Context, string, []sandbox.Secret) (sandbox.SecretChange, error) {
	return sandbox.SecretChange{}, nil
}

func wakeControl(t *testing.T, driver *wakeDriver, worker *fakeWorker) *Control {
	t.Helper()
	manager, err := sandbox.Open(context.Background(), sandbox.Options{
		DataDir: t.TempDir(),
		Driver:  driver,
	})
	if err != nil {
		t.Fatal(err)
	}
	control := controlWithWorker(t, worker)
	control.opts.Workspace.Path = driver.workspace
	control.opts.Sandbox = manager
	return control
}

func TestAWakeStartsTheSandboxWithTheVaultsValues(t *testing.T) {
	workspace := t.TempDir()
	driver := &wakeDriver{workspace: workspace}
	worker := newFakeWorker()
	control := wakeControl(t, driver, worker)
	hosts := []string{"api.github.com"}
	control.opts.Vault = vaultWith(t, vault.Upsert{
		Name: "GITHUB_PAT", Scope: vault.Global, Description: "the token",
		Value: "ghp-value", Hosts: &hosts,
	})

	control.answerSandboxWake(protocol.Frame{Type: "sandbox_wake", ID: "wake-1"})

	if len(driver.starts) != 1 {
		t.Fatalf("a stopped sandbox should be started once, got %d: %+v", len(driver.starts), driver.starts)
	}
	start := driver.starts[0]
	if start.name != sandbox.Name(workspace) {
		t.Errorf("started %q, want %q", start.name, sandbox.Name(workspace))
	}
	if len(start.secrets) != 1 || start.secrets[0].Name != "GITHUB_PAT" || start.secrets[0].Value != "ghp-value" {
		t.Fatalf("the start should carry the vault's value, got %+v", start.secrets)
	}

	answer, ok := wakeAnswer(worker)
	if !ok {
		t.Fatal("the worker should be told to attach after the start")
	}
	if answer.ReplyTo != "wake-1" {
		t.Errorf("the answer should name the wake it answers, got %q", answer.ReplyTo)
	}
	if answer.Error != "" {
		t.Errorf("a successful start should answer with no error, got %q", answer.Error)
	}
	if strings.Contains(fmt.Sprintf("%+v", answer), "ghp-value") {
		t.Fatal("the answer to the worker must not carry the value")
	}
}

func TestAWakeThatCannotStartSaysWhyAndRunsNothing(t *testing.T) {
	driver := &wakeDriver{workspace: t.TempDir(), startErr: errors.New("the engine refused the start")}
	worker := newFakeWorker()
	control := wakeControl(t, driver, worker)
	hosts := []string{"api.github.com"}
	control.opts.Vault = vaultWith(t, vault.Upsert{
		Name: "GITHUB_PAT", Scope: vault.Global, Description: "the token",
		Value: "ghp-value", Hosts: &hosts,
	})

	control.answerSandboxWake(protocol.Frame{Type: "sandbox_wake", ID: "wake-2"})

	if len(driver.starts) != 1 {
		t.Fatalf("the one start should be attempted, got %d", len(driver.starts))
	}
	answer, ok := wakeAnswer(worker)
	if !ok {
		t.Fatal("a failed wake should still answer the worker")
	}
	if !strings.Contains(answer.Error, "the engine refused the start") {
		t.Errorf("the answer should carry the reason, got %q", answer.Error)
	}
}

// wakeAnswer is the command control sent in reply to the wake, if any.
func wakeAnswer(worker *fakeWorker) (protocol.Command, bool) {
	for _, command := range worker.asked() {
		if command.Type == "sandbox_wake" {
			return command, true
		}
	}
	return protocol.Command{}, false
}

func TestASandboxWakeFrameIsAnsweredOffTheReadLoop(t *testing.T) {
	driver := &wakeDriver{workspace: t.TempDir()}
	worker := newFakeWorker()
	control := wakeControl(t, driver, worker)
	hosts := []string{"api.github.com"}
	control.opts.Vault = vaultWith(t, vault.Upsert{
		Name: "GITHUB_PAT", Scope: vault.Global, Description: "the token",
		Value: "ghp-value", Hosts: &hosts,
	})

	control.onFrame(protocol.Frame{Type: "sandbox_wake", ID: "wake-frame"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if answer, ok := wakeAnswer(worker); ok {
			if answer.ReplyTo != "wake-frame" {
				t.Fatalf("answered %q, want wake-frame", answer.ReplyTo)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the wake frame was not answered")
}
