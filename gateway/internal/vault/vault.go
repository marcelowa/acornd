// Package vault stores the secrets a command needs: values the agent may use but must
// never see in its context, in a transcript, or in a workspace. See
// docs/adr/0021-a-vault-for-tool-secrets.md.
//
// The store is install-wide and deliberately boring: one JSON file beside
// credentials.json, mode 0600, written atomically under a lock that the CLI and a running
// gateway share. It knows nothing about an engine. A sandbox driver asks for a workspace's
// effective entries and decides how to deliver them.
package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Scope says where a secret lives. A global secret is on every workspace; a workspace
// secret is on one, and wins when both define the same name.
type Scope string

const (
	Global    Scope = "global"
	Workspace Scope = "workspace"
)

// Substitution says where in an HTTP request a placeholder may be replaced. Headers are
// on by default because that is where most API credentials travel.
type Substitution struct {
	Headers bool `json:"headers"`
	Query   bool `json:"query"`
	Body    bool `json:"body"`
}

func (s Substitution) empty() bool { return !s.Headers && !s.Query && !s.Body }

// Entry is one stored secret, value included. It is never marshaled to a client.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       Scope  `json:"scope"`
	// Workspace is the absolute folder for a workspace secret; empty for a global one.
	Workspace    string       `json:"workspace,omitempty"`
	Value        string       `json:"value"`
	Hosts        []string     `json:"hosts,omitempty"`
	Substitution Substitution `json:"substitution"`
	AllowEnv     bool         `json:"allowEnv"`
	RotatedAt    time.Time    `json:"rotatedAt"`
}

// Meta is an entry without its value. The API and the interface only ever see this.
type Meta struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Scope        Scope        `json:"scope"`
	Workspace    string       `json:"workspace,omitempty"`
	Hosts        []string     `json:"hosts,omitempty"`
	Substitution Substitution `json:"substitution"`
	AllowEnv     bool         `json:"allowEnv"`
	RotatedAt    time.Time    `json:"rotatedAt"`
	// Overrides is true on a workspace entry whose name also exists globally.
	Overrides bool `json:"overrides,omitempty"`
	// Overridden is true on a global entry that a workspace entry replaced.
	Overridden bool `json:"overridden,omitempty"`
	// Source names the scope that won in an effective list.
	Source Scope `json:"source,omitempty"`
}

// Meta is the redacted form.
func (e Entry) Meta() Meta {
	hosts := append([]string(nil), e.Hosts...)
	return Meta{
		Name:         e.Name,
		Description:  e.Description,
		Scope:        e.Scope,
		Workspace:    e.Workspace,
		Hosts:        hosts,
		Substitution: e.Substitution,
		AllowEnv:     e.AllowEnv,
		RotatedAt:    e.RotatedAt,
	}
}

// Resolved is one workspace's view: the global entries, its own, and what the sandbox would
// receive. A global entry replaced by a workspace one carries Overridden; the workspace entry
// carries Overrides.
type Resolved struct {
	Global    []Meta `json:"global"`
	Workspace []Meta `json:"workspace"`
	Effective []Meta `json:"effective"`
}

// Upsert creates a secret or changes one. Empty Description, Value, Hosts, Substitution, or
// AllowEnv mean "keep what is there" on an update; on a create, Description and Value are
// required and the omitted fields take their defaults.
type Upsert struct {
	Name         string
	Description  string
	Scope        Scope
	Workspace    string
	Value        string
	Hosts        *[]string
	Substitution *Substitution
	AllowEnv     *bool
}

// Store is one secrets file. Safe for concurrent use within the process; a lock file
// serializes it against the CLI and any other process.
type Store struct {
	path string
	mu   sync.Mutex
}

// document is the on-disk form. The version is what lets encryption wrap the values later
// without guessing what an old file means.
type document struct {
	Version int     `json:"version"`
	Secrets []Entry `json:"secrets"`
}

const fileVersion = 1

// ErrNotFound is a rotate or delete for a secret that is not there.
var ErrNotFound = errors.New("secret not found")

// InvalidError is a caller's mistake: a name, a scope, a host, or a missing field. The API
// answers 400 for it and the CLI prints it; it is never a file or a lock failure.
type InvalidError struct{ message string }

func (e InvalidError) Error() string { return e.message }

func invalid(message string) error { return InvalidError{message: message} }

func invalidf(format string, args ...any) error {
	return InvalidError{message: fmt.Sprintf(format, args...)}
}

// Open resolves the file and makes sure its folder is private. It creates no secret and
// does not require the file to exist.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("vault: a store needs a path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	return &Store{path: abs}, nil
}

// Path is where the store keeps its file, for doctor and tests.
func (s *Store) Path() string { return s.path }

// List returns every entry's metadata, global and workspace, sorted by name.
func (s *Store) List() ([]Meta, error) {
	doc, err := s.read()
	if err != nil {
		return nil, err
	}
	metas := make([]Meta, 0, len(doc.Secrets))
	for _, entry := range doc.Secrets {
		metas = append(metas, entry.Meta())
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Name != metas[j].Name {
			return metas[i].Name < metas[j].Name
		}
		return metas[i].Scope < metas[j].Scope
	})
	return metas, nil
}

// Global returns the install-wide entries.
func (s *Store) Global() ([]Meta, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	global := make([]Meta, 0, len(all))
	for _, meta := range all {
		if meta.Scope == Global {
			global = append(global, meta)
		}
	}
	return global, nil
}

// ForWorkspace resolves what one folder receives. The workspace entries are its own; the
// effective list is the global set with any same-named workspace entry replacing it.
func (s *Store) ForWorkspace(workspace string) (Resolved, error) {
	workspace, err := workspacePath(workspace)
	if err != nil {
		return Resolved{}, err
	}
	doc, err := s.read()
	if err != nil {
		return Resolved{}, err
	}
	global := []Meta{}
	own := []Meta{}
	globalNames := map[string]bool{}
	ownNames := map[string]bool{}
	for _, entry := range doc.Secrets {
		meta := entry.Meta()
		switch entry.Scope {
		case Global:
			globalNames[meta.Name] = true
			global = append(global, meta)
		case Workspace:
			if entry.Workspace == workspace {
				ownNames[meta.Name] = true
				own = append(own, meta)
			}
		}
	}
	for i := range global {
		global[i].Overridden = ownNames[global[i].Name]
	}
	for i := range own {
		own[i].Overrides = globalNames[own[i].Name]
	}
	effective := make([]Meta, 0, len(global)+len(own))
	for _, meta := range global {
		if meta.Overridden {
			continue
		}
		meta.Source = Global
		effective = append(effective, meta)
	}
	for _, meta := range own {
		meta.Source = Workspace
		effective = append(effective, meta)
	}
	sortByName(global)
	sortByName(own)
	sortByName(effective)
	return Resolved{Global: global, Workspace: own, Effective: effective}, nil
}

// Resolve returns the entries a sandbox for this workspace receives, values included. It is
// the one method that hands a value to a caller; everything the API and the model see uses
// Meta.
func (s *Store) Resolve(workspace string) ([]Entry, error) {
	workspace, err := workspacePath(workspace)
	if err != nil {
		return nil, err
	}
	doc, err := s.read()
	if err != nil {
		return nil, err
	}
	resolved := map[string]Entry{}
	for _, entry := range doc.Secrets {
		if entry.Scope == Global {
			resolved[entry.Name] = entry
		}
	}
	for _, entry := range doc.Secrets {
		if entry.Scope == Workspace && entry.Workspace == workspace {
			resolved[entry.Name] = entry
		}
	}
	entries := make([]Entry, 0, len(resolved))
	for _, entry := range resolved {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Upsert creates or changes an entry and returns its metadata.
func (s *Store) Upsert(in Upsert) (Meta, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	workspace, err := normalizeWorkspace(in.Scope, in.Workspace)
	if err != nil {
		return Meta{}, err
	}
	in.Workspace = workspace
	if err := validateName(in.Name); err != nil {
		return Meta{}, err
	}
	if err := validateScope(in.Scope, in.Workspace); err != nil {
		return Meta{}, err
	}
	if in.Description != "" && len(in.Description) > maxDescBytes {
		return Meta{}, invalidf("description is longer than %d bytes", maxDescBytes)
	}
	if len(in.Value) > maxValueBytes {
		return Meta{}, invalidf("value is longer than %d bytes", maxValueBytes)
	}
	var hosts []string
	if in.Hosts != nil {
		hosts, err = normalizeHosts(*in.Hosts)
		if err != nil {
			return Meta{}, err
		}
	}
	var meta Meta
	err = s.update(func(doc *document) error {
		index := find(doc.Secrets, in.Scope, in.Workspace, in.Name)
		now := time.Now().UTC()
		if index < 0 {
			if in.Description == "" {
				return invalid("a new secret needs a description")
			}
			if in.Value == "" {
				return invalid("a new secret needs a value")
			}
			entry := Entry{
				Name:        in.Name,
				Description: in.Description,
				Scope:       in.Scope,
				Workspace:   in.Workspace,
				Value:       in.Value,
				Hosts:       hosts,
				RotatedAt:   now,
			}
			if in.Substitution == nil {
				entry.Substitution = Substitution{Headers: true}
			} else {
				entry.Substitution = *in.Substitution
				if entry.Substitution.empty() {
					return invalid("a secret needs at least one substitution location")
				}
			}
			if in.AllowEnv == nil {
				entry.AllowEnv = true
			} else {
				entry.AllowEnv = *in.AllowEnv
			}
			doc.Secrets = append(doc.Secrets, entry)
			meta = entry.Meta()
			markOverride(doc.Secrets, &meta)
			return nil
		}
		entry := doc.Secrets[index]
		if in.Description != "" {
			entry.Description = in.Description
		}
		if in.Value != "" {
			entry.Value = in.Value
			entry.RotatedAt = now
		}
		if in.Hosts != nil {
			entry.Hosts = hosts
		}
		if in.Substitution != nil {
			if in.Substitution.empty() {
				return invalid("a secret needs at least one substitution location")
			}
			entry.Substitution = *in.Substitution
		}
		if in.AllowEnv != nil {
			entry.AllowEnv = *in.AllowEnv
		}
		doc.Secrets[index] = entry
		meta = entry.Meta()
		markOverride(doc.Secrets, &meta)
		return nil
	})
	if err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Rotate replaces a value and its timestamp.
func (s *Store) Rotate(scope Scope, workspace, name, value string) (Meta, error) {
	name = strings.TrimSpace(name)
	workspace, err := normalizeWorkspace(scope, workspace)
	if err != nil {
		return Meta{}, err
	}
	if err := validateName(name); err != nil {
		return Meta{}, err
	}
	if err := validateScope(scope, workspace); err != nil {
		return Meta{}, err
	}
	if value == "" {
		return Meta{}, invalid("a rotated secret needs a value")
	}
	if len(value) > maxValueBytes {
		return Meta{}, invalidf("value is longer than %d bytes", maxValueBytes)
	}
	var meta Meta
	err = s.update(func(doc *document) error {
		index := find(doc.Secrets, scope, workspace, name)
		if index < 0 {
			return ErrNotFound
		}
		doc.Secrets[index].Value = value
		doc.Secrets[index].RotatedAt = time.Now().UTC()
		meta = doc.Secrets[index].Meta()
		markOverride(doc.Secrets, &meta)
		return nil
	})
	if err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Delete removes one entry.
func (s *Store) Delete(scope Scope, workspace, name string) error {
	name = strings.TrimSpace(name)
	workspace, err := normalizeWorkspace(scope, workspace)
	if err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateScope(scope, workspace); err != nil {
		return err
	}
	return s.update(func(doc *document) error {
		index := find(doc.Secrets, scope, workspace, name)
		if index < 0 {
			return ErrNotFound
		}
		doc.Secrets = append(doc.Secrets[:index], doc.Secrets[index+1:]...)
		return nil
	})
}

// read takes a shared lock, so a read never sees a file being renamed under it and a CLI
// write waits for a gateway read to finish.
func (s *Store) read() (document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock(false)
	if err != nil {
		return document{}, err
	}
	defer unlock(lock)
	return s.load()
}

// update takes the exclusive lock, reads the current file, applies the change, and writes
// the whole document. Nothing holds a value after the call returns except the caller.
func (s *Store) update(change func(*document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock(true)
	if err != nil {
		return err
	}
	defer unlock(lock)
	doc, err := s.load()
	if err != nil {
		return err
	}
	if err := change(&doc); err != nil {
		return err
	}
	return s.save(doc)
}

func (s *Store) load() (document, error) {
	payload, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return document{Version: fileVersion}, nil
	}
	if err != nil {
		return document{}, err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return document{Version: fileVersion}, nil
	}
	var doc document
	if err := json.Unmarshal(payload, &doc); err != nil {
		return document{}, fmt.Errorf("%s is not valid JSON: %w", s.path, err)
	}
	if doc.Version != fileVersion {
		return document{}, fmt.Errorf("%s has version %d; this build reads version %d", s.path, doc.Version, fileVersion)
	}
	for _, entry := range doc.Secrets {
		if err := validateName(entry.Name); err != nil {
			return document{}, fmt.Errorf("%s: %w", s.path, err)
		}
		if err := validateScope(entry.Scope, entry.Workspace); err != nil {
			return document{}, fmt.Errorf("%s: %w", s.path, err)
		}
	}
	return doc, nil
}

func (s *Store) save(doc document) error {
	doc.Version = fileVersion
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".secrets-*")
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
	return os.Rename(path, s.path)
}

// lock creates the lock file beside the store, so two processes never read-modify-write
// the same file over each other. The lock file itself holds nothing.
func (s *Store) lock(exclusive bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), how); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func unlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

// markOverride says whether a workspace entry has a global entry of the same name to
// replace. A global entry's Overridden flag needs a workspace, so ForWorkspace sets it.
func markOverride(entries []Entry, meta *Meta) {
	if meta.Scope != Workspace {
		return
	}
	for _, entry := range entries {
		if entry.Scope == Global && entry.Name == meta.Name {
			meta.Overrides = true
			return
		}
	}
}

func find(entries []Entry, scope Scope, workspace, name string) int {
	for i, entry := range entries {
		if entry.Scope == scope && entry.Workspace == workspace && entry.Name == name {
			return i
		}
	}
	return -1
}

func sortByName(metas []Meta) {
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
}

const (
	maxNameBytes  = 128
	maxValueBytes = 64 * 1024
	maxDescBytes  = 500
)

var namePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

var reservedNames = map[string]bool{
	"PATH": true, "HOME": true, "SHELL": true, "TERM": true, "USER": true,
	"HOSTNAME": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "TMPDIR": true,
	"LANG": true, "IFS": true, "_": true,
}

func validateName(name string) error {
	if name == "" {
		return invalid("a secret needs a name")
	}
	if len(name) > maxNameBytes {
		return invalidf("name is longer than %d bytes", maxNameBytes)
	}
	if !namePattern.MatchString(name) {
		return invalidf("name %q must be an uppercase environment variable: A-Z, 0-9, and _", name)
	}
	if reservedNames[name] || strings.HasPrefix(name, "MSB_") || strings.HasPrefix(name, "ACORNFALL_") {
		return invalidf("name %q is reserved by the sandbox or the host", name)
	}
	return nil
}

func validateScope(scope Scope, workspace string) error {
	switch scope {
	case Global:
		if workspace != "" {
			return invalid("a global secret cannot name a workspace")
		}
	case Workspace:
		if workspace == "" {
			return invalid("a workspace secret needs a workspace folder")
		}
		if !filepath.IsAbs(workspace) {
			return invalid("a workspace folder must be an absolute path")
		}
	default:
		return invalidf("unknown secret scope %q", scope)
	}
	return nil
}

func normalizeWorkspace(scope Scope, workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if scope == Global {
		if workspace != "" {
			return "", invalid("a global secret cannot name a workspace")
		}
		return "", nil
	}
	if scope != Workspace {
		return workspace, nil
	}
	if workspace == "" {
		return "", invalid("a workspace secret needs a workspace folder")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func workspacePath(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", invalid("a workspace folder is required")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func normalizeHosts(hosts []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, raw := range hosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			continue
		}
		if err := validateHost(host); err != nil {
			return nil, err
		}
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out, nil
}

func validateHost(host string) error {
	if host == "*" {
		return invalid("a secret cannot allow every host; name the hosts it may reach")
	}
	if len(host) > 253 {
		return invalidf("host %q is too long", host)
	}
	if strings.Contains(host, "..") {
		return invalidf("host %q has an empty label", host)
	}
	label := host
	if strings.HasPrefix(label, "*.") {
		label = strings.TrimPrefix(label, "*.")
		if label == "" {
			return invalid("a wildcard needs a domain after *.")
		}
	} else if strings.Contains(label, "*") {
		return invalidf("host %q may use a wildcard only as a leading *.", host)
	}
	for _, part := range strings.Split(label, ".") {
		if part == "" || len(part) > 63 {
			return invalidf("host %q has an invalid label", host)
		}
		if strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return invalidf("host %q has a label starting or ending with -", host)
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return invalidf("host %q has a character a hostname cannot have", host)
			}
		}
	}
	return nil
}
