package main

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/remote"
)

// logs captures what the standard logger writes, which is where a boot says what it did.
func logs(t *testing.T, run func()) string {
	t.Helper()
	var buffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(previous)
	run()
	return buffer.String()
}

// An install with no door in front of it has nothing to reconcile, and a boot that talks to Cloudflare anyway
// would be a single-admin install reaching for an account it never named.
func TestReconcileDoesNothingWithoutADoor(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "")
	t.Setenv("ACORNFALL_CLOUDFLARE_TOKEN", "not-used")
	out := logs(t, func() { reconcileRemote(context.Background(), t.TempDir()) })
	if out != "" {
		t.Errorf("an install with no door said %q", out)
	}
}

// With a door and no API token, the boot says so once and carries on: the door still works, and the person who
// can fix it is the person reading the log.
func TestReconcileSaysItCannotWithoutAnAPIToken(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "access")
	t.Setenv("ACORNFALL_CLOUDFLARE_TOKEN", "")
	dir := t.TempDir()
	if err := remote.Save(dir, remote.Setup{
		Desired: remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
		State:   remote.State{TunnelID: "t"},
	}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	out := logs(t, func() { reconcileRemote(context.Background(), dir) })
	if !strings.Contains(out, "no API token") {
		t.Errorf("the boot did not say why it skipped the reconcile: %q", out)
	}
}

// An install that never ran an apply has no declared state, so the boot has nothing to match and says nothing:
// the first apply is the admin's command, not something a boot invents.
func TestReconcileDoesNothingBeforeAnApply(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "access")
	t.Setenv("ACORNFALL_CLOUDFLARE_TOKEN", "present-but-unused")
	out := logs(t, func() { reconcileRemote(context.Background(), t.TempDir()) })
	if out != "" {
		t.Errorf("an install with no apply said %q", out)
	}
}

// The boot log gets one line, and it says what changed rather than how much did not.
func TestSummariseSaysWhatChanged(t *testing.T) {
	cases := []struct {
		name  string
		steps []remote.Step
		want  string
	}{
		{"nothing", []remote.Step{{Name: "tunnel"}, {Name: "dns"}}, "nothing to change"},
		{"created", []remote.Step{{Name: "tunnel", Created: true}, {Name: "dns"}}, "1 created"},
		{"moved", []remote.Step{{Name: "dns", Moved: true}, {Name: "tunnel"}}, "1 moved"},
		{"both", []remote.Step{{Name: "tunnel", Created: true}, {Name: "dns", Moved: true}}, "1 created, 1 moved"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := summarise(test.steps); got != test.want {
				t.Errorf("summarise = %q, want %q", got, test.want)
			}
		})
	}
}

// The book and the desire come from the install folder, and a missing book is a refusal rather than an empty
// allowlist: an apply that admits nobody is the half-setup remote apply already refuses to make.
func TestReconcileRefusesAnInstallWithNobodyInIt(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "access")
	t.Setenv("ACORNFALL_CLOUDFLARE_TOKEN", "present-but-unused")
	dir := t.TempDir()
	if err := remote.Save(dir, remote.Setup{
		Desired: remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
	}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	out := logs(t, func() { reconcileRemote(context.Background(), dir) })
	if !strings.Contains(out, "not reconciled") {
		t.Errorf("an install whose book is empty was reconciled anyway: %q", out)
	}
}
