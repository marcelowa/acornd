// Package people is who may use this install: one record per person, exactly one admin, and the rule
// that nobody the admin has not named reaches the process at all. See
// docs/adr/0032-who-may-call-the-console.md.
//
// The file is the authority and the door only enforces it. A second list — an Access policy edited by
// hand, a group somebody added an address to — is a second place to forget a revocation, so the door's
// list is derived from this one rather than maintained beside it.
package people

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// File is the book inside the install folder. ADR 0014 keeps the install folder out of every sandbox
// mount, which is what makes an admin's list of people a file an agent cannot read.
const File = "people.json"

// Role is what a record may do at the install level. There are two, deliberately: a viewer or an
// operator is a sharing model nobody has asked for, and a role added before its case exists is a
// permission nobody has thought about (ADR 0032 decision 5).
type Role string

const (
	// RoleAdmin is the install's admin: lifecycle, credentials, policy. Deliberately not a member of
	// anyone else's workspace.
	RoleAdmin Role = "admin"
	// RoleMember is everybody else: their own personal workspace, and the rooms they were added to.
	RoleMember Role = "member"
)

// State is where a record is in its life. There is no pending: a stranger never gets a record, and an
// address the admin named becomes active on its first sign-in.
type State string

const (
	// Invited is an address the admin added that has not signed in yet.
	Invited State = "invited"
	// Active is someone who has signed in.
	Active State = "active"
	// Revoked is someone the admin removed. Every request from them refuses from the next one.
	Revoked State = "revoked"
)

// Person is one record. Email is the identity: it is what the door proves, so it is what everything
// downstream keys on.
type Person struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	Role  Role   `json:"role"`
	State State  `json:"state"`
	// Models is what this person may run. Empty means any model, which is the admin's default.
	Models []string `json:"models,omitempty"`
	// CanCreate is whether they may create workspaces of their own.
	CanCreate bool `json:"can_create,omitempty"`
	// Created and LastSeen are dates, not timestamps: the question they answer is "did this happen at
	// all", and a clock in a file two processes write is a merge nobody needs.
	Created  string `json:"created,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
}

// The refusals. Each is a sentence the person reads, because the people behind this file are family
// and friends rather than operators with a log to check.
var (
	ErrNoAdmin  = errors.New("this install has no admin yet: run `acornfall people claim you@example.com` on the machine that runs it")
	ErrHasAdmin = errors.New("this install already has an admin")
	ErrExists   = errors.New("that address is already in the list")
)

// ErrUnknown is the refusal for an address the door proved and the book does not know.
type ErrUnknown struct{ Email string }

func (e ErrUnknown) Error() string {
	return fmt.Sprintf("this install has no record of %s: ask the admin to add you", e.Email)
}

// ErrRevoked is the refusal for someone the admin removed. It says so rather than pretending the
// address is unknown, because being told "ask again" after being removed is a lie.
type ErrRevoked struct{ Email string }

func (e ErrRevoked) Error() string {
	return fmt.Sprintf("%s no longer has access to this install: ask the admin", e.Email)
}

// DisplayName is what a shared room puts in front of this person's words: the name the admin gave, or the part of
// the address before the @ when they gave none. It is never empty for a person that exists.
func (p Person) DisplayName() string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	if local, _, found := strings.Cut(p.Email, "@"); found && strings.TrimSpace(local) != "" {
		return local
	}
	return p.Email
}

// Allows reports whether this person may run a model, named as "provider/modelId".
//
// An empty list is "any model this install has", which is the admin's default and what a person has until the
// admin narrows it. The list is a proxy for cost rather than a ceiling — a cheap model in a runaway loop still
// spends — which is why spend is shown per person and a cap is a separate decision (ADR 0032 §6).
func (p Person) Allows(provider, model string) bool {
	if len(p.Models) == 0 {
		return true
	}
	want := modelKey(provider, model)
	for _, allowed := range p.Models {
		named, id, found := strings.Cut(allowed, "/")
		if !found {
			// A list entry without a provider names nothing this install can run, and guessing the provider
			// would be a permission nobody granted.
			continue
		}
		if modelKey(named, id) == want {
			return true
		}
	}
	return false
}

// modelKey is one model's name, whatever spaces somebody left around its two halves.
func modelKey(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(model))
}

// ModelsText is what the refusal quotes: the models this person may run, as they were written down.
func (p Person) ModelsText() string {
	return strings.Join(p.Models, ", ")
}

// Book is the file. The zero value is not usable: Open sets the path.
type Book struct {
	path string
	// mu serialises this process's writers. Two processes are serialised by the lock file beside it,
	// because the command line and a running gateway both write here (ADR 0032 §3).
	mu sync.Mutex
	// Now is the clock, so a test can write a date without waiting for one. Nil means time.Now.
	Now func() time.Time
}

// document is the file's shape: one key, so a version can be added without guessing what came before.
type document struct {
	People []Person `json:"people"`
}

// Open reads a book, or starts an empty one when the file is not there. An install with no book is the
// single admin ADR 0020 describes rather than an error: the file appears the first time somebody is
// named.
func Open(path string) (*Book, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("people: a book needs a path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	return &Book{path: abs}, nil
}

// List answers every record, admin first and then by address, so two runs of the same book print the
// same list.
func (b *Book) List() ([]Person, error) {
	records, err := b.read()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Role != records[j].Role {
			return records[i].Role == RoleAdmin
		}
		return records[i].Email < records[j].Email
	})
	return records, nil
}

// Admin answers the install's admin.
func (b *Book) Admin() (Person, bool, error) {
	records, err := b.read()
	if err != nil {
		return Person{}, false, err
	}
	for _, record := range records {
		if record.Role == RoleAdmin {
			return record, true, nil
		}
	}
	return Person{}, false, nil
}

// Admitted is who may come in: everybody the admin has named and not revoked, in the order the book holds them.
// It is what the door's group is derived from, on every apply, which is what makes the book the only allowlist
// there is (ADR 0033 §3).
func (b *Book) Admitted() ([]string, error) {
	records, err := b.List()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(records))
	for _, record := range records {
		if record.State == Revoked {
			continue
		}
		out = append(out, record.Email)
	}
	return out, nil
}

// Find answers one record by address, because a door that proved somebody has to be answered about rather than
// looked up by scanning a list.
// promote is Claim's decision, apart from the file: the address keeps the record it already had, becomes the
// admin, and any twin of it is dropped. Two records for one address make "who is this" a question about line
// order, and an admin whose first line says "person" is locked out of their own install.
func promote(records []Person, address, name, today string) (Person, []Person, error) {
	var claimed Person
	found := false
	kept := make([]Person, 0, len(records)+1)
	for _, record := range records {
		if record.Role == RoleAdmin && record.Email != address {
			return Person{}, nil, ErrHasAdmin
		}
		if record.Email != address {
			kept = append(kept, record)
			continue
		}
		if !found {
			claimed, found = record, true
		}
	}
	claimed.Email = address
	claimed.Role = RoleAdmin
	claimed.State = Active
	if name != "" {
		claimed.Name = name
	}
	if claimed.Created == "" {
		claimed.Created = today
	}
	return claimed, append(kept, claimed), nil
}

func (b *Book) Find(email string) (Person, bool, error) {
	address, err := address(email)
	if err != nil {
		return Person{}, false, err
	}
	records, err := b.read()
	if err != nil {
		return Person{}, false, err
	}
	var found Person
	matched := false
	for _, record := range records {
		if record.Email != address {
			continue
		}
		// An address should appear once, and Claim is what keeps it that way. A book written before that was
		// true can still hold two records for one address, and the answer then must not depend on line order:
		// the strongest record wins, because the door proved the address and the admin is the same person.
		if !matched || (record.Role == RoleAdmin && found.Role != RoleAdmin) {
			found, matched = record, true
		}
	}
	return found, matched, nil
}

// Claim names the install's admin, once. It is a command-line operation on purpose: the token already
// proves ownership, so there is no first-come race through the API and no self-promotion (ADR 0032 §3).
func (b *Book) Claim(email, name string) (Person, error) {
	address, err := address(email)
	if err != nil {
		return Person{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	release, records, err := b.lock()
	if err != nil {
		return Person{}, err
	}
	defer release()
	claimed, kept, err := promote(records, address, strings.TrimSpace(name), b.today())
	if err != nil {
		return Person{}, err
	}
	if err := b.write(kept); err != nil {
		return Person{}, err
	}
	return claimed, nil
}

// Add names a person. They are invited until they first sign in, so a typo in an address costs nothing
// and nobody holds a workspace they never visited.
func (b *Book) Add(email, name string) (Person, error) {
	address, err := address(email)
	if err != nil {
		return Person{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	release, records, err := b.lock()
	if err != nil {
		return Person{}, err
	}
	defer release()
	for _, record := range records {
		if record.Email == address {
			return Person{}, fmt.Errorf("%w: %s", ErrExists, address)
		}
	}
	added := Person{
		Email:   address,
		Name:    strings.TrimSpace(name),
		Role:    RoleMember,
		State:   Invited,
		Created: b.today(),
	}
	if err := b.write(append(records, added)); err != nil {
		return Person{}, err
	}
	return added, nil
}

// Revoke takes access away. The record stays: "who was here" is a question the admin asks later, and a
// deletion answers it wrong. Their workspace is not touched — ADR 0011 keeps removal a separate,
// explicit command.
func (b *Book) Revoke(email string) error {
	address, err := address(email)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	release, records, err := b.lock()
	if err != nil {
		return err
	}
	defer release()
	for index := range records {
		if records[index].Email != address {
			continue
		}
		if records[index].Role == RoleAdmin {
			// Revoking the admin is how an install locks itself out of the door it needs to add anybody,
			// and there is no recovery command that does not start with the token.
			return errors.New("the admin cannot be revoked: there would be no one left to add anybody")
		}
		records[index].State = Revoked
		return b.write(records)
	}
	return ErrUnknown{Email: address}
}

// SetModels narrows what somebody may run. An empty list is every model this install has, which is where a
// person starts; the admin is never narrowed, because the install is theirs.
func (b *Book) SetModels(email string, models []string) (Person, error) {
	address, err := address(email)
	if err != nil {
		return Person{}, err
	}
	cleaned, err := modelList(models)
	if err != nil {
		return Person{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	release, records, err := b.lock()
	if err != nil {
		return Person{}, err
	}
	defer release()
	for index := range records {
		if records[index].Email != address {
			continue
		}
		if records[index].Role == RoleAdmin {
			return Person{}, errors.New("the admin runs any model this install has: there is nobody to narrow it")
		}
		records[index].Models = cleaned
		if err := b.write(records); err != nil {
			return Person{}, err
		}
		return records[index], nil
	}
	return Person{}, ErrUnknown{Email: address}
}

// modelList is a list of models as the book stores it: "provider/modelId", provider lowercased, and every
// entry refused rather than dropped, because a list with a typo in it is a permission nobody meant to grant.
func modelList(models []string) ([]string, error) {
	cleaned := make([]string, 0, len(models))
	for _, model := range models {
		provider, id, found := strings.Cut(strings.TrimSpace(model), "/")
		provider, id = strings.TrimSpace(provider), strings.TrimSpace(id)
		if !found || provider == "" || id == "" {
			return nil, fmt.Errorf("%q is not a model: write it as provider/modelId", model)
		}
		cleaned = append(cleaned, strings.ToLower(provider)+"/"+id)
	}
	return cleaned, nil
}

// SignIn is the door's question, asked after it proved an address: may this person in, and it counts as
// a visit. An invited record becomes active here, which is the moment their personal workspace is
// created (ADR 0032 §3), so the answer carries the record the caller needs.
func (b *Book) SignIn(email string) (Person, error) {
	address, err := address(email)
	if err != nil {
		return Person{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	release, records, err := b.lock()
	if err != nil {
		return Person{}, err
	}
	defer release()
	for index := range records {
		if records[index].Email != address {
			continue
		}
		if records[index].State == Revoked {
			return Person{}, ErrRevoked{Email: address}
		}
		records[index].State = Active
		records[index].LastSeen = b.today()
		if err := b.write(records); err != nil {
			return Person{}, err
		}
		return records[index], nil
	}
	return Person{}, ErrUnknown{Email: address}
}

// address normalises an address, because an identity compared case-sensitively admits one person twice.
func address(email string) (string, error) {
	address := strings.ToLower(strings.TrimSpace(email))
	local, domain, found := strings.Cut(address, "@")
	// Enough of an address to catch the typo a person makes, and no more: a full RFC parser here would
	// refuse an address the door has already proved, which is the one thing this must never do.
	if !found || local == "" || domain == "" || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("%q is not an email address", email)
	}
	return address, nil
}

func (b *Book) today() string {
	if b.Now != nil {
		return b.Now().Format("2006-01-02")
	}
	return time.Now().Format("2006-01-02")
}

// read answers the document and its records under a shared lock, so a read never sees a half-written
// file and never blocks a reader.
func (b *Book) read() ([]Person, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	release, records, err := b.lock()
	if err != nil {
		return nil, err
	}
	defer release()
	return records, nil
}

// load reads and parses the file. The caller holds the lock.
func (b *Book) load() ([]Person, error) {
	payload, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var parsed document
	if err := json.Unmarshal(payload, &parsed); err != nil {
		// A file nobody can parse is reported by name: rewriting it would delete the list, and starting
		// empty would admit nobody while looking like it worked.
		return nil, fmt.Errorf("%s is not a people file: %w", b.path, err)
	}
	// A book written before the roles were renamed says "owner" and "person". They are the same two
	// roles under their old names, and a book is a file people keep: they are read as the new ones and
	// the next write puts the new names back (ADR 0034).
	for i := range parsed.People {
		switch parsed.People[i].Role {
		case RoleAdmin, RoleMember:
			// Already the current names.
		case "owner":
			parsed.People[i].Role = RoleAdmin
		case "person":
			parsed.People[i].Role = RoleMember
		}
	}
	return parsed.People, nil
}

// write replaces the file atomically with 0600, which is the vault's discipline for the same reason: a
// half-written list is a list of the wrong people, and one world-readable moment is a leak.
func (b *Book) write(records []Person) error {
	if records == nil {
		records = []Person{}
	}
	payload, err := json.MarshalIndent(document{People: records}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temp, err := os.CreateTemp(filepath.Dir(b.path), ".people-*")
	if err != nil {
		return err
	}
	path := temp.Name()
	defer func() { _ = os.Remove(path) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(path, b.path)
}

// lock is held across the read-modify-write of one change, so the command line and a running gateway
// cannot lose each other's edit. The lock file holds nothing; it exists to be locked.
// The wait for another process is bounded on purpose. Flock is released when its holder ends, so what a caller
// waits for is a write in progress, and that is a moment; a wait that outlives this budget means the other
// process is wedged, and being told so beats hanging — by the command line, and by every test run, where a hang
// costs the whole timeout to learn nothing.
const (
	lockBudget = 2 * time.Second
	lockRetry  = 25 * time.Millisecond
)

func (b *Book) lock() (func(), []Person, error) {
	file, err := os.OpenFile(b.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := waitFor(file); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	records, err := b.load()
	release := func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return release, records, nil
}

// waitFor is the exclusive lock, asked for without blocking and retried until the budget is spent.
func waitFor(file *os.File) error {
	deadline := time.Now().Add(lockBudget)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("another acornfall process has been writing the book of people for over %s: try again", lockBudget)
		}
		time.Sleep(lockRetry)
	}
}
