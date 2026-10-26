package people

import (
	"errors"
	"os"
	"path/filepath"
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
