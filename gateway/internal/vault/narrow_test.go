package vault

import (
	"os"
	"strings"
	"testing"
)

// Rotating a value is the path that replaces a secret without touching the rest of the entry, and
// it has its own size check: a rotation is how a value arrives when nobody is typing it, so a
// value over the limit has to be refused here too rather than written and refused by the load.
func TestARotationIsMeasuredLikeAnUpsert(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.Upsert(Upsert{Name: "TOKEN", Scope: Global, Description: "d", Value: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rotate(Global, "", "TOKEN", strings.Repeat("v", maxValueBytes)); err != nil {
		t.Fatalf("a value at the limit should rotate: %v", err)
	}
	if _, err := store.Rotate(Global, "", "TOKEN", strings.Repeat("v", maxValueBytes+1)); err == nil {
		t.Fatal("a value over the limit should be refused")
	}
}

// A rotate keeps what it was not given: a value given without a host list keeps the hosts, which
// is what makes a rotate a rotate rather than a replace. An empty list is not the same as no list
// — it says "this secret may reach nowhere" — so the two are told apart.
func TestARotateKeepsTheHostsItWasNotGiven(t *testing.T) {
	store := openTestStore(t)
	hosts := []string{"api.example.com"}
	if _, err := store.Upsert(Upsert{Name: "TOKEN", Scope: Global, Description: "d", Value: "v", Hosts: &hosts}); err != nil {
		t.Fatal(err)
	}
	entry, err := store.Rotate(Global, "", "TOKEN", "rotated")
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Hosts) != 1 || entry.Hosts[0] != "api.example.com" {
		t.Errorf("a rotate without hosts kept %v", entry.Hosts)
	}

	empty := []string{}
	if _, err := store.Upsert(Upsert{Name: "TOKEN", Scope: Global, Description: "d", Hosts: &empty}); err != nil {
		t.Fatal(err)
	}
	entry, err = store.Rotate(Global, "", "TOKEN", "rotated again")
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Hosts) != 0 {
		t.Errorf("an explicit empty list means nowhere, kept %v", entry.Hosts)
	}
}

// A workspace entry replaces a global one of the same name, and the interface says so — but only
// when both the scope and the name are the same. Two workspaces may each have a TOKEN, and
// neither replaces the other, or the screen would claim an override that does not happen.
func TestAnOverrideIsOnlyWhenTheNameAndTheGlobalEntryMatch(t *testing.T) {
	store := openTestStore(t)
	for _, in := range []Upsert{
		{Name: "TOKEN", Scope: Global, Description: "d", Value: "global"},
		{Name: "OTHER", Scope: Workspace, Workspace: "w1", Description: "d", Value: "own"},
		{Name: "TOKEN", Scope: Workspace, Workspace: "w2", Description: "d", Value: "theirs"},
	} {
		if _, err := store.Upsert(in); err != nil {
			t.Fatalf("%s/%s: %v", in.Scope, in.Name, err)
		}
	}
	resolved, err := store.ForWorkspace("w1")
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range append(resolved.Global, resolved.Workspace...) {
		if meta.Name == "OTHER" && meta.Overrides {
			t.Error("a secret with its own name does not override anything")
		}
		if meta.Name == "TOKEN" && meta.Scope == Workspace {
			t.Error("w1 has no workspace TOKEN yet")
		}
	}
	// Another workspace's secret is not in this workspace's list at all.
	for _, meta := range resolved.Workspace {
		if meta.Workspace == "w2" {
			t.Errorf("w2's secret should not appear in w1's list: %+v", meta)
		}
	}

	// The same name in the workspace scope does replace the global one, and the list says so.
	if _, err := store.Upsert(Upsert{Name: "TOKEN", Scope: Workspace, Workspace: "w1", Description: "d", Value: "own"}); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.ForWorkspace("w1")
	if err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, meta := range resolved.Workspace {
		if meta.Name == "TOKEN" {
			marked = meta.Overrides
		}
	}
	if !marked {
		t.Error("a workspace TOKEN replaces the global one, and the list has to say so")
	}
}

// The host and label limits are at the numbers they say, and the alphabet is exactly lower-case
// ASCII, digits, and the hyphen: one character outside it is a homograph or a typo, and both are
// refused. The boundaries are the point — 'a', 'z', '0' and '9' are inside.
func TestTheHostShapeIsCheckedAtItsEdges(t *testing.T) {
	// A host of 253 bytes has to be several labels: one label is capped at 63, so the total is
	// reached only across dots, which is the case a per-label check alone would miss.
	atLimit := strings.Repeat("a", 63) + "." + strings.Repeat("a", 63) + "." +
		strings.Repeat("a", 63) + "." + strings.Repeat("a", 61)
	if len(atLimit) != maxHostBytes {
		t.Fatalf("the fixture is %d bytes, want %d", len(atLimit), maxHostBytes)
	}
	if err := validateHost(atLimit); err != nil {
		t.Errorf("a host at %d bytes should be allowed: %v", maxHostBytes, err)
	}
	if err := validateHost(atLimit + "a"); err == nil {
		t.Error("a host over the limit should be refused")
	}

	label := strings.Repeat("a", maxLabelBytes)
	if err := validateHost(label); err != nil {
		t.Errorf("a label at %d bytes should be allowed: %v", maxLabelBytes, err)
	}
	if err := validateHost(strings.Repeat("a", maxLabelBytes+1)); err == nil {
		t.Error("a label over the limit should be refused")
	}

	for _, good := range []string{"a", "z", "0", "9", "a-z", "9a", "a1z0", "x.y"} {
		if err := validateHost(good); err != nil {
			t.Errorf("validateHost(%q) = %v, want allowed", good, err)
		}
	}
	for _, bad := range []string{"A", "Z", "é", "a_b", "a b", "a@b", "-a", "a-"} {
		if err := validateHost(bad); err == nil {
			t.Errorf("validateHost(%q) should be refused", bad)
		}
	}
}

// A lock file that cannot be opened is an error, and it is returned before anything is read: a
// store that answered a question while another process held the file would be the race the lock
// exists to prevent.
func TestAStoreWhoseLockCannotBeOpenedRefuses(t *testing.T) {
	path := t.TempDir() + "/secrets.json"
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A folder where the lock file has to go: opening it fails even as root.
	if err := os.MkdirAll(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("a store that cannot take its lock should refuse rather than read")
	}
}

// markOverride is the rule the screen shows: an entry replaces one of the same name at a wider
// scope, and nothing else counts. A workspace entry beside itself is not an override — that would
// claim a replacement that does not happen.
func TestOnlyAWiderEntryOfTheSameNameIsAnOverride(t *testing.T) {
	own := Meta{Name: "TOKEN", Scope: Workspace, Workspace: "w1"}
	entries := []Entry{
		{Name: "OTHER", Scope: Global},
		{Name: "TOKEN", Scope: Workspace, Workspace: "w1"},
		{Name: "TOKEN", Scope: Workspace, Workspace: "w2"},
	}
	markOverride(entries, &own)
	if own.Overrides {
		t.Error("a same-scope entry of the same name is not an override")
	}

	withGlobal := append(entries, Entry{Name: "TOKEN", Scope: Global})
	markOverride(withGlobal, &own)
	if !own.Overrides {
		t.Error("a global entry of the same name is the override")
	}

	// A global entry is never marked: there is nothing wider to replace it with.
	global := Meta{Name: "TOKEN", Scope: Global}
	markOverride(withGlobal, &global)
	if global.Overrides {
		t.Error("a global entry overrides nothing")
	}
}
