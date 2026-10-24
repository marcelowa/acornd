package update

import (
	"encoding/json"
	"os"
	"testing"
)

// What "latest" means. An update that picks the wrong release is a downgrade nobody chose, or a
// prerelease installed on a machine that asked for stable — so the ordering is a rule, not a
// presentational detail, and every comparison here is one somebody could get wrong.

func TestTagsOrderTheWayAPersonReadsThem(t *testing.T) {
	// Pairs where the first is newer. Ordering is not string order: v0.10.0 is newer than
	// v0.9.0, which sorting the text would get wrong.
	newer := [][2]string{
		{"v1.0.0", "v0.9.0"},
		{"v0.10.0", "v0.9.0"},
		{"v1.2.3", "v1.2.2"},
		{"v2.0.0", "v1.99.99"},
		{"v1.0.0", "v1.0.0-rc.1"},
		{"v1.0.0-rc.2", "v1.0.0-rc.1"},
		{"v1.0.0-rc.1.1", "v1.0.0-rc.1"},
		{"v1.0.0-beta", "v1.0.0-alpha"},
	}
	for _, pair := range newer {
		if got := compareTags(pair[0], pair[1]); got <= 0 {
			t.Errorf("compareTags(%q, %q) = %d, want the first to be newer", pair[0], pair[1], got)
		}
		if got := compareTags(pair[1], pair[0]); got >= 0 {
			t.Errorf("compareTags(%q, %q) = %d, want the second to be older", pair[1], pair[0], got)
		}
	}
	same := [][2]string{
		{"v1.0.0", "v1.0.0"},
		{"1.0.0", "v1.0.0"},
		{"v1.0.0-rc.1", "v1.0.0-rc.1"},
	}
	for _, pair := range same {
		if got := compareTags(pair[0], pair[1]); got != 0 {
			t.Errorf("compareTags(%q, %q) = %d, want equal", pair[0], pair[1], got)
		}
	}
}

func TestATagThatIsNotAVersionStillOrders(t *testing.T) {
	// A tag nobody can parse falls back to text: the point is that it answers rather than
	// panicking, and that two of them still order consistently.
	for _, pair := range [][2]string{
		{"nightly", "main"},
		{"v1.0", "v1.0.0"},
		{"build-7", "build-6"},
	} {
		first := compareTags(pair[0], pair[1])
		second := compareTags(pair[1], pair[0])
		if first != -second {
			t.Errorf("compareTags(%q,%q)=%d and the reverse=%d should be opposites", pair[0], pair[1], first, second)
		}
	}
}

func TestAPrereleaseOrdersItsParts(t *testing.T) {
	// The parts are compared as numbers where they are numbers, and as text where they are not,
	// which is what makes rc.10 after rc.9 rather than before it.
	cases := []struct {
		a, b string
		want int
	}{
		{"rc.10", "rc.9", 1},
		{"rc.1", "rc.1.1", -1},
		{"rc.1", "rc.1", 0},
		{"beta", "alpha", 1},
		{"1", "alpha", -1},
	}
	for _, test := range cases {
		if got := sign(comparePrerelease(test.a, test.b)); got != test.want {
			t.Errorf("comparePrerelease(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

// A stable tag and its own release candidate: the plain one is newer, which is what keeps a
// machine on stable from being moved onto an rc with the same version numbers.
func TestAStableReleaseBeatsItsPrerelease(t *testing.T) {
	if got := compareParsed([]int{1, 2, 3}, "", []int{1, 2, 3}, "rc.1"); got <= 0 {
		t.Errorf("stable should beat its prerelease, got %d", got)
	}
	if got := compareParsed([]int{1, 2, 3}, "rc.1", []int{1, 2, 3}, ""); got >= 0 {
		t.Errorf("a prerelease should lose to stable, got %d", got)
	}
	if got := compareParsed([]int{1, 2, 3}, "", []int{1, 2, 3}, ""); got != 0 {
		t.Errorf("equal versions should compare equal, got %d", got)
	}
	// The numbers come first: a higher version with a prerelease is still newer than a lower
	// stable one, so a major bump is never held back by its own rc.
	if got := compareParsed([]int{2, 0, 0}, "rc.1", []int{1, 9, 9}, ""); got <= 0 {
		t.Errorf("2.0.0-rc.1 is newer than 1.9.9, got %d", got)
	}
}

// The state files are the install's memory between runs. A missing or unreadable one is a fresh
// install, not a crash — which is the difference between upgrading a machine and breaking it.
func TestTheStateSurvivesAMissingOrBrokenFile(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, "")
	if err := m.Open(); err != nil {
		t.Fatalf("a fresh install should open: %v", err)
	}
	if got := m.Status().Current; got == "" {
		t.Error("a fresh install still reports a version: dev")
	}

	// A settings file that is not JSON is ignored rather than fatal, and the same for the other
	// two: a half-written file from a crash must not stop the next start.
	for _, path := range []string{m.settingsPath(), m.versionPath(), m.failedPath()} {
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Open(); err != nil {
		t.Fatalf("unreadable state should be ignored, not fatal: %v", err)
	}
}

// A settings file that is readable is read: the channel and the check time come back.
func TestSettingsAreRemembered(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, "")
	if err := m.Open(); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"channel": "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.settingsPath(), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := New(dir, "")
	if err := reopened.Open(); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Settings().Channel; got != "beta" {
		t.Errorf("channel = %q, want beta", got)
	}
}
