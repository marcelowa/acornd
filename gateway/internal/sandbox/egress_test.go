package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// What a microVM workspace may reach is written into the engine's own arguments, for every case.
//
// The engine's implicit default is public-only and carries a DNS allowance that disappears the
// moment any rule is written, so relying on it is wrong twice: nothing of ours says "public only",
// and an engine release that changed the default would open every microVM workspace without a line
// of this code changing. The three cases are therefore asserted as arguments, which is what the
// engine reads.

func TestTheEgressPolicyIsWrittenForEveryCase(t *testing.T) {
	cases := []struct {
		why    string
		policy Policy
		want   []string
		absent []string
	}{
		{
			why:    "egress off says nothing: the rendered config is `network: none`, which is a whole policy",
			policy: Policy{Workspace: "/w", Egress: false, PrivateEgress: true},
			want:   nil,
		},
		{
			why:    "egress on with no grant allows public and DNS, and the default is deny",
			policy: Policy{Workspace: "/w", Egress: true},
			want:   []string{"--net-default", "deny", "--net-rule", "allow@public", "--net-rule", "allow@dns"},
			absent: []string{"allow@private"},
		},
		{
			why:    "egress on with the grant adds private and keeps the rest",
			policy: Policy{Workspace: "/w", Egress: true, PrivateEgress: true},
			want: []string{
				"--net-default", "deny",
				"--net-rule", "allow@public",
				"--net-rule", "allow@dns",
				"--net-rule", "allow@private",
			},
		},
	}
	for _, one := range cases {
		args := networkArgs(one.policy)
		for index, want := range one.want {
			if index >= len(args) || args[index] != want {
				t.Errorf("%s: args = %v, want it to start %v", one.why, args, one.want)
				break
			}
		}
		if len(args) != len(one.want) {
			t.Errorf("%s: args = %v, want %v", one.why, args, one.want)
		}
		for _, absent := range one.absent {
			if strings.Contains(strings.Join(args, " "), absent) {
				t.Errorf("%s: %q should not be there: %v", one.why, absent, args)
			}
		}
	}
}

// The engine is told on create, which is where the sandbox's network is built: a grant applied at
// start would be a policy the guest already booted without.
func TestCreatePassesTheEgressPolicyToTheEngine(t *testing.T) {
	for _, one := range []struct {
		why  string
		priv bool
		says string
	}{
		{"a workspace with no grant refuses private networks by default", false, "allow@dns"},
		{"a workspace with the grant is allowed them", true, "allow@private"},
	} {
		runner := &recordingRunner{}
		// The engine's sockets need a short state folder, which is the rule Create enforces first.
		short, err := os.MkdirTemp("", "sb")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(short) })
		engine := &Microsandbox{runner: runner, state: short, installed: true}
		policy := Policy{Workspace: t.TempDir(), Egress: true, PrivateEgress: one.priv}
		if err := engine.Create(context.Background(), policy, t.TempDir()); err != nil {
			t.Fatalf("%s: %v", one.why, err)
		}
		argv := runner.argv()
		if !strings.Contains(argv, "--net-default deny") {
			t.Errorf("%s: the default should be deny, got %q", one.why, argv)
		}
		if !strings.Contains(argv, "allow@public") {
			t.Errorf("%s: public should be allowed, got %q", one.why, argv)
		}
		if !strings.Contains(argv, one.says) {
			t.Errorf("%s: %q is missing from %q", one.why, one.says, argv)
		}
		if one.priv != strings.Contains(argv, "allow@private") {
			t.Errorf("%s: the grant is in the wrong state: %q", one.why, argv)
		}
	}
}

// The rendered config says `network: none` for a workspace with egress off, and says nothing about
// the network otherwise: the policy lives in the arguments, where it is one decision.
func TestTheRenderedConfigLeavesTheNetworkToTheArguments(t *testing.T) {
	off := string(Policy{Workspace: "/w", Egress: false}.Render())
	if !strings.Contains(off, "network: none") {
		t.Errorf("egress off should render network: none:\n%s", off)
	}
	on := string(Policy{Workspace: "/w", Egress: true}.Render())
	if strings.Contains(on, "network:") {
		t.Errorf("egress on should leave the network to the arguments:\n%s", on)
	}
	_ = filepath.Join
}

// recordingRunner keeps the argv the engine was called with.
type recordingRunner struct {
	mu    sync.Mutex
	args  []string
	calls int
}

func (r *recordingRunner) Run(_ context.Context, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.args = args
	return "", nil
}

func (r *recordingRunner) argv() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.args, " ")
}
