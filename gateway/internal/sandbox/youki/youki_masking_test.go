package youki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// maskProbe is the three things the policy file states for a leaf, read from inside: the
// capability set, whether /proc/kcore is a masked device, and whether /proc/sys is a mount of
// its own. The runtime applies the file; this is the file's promise observed.
const maskProbe = `printf 'caps:%s' "$(grep '^CapEff:' /proc/self/status | tr -d '\t' | cut -d: -f2)"; ` +
	`printf ' kcore:%s' "$(test -c /proc/kcore && echo masked || echo visible)"; ` +
	`printf ' sys:%s' "$(grep -q ' /proc/sys ' /proc/mounts && echo masked || echo open)"`

// A leaf is a workspace that must not nest: no capability at all, the /proc/kcore mask and the
// /proc/sys read-only mount kept. This is the hardening R8 asks the record to state.
func TestYoukiLeafSandboxKeepsItsMasks(t *testing.T) {
	driver, ctx := liveYouki(t)
	workspace := filepath.Join(t.TempDir(), "leaf")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	policy := sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim", DisableVMNesting: true}
	if err := driver.Create(ctx, policy, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	output, code := runInSupervisor(t, config.Socket, maskProbe)
	if code != 0 {
		t.Fatalf("the mask probe did not run: %q (exit %d)", output, code)
	}
	for _, want := range []string{"caps:0000000000000000", "kcore:masked", "sys:masked"} {
		if !strings.Contains(output, want) {
			t.Errorf("a leaf should have %q, got %q", want, output)
		}
	}
}

// A container that may nest keeps the /proc masks off and CAP_SETFCAP: the inner youki's
// procfs mount needs the path, and the inner uid map needs the capability (§2 rules 1 and 2).
func TestYoukiNestingSandboxCarriesSetfcapAndUnmaskedProc(t *testing.T) {
	driver, ctx := liveYouki(t)
	workspace := filepath.Join(t.TempDir(), "nesting")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	policy := sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim"}
	if err := driver.Create(ctx, policy, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	output, code := runInSupervisor(t, config.Socket, maskProbe)
	if code != 0 {
		t.Fatalf("the mask probe did not run: %q (exit %d)", output, code)
	}
	for _, want := range []string{"caps:0000000080000000", "kcore:visible", "sys:open"} {
		if !strings.Contains(output, want) {
			t.Errorf("a nesting container should have %q, got %q", want, output)
		}
	}
}

// The rest of the policy file, observed: the device grant, the rlimit, and a global resource
// folder bound read-only at its own path. The record is what a person reads; the runtime is
// what applies it.
func TestYoukiPolicyFileIsApplied(t *testing.T) {
	driver, ctx := liveYouki(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "readonly.txt"), []byte("from the host"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "policy")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	policy := sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim", Devices: true, Global: []string{global}}
	if err := driver.Create(ctx, policy, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	command := fmt.Sprintf(`printf 'nofile:%%s:%%s' "$(ulimit -Sn)" "$(ulimit -Hn)"; `+
		`printf ' ro:%%s' "$(cat %s/readonly.txt 2>/dev/null || echo unreadable)"; `+
		`printf ' rw:%%s' "$(echo x > %s/readonly.txt 2>/dev/null && echo writable || echo readonly)"`,
		global, global)
	for _, node := range config.DeviceNodes {
		command += fmt.Sprintf(`; test -c %s && printf ' dev:%s'`, node.Path, node.Path)
	}
	output, code := runInSupervisor(t, config.Socket, command)
	if code != 0 {
		t.Fatalf("the policy probe did not run: %q (exit %d)", output, code)
	}
	for _, want := range []string{"nofile:1024:524288", "ro:from the host", "rw:readonly"} {
		if !strings.Contains(output, want) {
			t.Errorf("the policy file should give %q, got %q", want, output)
		}
	}
	if len(config.DeviceNodes) == 0 {
		t.Fatal("the host has no device to grant, so this test cannot decide the device rule")
	}
	for _, node := range config.DeviceNodes {
		if !strings.Contains(output, "dev:"+node.Path) {
			t.Errorf("the device grant should put %s in the sandbox, got %q", node.Path, output)
		}
	}
}
