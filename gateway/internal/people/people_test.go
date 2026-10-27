package people

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func book(t *testing.T) *Book {
	t.Helper()
	opened, err := Open(filepath.Join(t.TempDir(), File))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	opened.Now = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	return opened
}

// An install with no file is the single owner ADR 0020 describes, not an error: the book appears the
// first time somebody is named, and until then nobody is.
func TestAnInstallWithNoBookHasNoPeople(t *testing.T) {
	b := book(t)
	list, err := b.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %v, want none", list)
	}
	if _, ok, err := b.Owner(); err != nil || ok {
		t.Errorf("owner = %v, %v; want nobody and no error", ok, err)
	}
	if _, err := b.SignIn("ana@example.com"); !errors.As(err, &ErrUnknown{}) {
		t.Fatalf("err = %v, want unknown", err)
	}
}

// Exactly one owner, and the command line is what claims it: the token already proves ownership, so
// there is no race and nothing to self-promote through the API.
func TestTheOwnerIsClaimedOnce(t *testing.T) {
	b := book(t)
	owner, err := b.Claim("Me@Example.com", "Me")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if owner.Role != RoleOwner || owner.State != Active {
		t.Errorf("owner = %+v, want an active owner", owner)
	}
	if owner.Email != "me@example.com" {
		t.Errorf("email = %q, want it lowercased", owner.Email)
	}
	if owner.Created != "2026-10-30" {
		t.Errorf("created = %q, want today's date", owner.Created)
	}
	if _, err := b.Claim("someone@example.com", "Someone"); !errors.Is(err, ErrHasOwner) {
		t.Fatalf("err = %v, want ErrHasOwner", err)
	}
}

// A person the owner names is invited until they show up. A typo therefore costs nothing, and nobody
// holds a workspace they never visited.
func TestAnAddedPersonIsInvitedUntilTheySignIn(t *testing.T) {
	b := book(t)
	if _, err := b.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	added, err := b.Add("Ana@Example.com", "Ana")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.State != Invited || added.Role != RolePerson {
		t.Fatalf("added = %+v, want an invited person", added)
	}

	signed, err := b.SignIn("ana@example.com")
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if signed.State != Active {
		t.Errorf("state = %q, want active", signed.State)
	}
	if signed.LastSeen != "2026-10-30" {
		t.Errorf("last seen = %q, want today", signed.LastSeen)
	}
	// And it stays one record whatever case the door proved.
	if _, err := b.Add("ana@example.com", "Ana again"); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

// Revoked is a state, not a deletion: "who was here" is a question the owner asks later, and every
// request from a revoked address refuses from the next one.
func TestRevokingKeepsTheRecordAndRefusesSignIn(t *testing.T) {
	b := book(t)
	if _, err := b.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := b.Revoke("ana@example.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	list, err := b.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v; want the record kept", list, err)
	}
	if list[0].State != Revoked {
		t.Errorf("state = %q, want revoked", list[0].State)
	}
	if _, err := b.SignIn("ana@example.com"); !errors.As(err, &ErrRevoked{}) {
		t.Fatalf("err = %v, want revoked rather than unknown", err)
	}
}

// Revoking the owner would leave nobody able to name anybody, and the only way back in is the token
// that belongs to the record being revoked.
func TestTheOwnerCannotBeRevoked(t *testing.T) {
	b := book(t)
	if _, err := b.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := b.Revoke("me@example.com"); err == nil {
		t.Fatal("the owner was revoked")
	}
	if owner, ok, _ := b.Owner(); !ok || owner.State != Active {
		t.Errorf("owner = %+v, want it intact", owner)
	}
}

// An address that is not an address is refused rather than stored: it would be a record nothing can
// ever prove, and a list that lies about who may get in.
func TestAnAddressThatIsNotOneIsRefused(t *testing.T) {
	b := book(t)
	for _, bad := range []string{"", "   ", "ana", "ana@", "@example.com"} {
		if _, err := b.Add(bad, "x"); err == nil {
			t.Errorf("Add(%q) was accepted", bad)
		}
		if _, err := b.SignIn(bad); err == nil {
			t.Errorf("SignIn(%q) was accepted", bad)
		}
	}
}

// The file is a list of people, so it is 0600 and written whole: a half-written list is a list of the
// wrong people.
func TestTheBookIsWrittenWholeAndUnreadableByOthers(t *testing.T) {
	b := book(t)
	if _, err := b.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	info, err := os.Stat(b.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
	payload, err := os.ReadFile(b.path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(payload), "ana@example.com") {
		t.Errorf("the file does not hold the person: %s", payload)
	}
	// No temporary file survives a write, or a list of people would accumulate in the install folder.
	entries, err := os.ReadDir(filepath.Dir(b.path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".people-") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}

// A file nobody can parse is reported by name rather than replaced: rewriting it would delete the list,
// and starting empty would admit nobody while looking like it worked.
func TestABookThatCannotBeParsedIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := opened.List(); err == nil || !strings.Contains(err.Error(), "not a people file") {
		t.Fatalf("err = %v, want it to name the file", err)
	}
	if _, err := opened.Claim("me@example.com", "Me"); err == nil {
		t.Fatal("a claim replaced a file it could not read")
	}
}

// A book whose file cannot be read — the path is a folder, or the install folder was deleted under a
// running process — is reported rather than treated as empty. "Empty" would admit nobody while looking
// like it worked.
func TestABookThatCannotBeReadIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, File), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	opened, err := Open(filepath.Join(dir, File))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := opened.List(); err == nil {
		t.Fatal("a folder read as a list of people")
	}
}

// A write that cannot land is an error the caller sees: a change reported as done and not stored is a
// person who thinks they added somebody and did not.
func TestAChangeThatCannotBeStoredIsReported(t *testing.T) {
	dir := t.TempDir()
	opened, err := Open(filepath.Join(dir, File))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The lock file is what every change takes first, so a folder in its place is the install becoming
	// unwritable for everybody at once.
	if err := os.Mkdir(filepath.Join(dir, File+".lock"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := opened.Add("ana@example.com", "Ana"); err == nil {
		t.Fatal("a change that could not take the lock reported success")
	}
}

// An install whose folder disappeared under it fails loudly instead of silently minting a new list in
// whatever directory is current.
func TestABookWhoseFolderIsGoneIsReported(t *testing.T) {
	dir := t.TempDir()
	opened, err := Open(filepath.Join(dir, "install", File))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "install")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := opened.Add("ana@example.com", "Ana"); err == nil {
		t.Fatal("a change to a folder that is gone reported success")
	}
}

// The clock is a seam, and with no clock injected the date is today's: the one thing a record cannot do
// is claim to have been created in the future because a test forgot to freeze time.
func TestTheDateIsTodaysWhenNoClockIsInjected(t *testing.T) {
	opened, err := Open(filepath.Join(t.TempDir(), File))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, want := opened.today(), time.Now().Format("2006-01-02"); got != want {
		t.Errorf("today = %q, want %q", got, want)
	}
}

// The refusal is what a family member reads on a page, so it is asserted as a sentence rather than as a
// type: "access denied" for a person who was removed is a support thread.
func TestTheRefusalsSayWhatToDo(t *testing.T) {
	unknown := ErrUnknown{Email: "ana@example.com"}
	if got := unknown.Error(); !strings.Contains(got, "ana@example.com") || !strings.Contains(got, "ask the owner") {
		t.Errorf("unknown = %q, want the address and who to ask", got)
	}
	revoked := ErrRevoked{Email: "ana@example.com"}
	if got := revoked.Error(); !strings.Contains(got, "no longer has access") {
		t.Errorf("revoked = %q, want it to say access was taken away", got)
	}
	b := book(t)
	if err := b.Revoke("nobody@example.com"); !errors.As(err, &ErrUnknown{}) {
		t.Errorf("revoking an address nobody named = %v, want unknown", err)
	}
}

// A book with no path, and one whose folder cannot be created, are both refusals at the boundary: the
// file's location is the caller's decision and a wrong one is a sentence rather than a panic.
func TestABookNeedsAPlaceItCanWrite(t *testing.T) {
	if _, err := Open("   "); err == nil {
		t.Error("a book with no path was opened")
	}
	base := t.TempDir()
	file := filepath.Join(base, "install")
	if err := os.WriteFile(file, []byte("not a folder"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(filepath.Join(file, File)); err == nil {
		t.Error("a book was opened under a file")
	}
}

// The allowlist is "provider/modelId", and an empty list means every model the install has. A person is bound
// by theirs; nothing here decides who the owner is, because the owner has no list at all.
func TestAModelsListIsWhatTheyMayRun(t *testing.T) {
	narrowed := Person{Email: "ana@example.com", Models: []string{"anthropic/claude-sonnet-4", " faux / faux-1 "}}
	cases := []struct {
		name     string
		person   Person
		provider string
		model    string
		want     bool
	}{
		{"a listed model", narrowed, "anthropic", "claude-sonnet-4", true},
		{"a listed model written with spaces", narrowed, "faux", "faux-1", true},
		{"a model they were not given", narrowed, "anthropic", "claude-opus-4", false},
		{"another provider's model of the same name", narrowed, "other", "claude-sonnet-4", false},
		{"every model, because the list is empty", Person{Email: "bo@example.com"}, "anything", "at-all", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.person.Allows(test.provider, test.model); got != test.want {
				t.Errorf("Allows(%q, %q) = %v, want %v", test.provider, test.model, got, test.want)
			}
		})
	}
	if got := narrowed.ModelsText(); !strings.Contains(got, "claude-sonnet-4") || !strings.Contains(got, "faux-1") {
		t.Errorf("ModelsText = %q, want the list a refusal can quote", got)
	}
}

// The owner narrows what somebody may run, and the book says so: a list, written down, that the door can read.
// The owner has no list to narrow, because the install is theirs.
func TestTheOwnerNarrowsWhatSomebodyMayRun(t *testing.T) {
	b := book(t)
	if _, err := b.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := b.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	narrowed, err := b.SetModels("ana@example.com", []string{"faux/faux-1", " Anthropic/Claude-Sonnet-4 "})
	if err != nil {
		t.Fatalf("set models: %v", err)
	}
	if !slices.Equal(narrowed.Models, []string{"faux/faux-1", "anthropic/Claude-Sonnet-4"}) {
		t.Errorf("models = %v, want them normalised", narrowed.Models)
	}
	if _, err := b.SetModels("me@example.com", []string{"faux/faux-1"}); err == nil {
		t.Error("the owner was narrowed")
	}
	if _, err := b.SetModels("ana@example.com", []string{"not-a-model"}); err == nil {
		t.Error("a model without a provider was accepted")
	}
	if _, err := b.SetModels("nobody@example.com", nil); !errors.As(err, &ErrUnknown{}) {
		t.Errorf("err = %v, want unknown", err)
	}
	// An empty list is every model, which is how a narrowing is taken back.
	restored, err := b.SetModels("ana@example.com", nil)
	if err != nil || len(restored.Models) != 0 {
		t.Errorf("models = %v, %v; want the list cleared", restored.Models, err)
	}
}

// Adding somebody and then claiming them is the order the setup screen invites — the list fills the door's
// group, and the owner names themselves after. The second step must promote the record the first one wrote,
// not write a twin beside it: with two records for one address, "who is this" depends on line order, and an
// owner whose first line says "person" is locked out of their own install.
func TestClaimingSomebodyAlreadyInTheBookPromotesThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	opened, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := opened.Add("marcelo@example.com", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	claimed, err := opened.Claim("marcelo@example.com", "")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Role != RoleOwner || claimed.State != Active {
		t.Errorf("claimed record is %q/%q, want owner and active", claimed.Role, claimed.State)
	}
	if records, err := opened.read(); err != nil || len(records) != 1 {
		t.Fatalf("%d records for one address (%v): %+v", len(records), err, records)
	} else if records[0].Role != RoleOwner {
		t.Errorf("the record is %+v, want the same address as owner", records[0])
	}

	// Claiming again is the same answer, not ErrHasOwner: it is the same person, and they may run the command
	// twice without being told their own install belongs to somebody else.
	if _, err := opened.Claim("marcelo@example.com", ""); err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if records, _ := opened.read(); len(records) != 1 {
		t.Errorf("%d records after a second claim", len(records))
	}

	// A name already in the book survives a claim that does not give one: the record is promoted, not
	// replaced, and a command that forgot the name must not erase it.
	named := filepath.Join(t.TempDir(), File)
	other, err := Open(named)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := other.Add("bea@example.com", "Bea"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if claimed, err := other.Claim("bea@example.com", ""); err != nil {
		t.Fatalf("claim: %v", err)
	} else if claimed.Name != "Bea" {
		t.Errorf("the claim erased the name: %q", claimed.Name)
	}

	// A different address is still refused, because that is a different owner.
	if _, err := opened.Claim("ana@example.com", ""); !errors.Is(err, ErrHasOwner) {
		t.Errorf("a second address claimed the install: %v", err)
	}
}

// A book that already holds two records for one address — written before Claim learned to promote — must not
// lock the owner out: the strongest record answers, whatever order the lines are in.
func TestAnAddressWrittenTwiceAnswersAsItsOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	written := `{"people":[
	  {"email":"ana@example.com","role":"person","state":"active","created":"2026-01-01"},
	  {"email":"ana@example.com","role":"owner","state":"active","created":"2026-01-02"}]}`
	if err := os.WriteFile(path, []byte(written), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	person, found, err := opened.Find("ana@example.com")
	if err != nil || !found {
		t.Fatalf("find: %v, found %v", err, found)
	}
	if person.Role != RoleOwner {
		t.Errorf("the answer is %q, want the owner record", person.Role)
	}
	// And claiming heals the file rather than adding to it.
	if _, err := opened.Claim("ana@example.com", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if records, _ := opened.read(); len(records) != 1 || records[0].Role != RoleOwner {
		t.Errorf("the book still holds %+v", records)
	}

	// A duplicate is not the only way to be locked out: the record that answers may be the one with no role
	// written down at all, and an install that predates roles would then have nobody able to administer it.
	path = filepath.Join(t.TempDir(), File)
	written = `{"people":[{"email":"ana@example.com","state":"active"},{"email":"ana@example.com"}]}`
	if err := os.WriteFile(path, []byte(written), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	opened, err = Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if person, _, err := opened.Find("ana@example.com"); err != nil || person.Role == RoleOwner {
		t.Errorf("a roleless record answered as %q (%v)", person.Role, err)
	}
	if _, err := opened.Claim("ana@example.com", ""); err != nil {
		t.Fatalf("claim over roleless records: %v", err)
	}
	if records, _ := opened.read(); len(records) != 1 || records[0].Role != RoleOwner {
		t.Errorf("the roleless book still holds %+v", records)
	}
}
