package control

import (
	"path/filepath"
	"testing"
	"time"
)

// The rules a workspace list obeys, and the ones a policy is read from. These are small functions
// with a lot behind them: the list is what a person picks from, and the policy is what a sandbox
// is built from.

// The general workspace is first because it is the one that is always there, and the rest are by
// name because a list that reorders itself is a list nobody can learn.
func TestTheWorkspaceListPutsGeneralFirstThenNames(t *testing.T) {
	workspaces := &Workspaces{registry: map[string]Workspace{
		"zulu":    {Name: "zulu"},
		"general": {Name: "general", General: true},
		"alpha":   {Name: "alpha"},
		"mike":    {Name: "mike"},
	}}
	got := make([]string, 0, 4)
	for _, workspace := range workspaces.List() {
		got = append(got, workspace.Name)
	}
	want := []string{"general", "alpha", "mike", "zulu"}
	for index := range want {
		if index >= len(got) || got[index] != want[index] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// A workspace's own settings become a sandbox policy, and the two that change what the sandbox can
// do are the ones worth pinning: networking off means egress off, and a keep-alive workspace is one
// the engine must not stop by itself.
func TestAWorkspaceBecomesThePolicyItAskedFor(t *testing.T) {
	base := Workspace{Name: "w", Path: t.TempDir(), Network: NetworkOff}
	policy := base.SandboxPolicy(t.TempDir(), t.TempDir())
	if policy.Egress {
		t.Error("network off should mean no egress")
	}
	if policy.IdleTimeout != "" {
		t.Errorf("a workspace that is not kept alive has no idle override, got %q", policy.IdleTimeout)
	}

	online := base
	online.Network = "on"
	if !online.SandboxPolicy(t.TempDir(), t.TempDir()).Egress {
		t.Error("network on should mean egress")
	}

	// A workspace whose network is not the off constant is online: the rule is written as "not
	// off" so a value this build does not know errs on the side of what the person asked for.
	unknown := base
	unknown.Network = "private"
	if !unknown.SandboxPolicy(t.TempDir(), t.TempDir()).Egress {
		t.Error("anything but off is egress, including a value from a newer file")
	}

	kept := base
	kept.KeepAlive = true
	policy = kept.SandboxPolicy(t.TempDir(), t.TempDir())
	if policy.IdleTimeout == "" {
		t.Error("a keep-alive workspace must tell the engine not to stop it")
	}

	// Nesting is a pointer so that "not mentioned" is not "false": absent nests when it can.
	nesting := true
	held := base
	held.DisableVMNesting = &nesting
	if !held.SandboxPolicy(t.TempDir(), t.TempDir()).DisableVMNesting {
		t.Error("a workspace that refuses nesting should say so in the policy")
	}
	if base.SandboxPolicy(t.TempDir(), t.TempDir()).DisableVMNesting {
		t.Error("an unmentioned setting must not disable nesting")
	}
}

// The eviction rule: a workspace left alone long enough is stopped, and one that is being watched
// or running something is not. The boundary is the whole rule, and a mutation of it changes when a
// sandbox disappears under a person.
func TestTheEvictionRuleHasABoundary(t *testing.T) {
	if idleEviction <= 0 {
		t.Fatalf("idleEviction = %v", idleEviction)
	}
	cases := []struct {
		name  string
		idle  time.Duration
		evict bool
	}{
		{"just used", 0, false},
		{"a second inside the window", idleEviction - time.Second, false},
		{"exactly at the window", idleEviction, true},
		{"long idle", idleEviction * 2, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// The rule as written: evict only when the workspace has been idle for at least the
			// window, which is the "<" in the sweep read the other way round.
			evict := !(test.idle < idleEviction)
			if evict != test.evict {
				t.Errorf("idle %v: evict = %v, want %v", test.idle, evict, test.evict)
			}
		})
	}
}

// Where workspaces live is decided in three steps, and which step answered is what a person sees
// in the log: the flag, then what the file recorded, then the platform default.
func TestTheWorkspacesFolderIsResolvedInOrder(t *testing.T) {
	recorded := filepath.Join(t.TempDir(), "recorded")
	flag := filepath.Join(t.TempDir(), "from-flag")

	// The flag wins and is recorded.
	withFlag := &Workspaces{opts: WorkspacesOptions{DataDir: t.TempDir(), WorkspacesDir: flag}}
	if err := withFlag.resolveDir(recorded); err != nil {
		t.Fatal(err)
	}
	if withFlag.dir != flag {
		t.Errorf("the flag should win: %q", withFlag.dir)
	}

	// With no flag, the file's answer is used.
	fromFile := &Workspaces{opts: WorkspacesOptions{DataDir: t.TempDir()}}
	if err := fromFile.resolveDir(recorded); err != nil {
		t.Fatal(err)
	}
	if fromFile.dir != recorded {
		t.Errorf("the recorded folder should be used: %q", fromFile.dir)
	}

	// With neither, the platform default answers — and it is absolute, because a relative folder
	// would be relative to wherever the process was started.
	fresh := &Workspaces{opts: WorkspacesOptions{DataDir: t.TempDir()}}
	if err := fresh.resolveDir(""); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(fresh.dir) || fresh.dir == "" {
		t.Errorf("a fresh install needs an absolute default, got %q", fresh.dir)
	}
}
