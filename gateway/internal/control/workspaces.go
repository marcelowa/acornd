// Package control's workspace layer: the install's registry of workspaces and one
// Control per started workspace.
//
// A Control owns one storage, and conversation ids are only unique inside a storage, so
// "which workspace" has to be answered before anything else can be looked up. That is what
// this file does: it routes, it starts workers lazily, and it keeps the registry.
// See docs/adr/0011-workspaces.md.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/vault"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
)

// PersonalWorkspaceName is the install's own workspace: the one the owner lands on when nothing else is
// chosen, the one that cannot be removed, and the name every install has exactly one of. It was called
// "general" because there was one workspace; ADR 0032 §9 renamed it when there was more than one person.
const PersonalWorkspaceName = "personal"

// DefaultWorkspacesDir is where workspaces are created. It is deliberately outside the
// hidden installation folder: this is the reader's own content, and it should be somewhere
// they would look.
//
// An install without a home directory gets an error rather than a relative guess: workspaces
// would otherwise land wherever the process happened to start, which is not a place anyone
// chose and is how a folder of them appears in a container's working directory.
func DefaultWorkspacesDir() (string, error) {
	dataDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot tell where to create workspaces: %w (pass --workspaces-dir)", err)
	}
	return filepath.Join(dataDir, "acornfall", "workspaces"), nil
}

// Workspace is a folder the agent works in, plus the decisions that belong to it.
type Workspace struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Title string `json:"title"`
	// General marks the default workspace. Exactly one workspace is general.
	General bool `json:"general"`
	// Image is the sandbox's environment for this workspace: the project's toolchain, not
	// ours. Empty means the default, and changing it takes effect at the next sandbox
	// because the running one is where the workspace's dependencies live.
	Image string `json:"image,omitempty"`
	// Network is whether the sandbox may reach the network: "on", "off", or empty for the
	// default, which is on. A string rather than a bool so that "not set" is expressible —
	// and so an allowlist can be added later without a migration.
	Network string `json:"network,omitempty"`
	// Private is whether the sandbox may reach private networks: this machine's own LAN,
	// link-local addresses, and any overlay it is part of, such as a tailnet. Off by default:
	// the network a workspace gets is the public internet unless someone said otherwise, because
	// a sandbox that reaches the machine's private networks does so as the machine.
	Private bool `json:"private,omitempty"`
	// KeepAlive keeps this workspace's sandbox running when nothing is watching it. It is for a
	// workspace that runs a daemon or a cron job: stopping an idle sandbox kills its processes,
	// which is fine for a folder and not fine for a server. Off by default, because the point of
	// stopping is that a personal machine is not holding workspaces nobody is using.
	KeepAlive bool `json:"keep_alive,omitempty"`
	// Engine is which sandbox engine this workspace runs on: microsandbox, bwrap, or host.
	// Empty means the install's default, from --sandbox-engine. Like Image, it takes effect
	// at the next sandbox, because a running sandbox is where the dependencies live.
	Engine string `json:"engine,omitempty"`
	// DisableVMNesting is whether this workspace's sandbox must not nest virtual machines:
	// run a hypervisor inside it. Nil means "not set", which follows the install's
	// --disable-vm-nesting; a pointer because true is a choice and absent is not. Like Image,
	// it takes effect at the next sandbox, because nesting is decided when one is created.
	DisableVMNesting *bool `json:"disable_vm_nesting,omitempty"`
	// DisableDevices is whether this bwrap workspace must not receive the host's
	// /dev/net/tun and /dev/kvm. Nil means the default: devices are granted, so a
	// workspace can run another acornfall with its own bwrap engine and network. A
	// pointer because true is a decision and absent is not.
	DisableDevices *bool `json:"disable_devices,omitempty"`
	// Members is who may use this workspace: email addresses, lowercase. Being a member is the whole of it
	// — there are no roles inside one, because ADR 0032 gives two and both are install-level. A workspace
	// with no members belongs to nobody, which is the state every workspace was in before there were
	// people, and which the owner's own claim fills in.
	Members []string `json:"members,omitempty"`
	// Running is runtime state, never stored: a persisted `true` would be a claim about a
	// process that does not exist after a restart.
	Running bool `json:"-"`
}

// ErrNoWorkspace is "there is no workspace here for you": it does not exist, or it is not one you are a
// member of. The edge answers 404 for both, because a status that told them apart would let a person
// enumerate the workspaces they cannot see (ADR 0032 §4).
var ErrNoWorkspace = errors.New("no workspace")

// Caller is who a request is from, in control's own words: the install's owner, or a person. The edge
// translates the proof it obtained into this shape, because ADR 0002 keeps the transport's types out of
// the domain and control is what owns identity, resources and policy (ADR 0001).
type Caller struct {
	Owner bool
	Email string
}

// Allows reports whether a caller may use this workspace.
//
// Being a member is the whole of it: it is not a read where a member writes, and it is not a role. The
// owner is not special here — ADR 0032 has them own the install, not other people's conversations — and
// the one exception is an install where no owner has been claimed, which is the machine talking to
// itself before anybody is named.
func (w Workspace) Allows(caller Caller) bool {
	if strings.TrimSpace(caller.Email) == "" {
		return caller.Owner
	}
	return slices.Contains(w.Members, strings.ToLower(caller.Email))
}

// NetworkOff is the one non-default value Network has today.
const NetworkOff = "off"

// SandboxPolicy is what this workspace's sandbox is allowed to be. It is derived rather than
// stored so that a new field cannot be forgotten in one of the two places. `dataDir` is the
// installation folder and `userHome` is the user's, because the global resources live in two
// places and the worker reads both: the sandbox mounts the same paths at the same strings.
// `disableVMNesting` is the install's own answer, --disable-vm-nesting, and a workspace's
// own setting wins over it.
func (w Workspace) SandboxPolicy(dataDir, userHome string) sandbox.Policy {
	disableVMNesting := false
	if w.DisableVMNesting != nil {
		disableVMNesting = *w.DisableVMNesting
	}
	devices := true
	if w.DisableDevices != nil {
		devices = !*w.DisableDevices
	}
	policy := sandbox.Policy{
		Image:            w.Image,
		Workspace:        w.Path,
		Egress:           w.Network != NetworkOff,
		PrivateEgress:    w.Private,
		DisableVMNesting: disableVMNesting,
		Devices:          devices,
		Global:           sandbox.GlobalPaths(dataDir, userHome),
	}
	if w.KeepAlive {
		// An engine that stops idle sandboxes on its own has to be told not to here: control
		// already exempts this workspace from eviction, and a sandbox stopped under a live worker
		// is woken by the next command — which kills the daemons it was kept alive for.
		policy.IdleTimeout = sandbox.KeptAliveIdleTimeout
	}
	return policy
}

// ValidNetwork reports whether a network value means something. An unknown value is refused
// rather than ignored: a caller asking for "maybe" should be told, not humoured.
func ValidNetwork(value string) bool {
	return value == "" || value == "on" || value == NetworkOff
}

// ValidEngine reports whether a workspace may name an engine. The transport asks, because a bad
// name is a bad request rather than a failed workspace; the rule lives here because control is
// what provisions the engine, and a second copy of "which engines exist" in the HTTP layer is
// how the two drift apart.
func ValidEngine(name string) bool { return sandbox.ValidEngine(name) }

// EngineNames is the list a refusal quotes, in the order the interface offers the choices.
func EngineNames() []string { return sandbox.EngineNames() }

// ValidKeepAlive is a bool on the wire; nothing to validate. It is here so that a caller reading
// the create and update handlers sees the same shape for every workspace setting.

// Views renders workspaces for the interface, which is the only place `running` belongs.
//
// A conversion rather than a field-by-field copy, because the two shapes are the same shape:
// a field added to one and not the other is then a compile error instead of a value that
// quietly stops reaching the interface.
// Views is the wire shape of a workspace list, with the status each one is in. A nil progress
// function means the caller has none to give, and the views then say nothing about it.
func Views(list []Workspace, progress func(string) Progress) []WorkspaceState {
	views := make([]WorkspaceState, 0, len(list))
	for _, workspace := range list {
		view := WorkspaceState{
			Name:             workspace.Name,
			Path:             workspace.Path,
			Title:            workspace.Title,
			General:          workspace.General,
			Image:            workspace.Image,
			Network:          workspace.Network,
			Private:          workspace.Private,
			KeepAlive:        workspace.KeepAlive,
			Engine:           workspace.Engine,
			DisableVMNesting: workspace.DisableVMNesting,
			DisableDevices:   workspace.DisableDevices,
			Running:          workspace.Running,
		}
		if progress != nil {
			state := progress(workspace.Name)
			view.Status = state.Status
			view.Stage = state.Stage
			view.Error = state.Error
		}
		views = append(views, view)
	}
	return views
}

// WorkspacesOptions configures the workspace layer.
type WorkspacesOptions struct {
	// DataDir is the installation folder: global resources, credentials, and state.
	DataDir string
	// UserHome is the user's home folder, where `~/.agents` lives. It is resolved at the
	// entry point so that the sandbox mount list and the worker's reads are built from the
	// same answer.
	UserHome string
	// WorkspacesDir is where workspaces are created, from --workspaces-dir. Empty means
	// the registry's value, and failing that the default.
	WorkspacesDir string
	WorkerCommand []string
	WorkerDir     string
	// Update reports the self-update summary for the state stream.
	Update func() UpdateSummary
	// Model is the last resort, from ACORNFALL_MODEL: below every workspace and below the
	// install's own default, which `acornfall model set` writes.
	Model string
	// Sandbox is the install's default engine, shared by every workspace that does not name
	// one of its own.
	Sandbox *sandbox.Manager
	// Sandboxes holds every engine this install can run, so a workspace that names one gets
	// that engine rather than the default. Nil means this install has one engine only.
	Sandboxes *sandbox.Tier
	// Vault is the install-wide tool-secret store. Nil means this install has none, which
	// is a state the interface reports rather than a reason not to open a workspace.
	Vault *vault.Store
	// DisableVMNesting is the install default from --disable-vm-nesting: a workspace's
	// sandbox nests when the engine and the machine can, unless a workspace refuses it.
	DisableVMNesting bool
	// OwnerEmail is the install owner's address, from the book of people, or empty when nobody has claimed
	// it. It exists for one decision: a workspace that predates people is the owner's, and recording that
	// is what stops turning people on from locking the owner out of everything they had.
	OwnerEmail string
}

// Workspaces is the install: a registry of workspaces and their started Controls.
type Workspaces struct {
	opts WorkspacesOptions

	// mu guards the registry and the controls.
	//
	// Lock order, and it is not a preference: **registry before control, never the
	// reverse.** `List` and the eviction sweep read a control while holding this, so a
	// path that read the registry while holding a control would make two orders out of one
	// pair of locks, and a pending writer on either side would then hang every request
	// that touches both — which is every request. `TestStateDoesNotTakeTheRegistryLockInsideItsOwn`
	// checks the one place that had it backwards.
	mu       sync.RWMutex
	dir      string
	registry map[string]Workspace
	controls map[string]*Control
	progress map[string]Progress
	starting map[string]*sync.Mutex
	closed   bool

	// engines is the machine's answer for the chooser. It is kept because the probe behind it
	// does real work (bubblewrap runs a user namespace) and the answer rarely moves — but it
	// does move while a gateway runs: the microVM engine downloads itself on a workspace's
	// first use, so a workspace start drops the answer instead of serving a stale "not
	// installed" until the next restart.
	enginesMu  sync.Mutex
	engines    []EngineInfo
	enginesSet bool

	// used is guarded by usedMu rather than by mu, because it changes on every request:
	// taking the registry's write lock to store a timestamp would put the hot path in
	// front of everything that reads the registry. It is a leaf lock, never held while
	// taking another.
	usedMu sync.Mutex
	used   map[string]time.Time
}

func NewWorkspaces(opts WorkspacesOptions) *Workspaces {
	return &Workspaces{
		opts:     opts,
		registry: map[string]Workspace{},
		controls: map[string]*Control{},
		used:     map[string]time.Time{},
		starting: map[string]*sync.Mutex{},
	}
}

// Vault is the install-wide secret store, or nil when the gateway was built without one.
func (w *Workspaces) Vault() *vault.Store { return w.opts.Vault }

// Dir is the folder workspaces are created in.
func (w *Workspaces) Dir() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.dir
}

// Open loads the registry, seeding the install's own workspace on first run, and starts it so
// the interface has somewhere to land.
func (w *Workspaces) Open(ctx context.Context) error {
	if err := w.loadRegistry(); err != nil {
		return err
	}
	if err := os.MkdirAll(w.Dir(), 0o755); err != nil {
		return err
	}
	if err := w.migrate(); err != nil {
		return err
	}
	if err := w.nameTheOwnersWorkspaces(); err != nil {
		return err
	}
	if _, err := w.Ensure(ctx, PersonalWorkspaceName); err != nil {
		// Logged, not fatal: the install's own workspace is where a client lands, but a client can
		// still be told what is wrong with it. Exiting here would leave nothing to talk to.
		workerpool.Log("%s workspace: %v", PersonalWorkspaceName, err)
	}
	go w.evictIdle(ctx)
	return nil
}

func (w *Workspaces) Close() {
	w.mu.Lock()
	controls := make([]*Control, 0, len(w.controls))
	for _, control := range w.controls {
		controls = append(controls, control)
	}
	w.controls = map[string]*Control{}
	w.closed = true
	w.mu.Unlock()
	for _, control := range controls {
		control.Close()
	}
}

const (
	registryFile = "workspaces.json"
	// A warm worker costs a process. Keeping every workspace the reader has visited alive
	// forever would turn a few visits into a few hundred megabytes, so an idle one that
	// nobody is watching is stopped and started again from its own storage.
	idleEviction  = 15 * time.Minute
	evictionSweep = time.Minute
)

// List returns the registry, general first and then by name. It includes whether each
// workspace's worker is running, because that is the difference between switching to a
// workspace and waiting for one to start.
func (w *Workspaces) List() []Workspace {
	w.mu.RLock()
	defer w.mu.RUnlock()
	list := make([]Workspace, 0, len(w.registry))
	for _, workspace := range w.registry {
		if control, started := w.controls[workspace.Name]; started {
			workspace.Running = control.WorkerUp()
		}
		list = append(list, workspace)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].General != list[j].General {
			return list[i].General
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// ListFor answers the workspaces a caller may see: every one of them for the owner, and only their own for
// a person. The owner sees the install because they administer it; that is a list of names, not a
// transcript from inside any of them.
func (w *Workspaces) ListFor(caller Caller) []Workspace {
	if caller.Owner {
		return w.List()
	}
	visible := make([]Workspace, 0)
	for _, workspace := range w.List() {
		if workspace.Allows(caller) {
			visible = append(visible, workspace)
		}
	}
	return visible
}

// personal answers the name of the caller's own workspace, making it if they have none.
//
// It is made on the first request rather than at sign-in because that request is the sign-in: nobody gets a
// workspace they never showed up for, and nothing has to remember that the making was owed.
func (w *Workspaces) personal(caller Caller) (string, error) {
	if caller.Owner || strings.TrimSpace(caller.Email) == "" {
		return PersonalWorkspaceName, nil
	}
	// One workspace with exactly one member, that member being this person. It is derived rather than
	// flagged, because a membership list is a fact and a flag is a claim that can disagree with it.
	for _, workspace := range w.List() {
		if len(workspace.Members) == 1 && workspace.Members[0] == strings.ToLower(caller.Email) {
			return workspace.Name, nil
		}
	}
	created, err := w.CreatePersonal(caller.Email)
	if err != nil {
		return "", err
	}
	return created.Name, nil
}

// CreatePersonal makes somebody's own workspace: named after their address, one member, in the workspaces
// folder like any other.
//
// The name is theirs and the console never shows it — the label is "Personal" — but a name has to be unique
// in one registry, so a second Ana becomes ana-2 rather than somebody's workspace going missing.
func (w *Workspaces) CreatePersonal(email string) (Workspace, error) {
	address := strings.ToLower(strings.TrimSpace(email))
	if address == "" {
		return Workspace{}, errors.New("a personal workspace needs an owner's address")
	}
	name, err := w.freeName(handle(address))
	if err != nil {
		return Workspace{}, err
	}
	path := filepath.Join(w.Dir(), name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return Workspace{}, err
	}
	workspace := Workspace{Name: name, Path: path, Title: "Personal", Members: []string{address}}
	if err := w.Add(workspace); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// freeName is the first name that is not taken: ana, then ana-2, then ana-3. A cap rather than a loop that
// might not end, because a registry with a thousand Anas is a problem to look at rather than to keep trying.
func (w *Workspaces) freeName(base string) (string, error) {
	for attempt := 1; attempt <= 100; attempt++ {
		name := base
		if attempt > 1 {
			name = fmt.Sprintf("%s-%d", base, attempt)
		}
		if _, taken := w.Get(name); !taken {
			return name, nil
		}
	}
	return "", fmt.Errorf("no free workspace name for %q after 100 tries", base)
}

// handle is the readable part of an address, reduced to what a folder name and a workspace name allow. It is
// derived rather than asked for: the owner names people, and a form field for "what should my folder be
// called" is a question nobody wants to answer twice.
func handle(email string) string {
	local, _, _ := strings.Cut(email, "@")
	var cleaned strings.Builder
	for _, r := range strings.ToLower(local) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '+':
			cleaned.WriteRune(r)
		default:
			cleaned.WriteRune('-')
		}
	}
	name := strings.Trim(cleaned.String(), "-.")
	if name == "" {
		return "personal"
	}
	return name
}

// Get returns one workspace by name.
func (w *Workspaces) Get(name string) (Workspace, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	workspace, ok := w.registry[name]
	return workspace, ok
}

// Create makes a workspace inside the workspaces folder. This is the only way the API
// creates one, which is what keeps the interface inside a single directory: a folder
// anywhere else on the machine is an operator's decision, not a form field.
//
// A folder of that name already there is adopted rather than refused, so a workspace the
// reader prepared themselves works.
func (w *Workspaces) Create(name string) (Workspace, error) {
	if strings.TrimSpace(name) == "" {
		return Workspace{}, errors.New("a workspace needs a name")
	}
	if strings.ContainsAny(name, "/\\ ") {
		return Workspace{}, fmt.Errorf("a workspace name cannot contain a slash or a space: %q", name)
	}
	path := filepath.Join(w.Dir(), name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return Workspace{}, err
	}
	workspace := Workspace{Name: name, Path: path, Title: name}
	if err := w.Add(workspace); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// Add registers a workspace at a folder of the caller's choosing. The API does not use it;
// the CLI and anyone editing the registry do.
func (w *Workspaces) Add(workspace Workspace) error {
	if err := validateWorkspace(workspace); err != nil {
		return err
	}
	w.mu.Lock()
	if _, exists := w.registry[workspace.Name]; exists {
		w.mu.Unlock()
		return fmt.Errorf("workspace %q already exists", workspace.Name)
	}
	w.registry[workspace.Name] = workspace
	w.mu.Unlock()
	return w.persist()
}

// Update replaces a workspace record. The name may not change: it is the identity every
// route uses, and renaming would orphan conversations that nothing else references.
func (w *Workspaces) Update(workspace Workspace) error {
	if err := validateWorkspace(workspace); err != nil {
		return err
	}
	existing, control, rebuilt, err := w.adopt(workspace)
	if err != nil {
		return err
	}
	if control != nil && rebuilt {
		// Stopping is not enough for what the engine baked into the sandbox at creation: it is
		// started again, not built again, so the workspace would go on reaching nothing while the
		// record said otherwise. The sandbox is discarded so the next start is built from the
		// record — the network grant and the device grant are decisions about the guest, and a
		// guest keeps the one it booted with.
		control.discardSandbox(existing.Path)
	}
	return w.persist()
}

// adopt writes the record and returns what the caller has to do about it: the previous record, the
// control that was running it, and whether a grant changed — which is the one edit that needs the
// sandbox rebuilt rather than the worker restarted.
func (w *Workspaces) adopt(workspace Workspace) (Workspace, *Control, bool, error) {
	w.mu.Lock()
	existing, exists := w.registry[workspace.Name]
	if !exists {
		w.mu.Unlock()
		return Workspace{}, nil, false, fmt.Errorf("no workspace %q", workspace.Name)
	}
	// An existing record keeps its general flag: exactly one workspace is general, and it is not
	// this call's to change.
	workspace.General = existing.General
	workspace.Name = existing.Name
	w.registry[workspace.Name] = workspace
	control := w.controls[workspace.Name]
	// The folder is passed to the worker when it starts, so a change only takes effect in a new
	// process. Restarting here is what makes a move mean something immediately instead of at the
	// next idle eviction. A grant is built into the sandbox at creation, so it needs a new one —
	// and withdrawing it is a downgrade that waits for nothing.
	privateChanged := control != nil && existing.Private != workspace.Private
	devicesChanged := control != nil && disabled(existing.DisableDevices) != disabled(workspace.DisableDevices)
	rebuilt := privateChanged || devicesChanged
	restart := control != nil && (existing.Path != workspace.Path || rebuilt)
	if restart {
		delete(w.controls, workspace.Name)
	}
	w.mu.Unlock()
	if restart {
		control.Close()
		workerpool.Log("workspace %s stopped: its record changed", workspace.Name)
	} else if control != nil {
		control.SetWorkspace(workspace)
	}
	return existing, control, rebuilt, nil
}

// disabled is a workspace's opt-out as a boolean: absent and false mean the same thing.
func disabled(value *bool) bool { return value != nil && *value }

// discardSandbox deletes the workspace's sandbox so the next start builds it from the record. It
// exists for the edits an engine bakes in at creation — the network policy above all — where
// stopping is not enough because a stopped sandbox is started again, not built again.
func (c *Control) discardSandbox(workspacePath string) {
	manager := c.sandboxManager()
	if manager == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandbox.Timeout)
	defer cancel()
	if err := manager.Remove(ctx, workspacePath); err != nil && !errors.Is(err, sandbox.ErrUnavailable) {
		workerpool.Log("discarding the sandbox for %s: %v", c.opts.Workspace.Name, err)
	}
}

// Remove forgets a workspace. It never touches the folder or the history, the same way
// deleting a conversation is a tombstone rather than a delete.
func (w *Workspaces) Remove(ctx context.Context, name string) error {
	if name == PersonalWorkspaceName {
		return fmt.Errorf("the %s workspace cannot be removed: it is the one this install lands on", PersonalWorkspaceName)
	}
	w.mu.Lock()
	workspace, exists := w.registry[name]
	if !exists {
		w.mu.Unlock()
		return fmt.Errorf("no workspace %q", name)
	}
	control := w.controls[name]
	delete(w.registry, name)
	delete(w.controls, name)
	delete(w.progress, name)
	w.mu.Unlock()
	w.forgetUsed(name)
	if control != nil {
		control.Close()
	}
	// The sandbox is deleted with the workspace: it is the one thing that holds what the
	// agent accumulated inside it, and leaving it behind would leak a disk per forgotten
	// workspace. A failure is reported and does not stop the removal the user asked for.
	// Resolved for this workspace's own engine: the default engine's manager knows nothing about
	// a sandbox another engine made, and asking it to delete one is a no-op that leaks the disk.
	if manager := w.sandboxManagerFor(workspace); manager != nil {
		if err := manager.Remove(ctx, workspace.Path); err != nil && !errors.Is(err, sandbox.ErrUnavailable) {
			workerpool.Log("removing the sandbox for %s: %v", name, err)
		}
	}
	return w.persist()
}

// Ensure returns the Control for a workspace, starting its worker if this is the first
// use. The install's own workspace cannot be missing; any other name must be registered.
func (w *Workspaces) Ensure(ctx context.Context, name string) (*Control, error) {
	if name == "" {
		name = PersonalWorkspaceName
	}
	workspace, control, starter, known, closed := w.lookup(name)
	if closed {
		return nil, errors.New("the workspace layer is closed")
	}
	if !known {
		return nil, fmt.Errorf("no workspace %q", name)
	}
	if control != nil {
		w.markUsed(name)
		return control, nil
	}

	starter.Lock()
	defer starter.Unlock()
	// One starter per workspace, so two concurrent requests cannot spawn two workers over one
	// storage: the second would find it locked, or worse, write to it. The check is repeated
	// under that lock because the other request may have finished while this one waited.
	if control := w.controlFor(name); control != nil {
		w.markUsed(name)
		return control, nil
	}
	return w.open(ctx, name, workspace)
}

// lookup reads the registry once, and hands back the starter this workspace's open will use.
func (w *Workspaces) lookup(name string) (Workspace, *Control, *sync.Mutex, bool, bool) {
	w.mu.RLock()
	workspace, known := w.registry[name]
	control := w.controls[name]
	closed := w.closed
	starter := w.starting[name]
	w.mu.RUnlock()
	if starter != nil {
		return workspace, control, starter, known, closed
	}
	w.mu.Lock()
	if starter = w.starting[name]; starter == nil {
		starter = &sync.Mutex{}
		w.starting[name] = starter
	}
	w.mu.Unlock()
	return workspace, control, starter, known, closed
}

func (w *Workspaces) controlFor(name string) *Control {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.controls[name]
}

// open builds the control, opens it, and records what it became. From here the workspace is
// opening, and a second client asks what it is doing rather than waiting for it: the stage comes
// from the code that is doing the work.
func (w *Workspaces) open(ctx context.Context, name string, workspace Workspace) (*Control, error) {
	w.setProgress(name, Progress{Status: StatusOpening, Stage: "starting", Since: time.Now()})
	control := New(Options{
		OnStage:       w.stage(name),
		Progress:      w.Progress,
		Workspace:     workspace,
		Credentials:   filepath.Join(w.opts.DataDir, "credentials.json"),
		Registry:      w.List,
		WorkspacesDir: w.Dir(),
		DataDir:       w.opts.DataDir,
		UserHome:      w.opts.UserHome,
		StoragePath:   filepath.Join(w.opts.DataDir, "state", name, "session.sqlite"),
		SettingsPath:  filepath.Join(w.opts.DataDir, "state", name, "settings.json"),
		DraftsPath:    filepath.Join(w.opts.DataDir, "state", name, "drafts.json"),
		Sandbox:       w.opts.Sandbox,
		Sandboxes:     w.opts.Sandboxes,
		Engines:       w.Engines,
		Vault:         w.opts.Vault,
		WorkerCommand: w.opts.WorkerCommand,
		WorkerDir:     w.opts.WorkerDir,
		Update:        w.opts.Update,
		Model:         w.opts.Model,
	})
	if err := os.MkdirAll(filepath.Join(w.opts.DataDir, "state", name), 0o700); err != nil {
		return nil, err
	}
	control.Open(ctx)
	// A start is also an install: the microVM engine fetches itself the first time a workspace
	// asks for it, so the chooser's kept answer is stale from here on.
	w.refreshEngines()
	w.mu.Lock()
	w.controls[name] = control
	w.mu.Unlock()
	// A harness that did not come up is a workspace that opened with its tools disabled: it is
	// reported as failed, with the worker's own sentence, rather than as ready.
	if control.WorkerUp() {
		w.setProgress(name, Progress{Status: StatusReady, Since: time.Now()})
	} else {
		w.setProgress(name, Progress{Status: StatusFailed, Error: control.WorkerError(), Since: time.Now()})
	}
	w.markUsed(name)
	workerpool.Log("workspace %s started (%s)", name, workspace.Path)
	return control, nil
}

// The install-wide defaults file: <data-dir>/settings.json. The layer that owns the file owns
// writing it, so every workspace sees one value rather than a copy each.
func (w *Workspaces) installDefaultsPath() string {
	return filepath.Join(w.opts.DataDir, "settings.json")
}

// SetInstallModel changes the model every workspace falls back to, below its own default
// and above the last resort.
func (w *Workspaces) SetInstallModel(ctx context.Context, provider, modelID string) error {
	if (provider == "") != (modelID == "") {
		return errors.New("a model default needs both a provider and a model id")
	}
	if provider != "" && w.noneOffer(provider, modelID) {
		return unavailableModel(provider, modelID)
	}
	return w.changeInstall(ctx, func(defaults *Defaults) error {
		defaults.Model = protocol.ModelBinding{Provider: provider, ModelID: modelID}
		return nil
	})
}

// SetInstallThinking changes the level every workspace falls back to.
func (w *Workspaces) SetInstallThinking(ctx context.Context, level string) error {
	if err := validateThinkingLevel(level); err != nil {
		return err
	}
	return w.changeInstall(ctx, func(defaults *Defaults) error {
		defaults.Thinking = level
		return nil
	})
}

// SetInstallAgent changes the definition every workspace falls back to. A definition a
// workspace keeps to itself is legitimately unknown to the others: resolution skips it where
// it is not there.
func (w *Workspaces) SetInstallAgent(ctx context.Context, name string) error {
	if name != "" && w.noDefinition(name) {
		return fmt.Errorf("no agent definition named %q", name)
	}
	return w.changeInstall(ctx, func(defaults *Defaults) error {
		defaults.Agent = name
		return nil
	})
}

// changeInstall writes the file once and tells every running workspace, so a change applies
// without a restart.
func (w *Workspaces) changeInstall(ctx context.Context, change func(*Defaults) error) error {
	path := w.installDefaultsPath()
	defaults, err := LoadDefaults(path)
	if err != nil {
		return err
	}
	if err := change(&defaults); err != nil {
		return err
	}
	// An empty layer is not a layer: leaving a file full of zeroes behind would make the
	// install look configured when it is not.
	if defaults == (Defaults{}) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err := SaveDefaults(path, defaults); err != nil {
		return err
	}
	for _, control := range w.running() {
		if err := control.SetInstallDefaults(ctx, defaults); err != nil {
			workerpool.Log("apply install defaults to %s: %v", control.opts.Workspace.Name, err)
		}
	}
	return nil
}

// ApplySecrets reconciles the running workspaces a change affects. A global change reaches
// every started workspace; a workspace change reaches the one whose folder it names. A
// stopped workspace needs nothing: its next start reads the vault. Failures are logged and
// left in the workspace's sandbox state, because the store already has the value and the
// caller asked to save it, not to prove every sandbox accepted it.
func (w *Workspaces) ApplySecrets(ctx context.Context, scope vault.Scope, workspacePath string, deferRestart bool) {
	for _, control := range w.running() {
		if scope == vault.Workspace && control.opts.Workspace.Path != workspacePath {
			continue
		}
		if err := control.ApplySecrets(ctx, deferRestart); err != nil {
			workerpool.Log("secrets for %s: %v", control.opts.Workspace.Name, err)
		}
	}
}

// running is a snapshot of the started workspaces, copied out so nothing is called while the
// registry lock is held.
func (w *Workspaces) running() []*Control {
	w.mu.RLock()
	defer w.mu.RUnlock()
	controls := make([]*Control, 0, len(w.controls))
	for _, control := range w.controls {
		controls = append(controls, control)
	}
	return controls
}

// StopWorkers terminates every started workspace's worker process and
// leaves warm sandboxes alone: the next open reattaches. No failure is
// possible beyond the waits inside, so there is no error to return.
func (w *Workspaces) StopWorkers() {
	for _, control := range w.running() {
		control.stopWorker()
	}
}

// AbortAll stops every running conversation in every started workspace.
// One failure does not stop the rest: an update that asked for drain-stop
// wants everything quiet, and the joined error says what stayed loud.
func (w *Workspaces) AbortAll(ctx context.Context) error {
	var errs []error
	for _, control := range w.running() {
		for _, id := range control.RunningIDs() {
			if err := control.Abort(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// AnyRunning reports whether any conversation in any started workspace is running.
// Only started workspaces are asked: an unstarted worker has no runs. The update
// tier reads this before swapping binaries, so an update never lands mid-run.
func (w *Workspaces) AnyRunning() bool {
	for _, control := range w.running() {
		if control.AnyRunning() {
			return true
		}
	}
	return false
}

// RunningWorkspace names one started workspace with runs, and how many, so the
// install dialog can say what stopping runs would stop instead of saying "runs".
type RunningWorkspace struct {
	Workspace string `json:"workspace"`
	Runs      int    `json:"runs"`
}

// RunningSummary answers what a drain-stop would stop: every started workspace
// with running conversations, and the count in each, by name. The registry lock
// is released before the controls are asked, so the lock order stays registry
// before control.
func (w *Workspaces) RunningSummary() []RunningWorkspace {
	w.mu.RLock()
	named := make(map[string]*Control, len(w.controls))
	maps.Copy(named, w.controls)
	w.mu.RUnlock()
	summary := make([]RunningWorkspace, 0, len(named))
	for name, control := range named {
		if runs := len(control.RunningIDs()); runs > 0 {
			summary = append(summary, RunningWorkspace{Workspace: name, Runs: runs})
		}
	}
	sort.Slice(summary, func(i, j int) bool { return summary[i].Workspace < summary[j].Workspace })
	return summary
}

// noneOffer reports whether no started workspace offers this model. With none started there
// is nothing to check against, and refusing would make the first thing anyone does fail.
func (w *Workspaces) noneOffer(provider, modelID string) bool {
	controls := w.running()
	if len(controls) == 0 {
		return false
	}
	for _, control := range controls {
		if control.isAvailable(control.Describe(), protocol.ModelBinding{Provider: provider, ModelID: modelID}) {
			return false
		}
	}
	return true
}

func (w *Workspaces) noDefinition(name string) bool {
	controls := w.running()
	if len(controls) == 0 {
		return false
	}
	for _, control := range controls {
		control.mu.RLock()
		found := definitionExists(control.catalog.Agents, name)
		control.mu.RUnlock()
		if found {
			return false
		}
	}
	return true
}

// Route finds the workspace a request is about and the control that serves it.
func (w *Workspaces) Route(ctx context.Context, caller Caller, workspaceName string) (Workspace, *Control, error) {
	name := workspaceName
	if name == "" {
		// "The workspace" meant one workspace when there was one owner. Now it means the caller's own, and
		// for a person that is their personal workspace, made the first time they ask (ADR 0032 §3, §9).
		own, err := w.personal(caller)
		if err != nil {
			return Workspace{}, nil, err
		}
		name = own
	}
	workspace, ok := w.Get(name)
	// One refusal for both, deliberately: a caller who is not a member learns nothing about whether the
	// workspace exists.
	if !ok || !workspace.Allows(caller) {
		return Workspace{}, nil, fmt.Errorf("%w %q", ErrNoWorkspace, name)
	}
	control, err := w.Ensure(ctx, name)
	if err != nil {
		return Workspace{}, nil, err
	}
	return workspace, control, nil
}

// The states a workspace can be in between "asked for" and "usable". They are strings because
// they cross the API, and the interface shows them as they are.
const (
	StatusStopped = "stopped"
	StatusOpening = "opening"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// Progress is what a workspace's start is doing. The stage is a sentence from the code that is
// doing the work, so the interface can say "copying the image" instead of showing nothing.
type Progress struct {
	Status string    `json:"status"`
	Stage  string    `json:"stage,omitempty"`
	Since  time.Time `json:"since,omitzero"`
	Error  string    `json:"error,omitempty"`
}

// Progress reports where a workspace is: stopped, opening (with the step it is on), ready, or
// failed. It never starts anything, so the interface can ask this while a start is in flight.
func (w *Workspaces) Progress(name string) Progress {
	if name == "" {
		name = PersonalWorkspaceName
	}
	w.mu.RLock()
	control := w.controls[name]
	progress, known := w.progress[name]
	w.mu.RUnlock()
	// A live control answers for itself: a harness that came up and then died is failed, not
	// ready, and the stored entry would still say ready.
	if control != nil {
		if control.WorkerUp() {
			return Progress{Status: StatusReady}
		}
		return Progress{Status: StatusFailed, Error: control.WorkerError()}
	}
	if !known {
		return Progress{Status: StatusStopped}
	}
	return progress
}

// setProgress records a step. Called while a workspace opens, so it is short and happens under
// the lock the registry already uses.
func (w *Workspaces) setProgress(name string, progress Progress) {
	if progress.Since.IsZero() {
		progress.Since = time.Now()
	}
	w.mu.Lock()
	if w.progress == nil {
		w.progress = map[string]Progress{}
	}
	w.progress[name] = progress
	w.mu.Unlock()
}

// stage is what a workspace's start tells the interface as it goes.
func (w *Workspaces) stage(name string) func(string) {
	return func(step string) {
		if strings.TrimSpace(step) == "" {
			return
		}
		w.setProgress(name, Progress{Status: StatusOpening, Stage: step, Since: time.Now()})
	}
}

// markUsed records that a workspace was asked for, which is what keeps an actively used
// one from being evicted.
func (w *Workspaces) markUsed(name string) {
	w.usedMu.Lock()
	w.used[name] = time.Now()
	w.usedMu.Unlock()
}

func (w *Workspaces) forgetUsed(name string) {
	w.usedMu.Lock()
	delete(w.used, name)
	w.usedMu.Unlock()
}

// idleFor reports how long a workspace has gone unused. An unknown one is idle: it has no
// worker to keep.
func (w *Workspaces) idleFor(name string) time.Duration {
	w.usedMu.Lock()
	defer w.usedMu.Unlock()
	at, known := w.used[name]
	if !known {
		return idleEviction
	}
	return time.Since(at)
}

// evictIdle stops workers nobody is watching. The install's own workspace is kept: it is where a
// fresh client lands, and starting it is the first thing every visit does.
func (w *Workspaces) evictIdle(ctx context.Context) {
	ticker := time.NewTicker(evictionSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stopping := w.evictCandidates()
			for _, control := range stopping {
				control.Close()
			}
			if len(stopping) > 0 {
				workerpool.Log("stopped %d idle workspace(s)", len(stopping))
			}
		}
	}
}

// evictCandidates decides which idle workspaces to stop, and removes them from the
// controls map so nothing can start a second worker over the same storage while they are
// being closed. Closing happens outside: it waits for a process, and the registry lock is
// not a place to wait for anything.
//
// This is the one caller that reads a control while holding the registry lock, which is why
// the order is registry-then-control everywhere.
func (w *Workspaces) evictCandidates() []*Control {
	w.mu.Lock()
	defer w.mu.Unlock()
	var stopping []*Control
	for name, control := range w.controls {
		if name == PersonalWorkspaceName {
			continue
		}
		// A workspace that runs something keeps running: its sandbox's processes die with it,
		// and a cron job that is not there at its next run is worse than a few megabytes held.
		if workspace, ok := w.registry[name]; ok && workspace.KeepAlive {
			continue
		}
		if w.idleFor(name) < idleEviction {
			continue
		}
		if control.Watching() || control.Busy() {
			continue
		}
		// Stopped is a state the interface shows, so it is recorded with the others. This runs
		// under the registry lock, which is why it does not go through setProgress.
		if w.progress == nil {
			w.progress = map[string]Progress{}
		}
		w.progress[name] = Progress{Status: StatusStopped, Since: time.Now()}
		stopping = append(stopping, control)
		delete(w.controls, name)
	}
	return stopping
}

func validateWorkspace(workspace Workspace) error {
	if strings.TrimSpace(workspace.Name) == "" {
		return errors.New("a workspace needs a name")
	}
	if strings.ContainsAny(workspace.Name, "/\\ ") {
		return fmt.Errorf("a workspace name cannot contain a slash or a space: %q", workspace.Name)
	}
	if strings.TrimSpace(workspace.Path) == "" {
		return fmt.Errorf("workspace %q needs a folder", workspace.Name)
	}
	if !sandbox.ValidEngine(workspace.Engine) {
		return fmt.Errorf("workspace %q names an unknown sandbox engine %q (choose one of %s)", workspace.Name, workspace.Engine, strings.Join(sandbox.EngineNames(), ", "))
	}
	return nil
}

func (w *Workspaces) registryPath() string {
	return filepath.Join(w.opts.DataDir, registryFile)
}

// RegistryFile is the workspaces registry as it exists on disk. It has one owner so the
// server and the command line cannot disagree about its shape.
//
// WorkspacesDir is recorded here rather than in the global config because it belongs to the
// registry, and because that config is what shapes prompts: a control-plane path there
// would be a concern in the wrong file.
type RegistryFile struct {
	// Path is where it was read from; not part of the file.
	Path string `json:"-"`
	// dropped says the file mentioned keys this build no longer writes, so the caller can
	// rewrite it. Not part of the file either.
	dropped       bool
	WorkspacesDir string      `json:"workspacesDir,omitempty"`
	Workspaces    []Workspace `json:"workspaces"`
}

// droppedKeys reports whether the file on disk mentions a key this build no longer writes.
//
// A workspace used to carry `trusted`. Nothing reads it any more, and a stale value that
// nothing reads is a value something can read again by accident — so the first load that sees
// one rewrites the file without it, rather than leaving it there for a later version to
// rediscover and mistrust.
func droppedKeys(payload []byte) bool {
	var raw struct {
		Workspaces []map[string]json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return false
	}
	for _, record := range raw.Workspaces {
		if _, found := record["trusted"]; found {
			return true
		}
	}
	return false
}

// LoadRegistryFile reads a registry, tolerating absence.
func LoadRegistryFile(path string) (RegistryFile, error) {
	registry := RegistryFile{Path: path}
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	if err := json.Unmarshal(payload, &registry); err != nil {
		return registry, fmt.Errorf("%s: %w", path, err)
	}
	registry.Path = path
	registry.dropped = droppedKeys(payload)
	return registry, nil
}

// SaveRegistryFile writes a registry atomically and privately.
func SaveRegistryFile(registry RegistryFile) error {
	sort.Slice(registry.Workspaces, func(i, j int) bool { return registry.Workspaces[i].Name < registry.Workspaces[j].Name })
	payload, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(registry.Path), 0o700); err != nil {
		return err
	}
	temp := registry.Path + ".tmp"
	if err := os.WriteFile(temp, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, registry.Path)
}

func (w *Workspaces) loadRegistry() error {
	file, err := LoadRegistryFile(w.registryPath())
	if err != nil {
		return err
	}
	// Anything the file says that we no longer write is dropped on the way out, once.
	defer func() {
		if file.dropped {
			if err := w.persist(); err != nil {
				workerpool.Log("rewriting the registry without old keys: %v", err)
			}
		}
	}()
	for _, workspace := range file.Workspaces {
		w.registry[workspace.Name] = workspace
	}
	if err := w.resolveDir(file.WorkspacesDir); err != nil {
		return err
	}
	if err := w.renameGeneralToPersonal(); err != nil {
		return err
	}
	return w.ensurePersonal()
}

// resolveDir decides where workspaces live: the flag for this run, then what the file recorded,
// then the platform default. The flag is recorded, so where workspaces live is a decision that
// sticks rather than one that has to be repeated.
func (w *Workspaces) resolveDir(recorded string) error {
	dir := w.opts.WorkspacesDir
	if dir == "" {
		dir = recorded
	}
	if dir == "" {
		defaultDir, err := DefaultWorkspacesDir()
		if err != nil {
			return err
		}
		dir = defaultDir
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	w.dir = absolute
	if w.opts.WorkspacesDir != "" && w.opts.WorkspacesDir != recorded {
		workerpool.Log("workspaces folder: %s", absolute)
	}
	return nil
}

// oldWorkspaceName is what the install's own workspace was called before ADR 0032 §9.
const oldWorkspaceName = "general"

// renameGeneralToPersonal moves an install onto the name the ADR chose: the record, and the whole state folder
// with it.
//
// The folder has to travel. A workspace's storage, its settings and its drafts live under
// `<data-dir>/state/<name>/`, so a rename that moved the record and left the folder would present every
// conversation as lost — which is what ADR 0011's first attempt at moving one of those files did, one
// directory down.
func (w *Workspaces) renameGeneralToPersonal() error {
	w.mu.Lock()
	legacy, found := w.registry[oldWorkspaceName]
	_, taken := w.registry[PersonalWorkspaceName]
	w.mu.Unlock()
	switch {
	case !found:
		return nil
	case taken:
		// Both names exist: a registry somebody edited, or an install that made the new one already. Nothing
		// is moved, because guessing which of the two holds the real history is how a transcript is lost.
		workerpool.Log("workspaces: %q and %q both exist, so nothing was renamed; %q is kept as an ordinary workspace", oldWorkspaceName, PersonalWorkspaceName, oldWorkspaceName)
		return nil
	}
	if err := w.moveState(oldWorkspaceName, PersonalWorkspaceName); err != nil {
		return err
	}
	w.mu.Lock()
	legacy.Name = PersonalWorkspaceName
	legacy.Title = "Personal"
	if len(legacy.Members) == 0 && strings.TrimSpace(w.opts.OwnerEmail) != "" {
		legacy.Members = []string{strings.ToLower(strings.TrimSpace(w.opts.OwnerEmail))}
	}
	delete(w.registry, oldWorkspaceName)
	w.registry[PersonalWorkspaceName] = legacy
	w.mu.Unlock()
	workerpool.Log("workspaces: renamed %q to %q, with its state folder", oldWorkspaceName, PersonalWorkspaceName)
	return w.persist()
}

// moveState renames one workspace's state folder. One rename, so it either happened or it did not; a folder
// that is not there is not a failure, because a workspace nobody has opened has no state yet.
func (w *Workspaces) moveState(from, to string) error {
	source := filepath.Join(w.opts.DataDir, "state", from)
	target := filepath.Join(w.opts.DataDir, "state", to)
	switch _, err := os.Stat(source); {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if _, err := os.Stat(target); err == nil {
		// Refused rather than merged: two folders of conversations under one name is a question only a person
		// can answer, and boot says so instead of picking one.
		return fmt.Errorf("state/%s is in the way of renaming state/%s: move one aside and start again", to, from)
	}
	return os.Rename(source, target)
}

// ensurePersonal makes the install's own workspace on a first run, then repairs a registry that was
// hand-edited into two: exactly one workspace is the install's own.
func (w *Workspaces) ensurePersonal() error {
	if _, ok := w.registry[PersonalWorkspaceName]; !ok {
		// First run: the install's own workspace is a workspace like any other, created in the
		// workspaces folder under its own name. An install that has one already keeps it where it was,
		// because the registry says where it is.
		absolute, err := filepath.Abs(filepath.Join(w.dir, PersonalWorkspaceName))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(absolute, 0o755); err != nil {
			return err
		}
		w.registry[PersonalWorkspaceName] = Workspace{
			Name:    PersonalWorkspaceName,
			Path:    absolute,
			Title:   filepath.Base(absolute),
			General: true,
		}
		if err := w.persist(); err != nil {
			return err
		}
	}
	// Exactly one install workspace: a registry hand-edited into two is repaired by keeping one and
	// clearing the rest.
	w.exactlyOneGeneral()
	return nil
}

// exactlyOneGeneral keeps the workspace named general if one is, and otherwise the first by name.
// The rule decides rather than the iteration order: the registry is a map, which has no order to
// keep, so "the first in the file" was a promise this code could not have kept.
func (w *Workspaces) exactlyOneGeneral() {
	keeper := ""
	for name, workspace := range w.registry {
		if !workspace.General {
			continue
		}
		switch {
		case keeper == "":
			keeper = name
		case name == PersonalWorkspaceName:
			keeper = name
		case keeper != PersonalWorkspaceName && name < keeper:
			keeper = name
		}
	}
	for name, workspace := range w.registry {
		if !workspace.General || name == keeper {
			continue
		}
		workspace.General = false
		w.registry[name] = workspace
	}
}

// persist writes the registry: the folder it records, and every workspace in it. It is the file
// that answers where a workspace lives on the next start.

func (w *Workspaces) persist() error {
	w.mu.RLock()
	file := RegistryFile{Path: w.registryPath(), WorkspacesDir: w.dir, Workspaces: make([]Workspace, 0, len(w.registry))}
	for _, workspace := range w.registry {
		file.Workspaces = append(file.Workspaces, workspace)
	}
	w.mu.RUnlock()
	return SaveRegistryFile(file)
}

// nameTheOwnersWorkspaces gives an unnamed workspace to the install's owner.
//
// Before there were people, every conversation in this registry was the owner's, so this records a fact
// rather than making a grant. Without it, the first boot with a door would hide every existing workspace
// from the person at the keyboard — the one outcome nobody would guess from an upgrade.
func (w *Workspaces) nameTheOwnersWorkspaces() error {
	owner := strings.ToLower(strings.TrimSpace(w.opts.OwnerEmail))
	if owner == "" {
		return nil
	}
	w.mu.Lock()
	changed := false
	for name, workspace := range w.registry {
		if len(workspace.Members) > 0 {
			continue
		}
		workspace.Members = []string{owner}
		w.registry[name] = workspace
		changed = true
	}
	w.mu.Unlock()
	if !changed {
		return nil
	}
	return w.persist()
}

// migrate moves a single-workspace install onto the workspace layout: one storage and one
// settings file, moved into the install's own workspace's state directory. Nothing is rewritten
// inside them, so every conversation appears exactly where it was.
func (w *Workspaces) migrate() error {
	state := filepath.Join(w.opts.DataDir, "state", PersonalWorkspaceName)
	// The write-ahead log travels with its database. Moving one without the other loses
	// every commit that had not been checkpointed and leaves megabytes behind.
	moves := []struct{ from, to string }{
		{filepath.Join(w.opts.DataDir, "session.sqlite"), filepath.Join(state, "session.sqlite")},
		{filepath.Join(w.opts.DataDir, "session.sqlite-wal"), filepath.Join(state, "session.sqlite-wal")},
		{filepath.Join(w.opts.DataDir, "session.sqlite-shm"), filepath.Join(state, "session.sqlite-shm")},
		{filepath.Join(w.opts.DataDir, "settings.json"), filepath.Join(state, "settings.json")},
	}
	for _, move := range moves {
		if _, err := os.Stat(move.from); err != nil {
			continue
		}
		if _, err := os.Stat(move.to); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(move.to), 0o700); err != nil {
			return err
		}
		if err := os.Rename(move.from, move.to); err != nil {
			return fmt.Errorf("moving %s into the %s workspace: %w", move.from, PersonalWorkspaceName, err)
		}
		workerpool.Log("migrated %s to %s", move.from, move.to)
	}
	// A database that already moved leaves its sidecars behind: they belong to it, so
	// leaving them would both waste space and invite the next reader to use the wrong one.
	if _, err := os.Stat(filepath.Join(state, "session.sqlite")); err == nil {
		for _, orphan := range []string{"session.sqlite-wal", "session.sqlite-shm"} {
			path := filepath.Join(w.opts.DataDir, orphan)
			if _, err := os.Stat(path); err == nil {
				workerpool.Log("removing orphaned %s", path)
				_ = os.Remove(path)
			}
		}
	}
	return nil
}

// EngineInfo is one sandbox engine this install can run, as the chooser shows it. It is a
// machine fact, not a workspace's: the same for every workspace, cached because a listing is
// served on every state request.
type EngineInfo struct {
	Name      string `json:"name"`
	Default   bool   `json:"default,omitempty"`
	Isolated  bool   `json:"isolated"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Substitutes is whether a secret's value stays on the host and only a placeholder enters the
	// sandbox. The card's wording follows it, so the interface does not carry its own list of which
	// engines substitute — a list that would be wrong the day a third engine arrives.
	Substitutes bool `json:"substitutes,omitempty"`
	// Problem is why this machine cannot run the engine, in a sentence. An engine that is
	// listed with a problem is still offered: the person choosing should see the reason.
	Problem string `json:"problem,omitempty"`
}

// DefaultEngine is the engine a workspace that names none gets: the install's own choice when
// `--sandbox-engine` named one, otherwise the first engine this machine can actually run. It is
// what a workspace records at creation, so the choice is made once and stays visible.
func (w *Workspaces) DefaultEngine() string {
	if w.opts.Sandbox != nil && w.opts.Sandbox.Driver() != nil {
		return w.opts.Sandbox.Driver().Name()
	}
	return "host"
}

// Engines reports every engine this build implements, whether this machine can run it, and
// which one a workspace with no engine of its own gets. The answer is kept until a workspace
// start drops it: an engine with no package manager installs itself on first use, so the
// chooser must not keep saying it is missing once it is there (refreshEngines).
func (w *Workspaces) Engines() []EngineInfo {
	w.enginesMu.Lock()
	defer w.enginesMu.Unlock()
	if w.enginesSet {
		return w.engines
	}
	def := w.DefaultEngine()
	infos := make([]EngineInfo, 0, len(sandbox.EngineNames()))
	for _, name := range sandbox.EngineNames() {
		infos = append(infos, w.engineInfo(name, name == def))
	}
	w.engines = infos
	w.enginesSet = true
	return infos
}

// refreshEngines drops the kept answer so the next request probes the machine again. A
// workspace's start is where an engine without a package manager arrives: the microVM
// downloads itself on first use, and a list cached before that would leave the switch that
// asked for it disabled with no way to install it.
func (w *Workspaces) refreshEngines() {
	w.enginesMu.Lock()
	w.enginesSet = false
	w.engines = nil
	w.enginesMu.Unlock()
}

// engineInfo is what this machine can say about one engine without starting it: whether it is
// installed, what version, and — a probe the machine answers rather than a claim from a table —
// whether it is usable here. Bubblewrap checks a user namespace and the package itself; the
// microVM engine checks its hypervisor, which is a fact about the machine and not about whether
// the 67 MB engine has been fetched. The probe runs whether or not the engine is on disk: an
// engine that is missing and cannot fetch itself must keep saying why, and one that fetches
// itself on first use must be offered with no download behind it.
func (w *Workspaces) engineInfo(name string, isDefault bool) EngineInfo {
	info := EngineInfo{Name: name, Default: isDefault}
	manager := w.engineManager(name)
	if manager == nil {
		return info
	}
	info.Substitutes = manager.SecretDelivery() == sandbox.DeliverySubstitute
	report := manager.Report()
	info.Installed = report.Installed
	info.Version = report.Version
	info.Isolated = report.Isolated
	if report.Problem != "" {
		info.Problem = report.Problem
		return info
	}
	if err := manager.Driver().Usable(); err != nil {
		info.Problem = err.Error()
	}
	return info
}

// sandboxManagerFor is the manager that owns a workspace's sandbox: the workspace's own engine
// when it named one, the install's default when it did not, and nil when this install has no tier.
// It is the same rule Control applies when it provisions, and the two have to agree or a workspace
// is stopped by one engine and removed by another.
func (w *Workspaces) sandboxManagerFor(workspace Workspace) *sandbox.Manager {
	if name := strings.TrimSpace(workspace.Engine); name != "" && w.opts.Sandboxes != nil {
		if manager, err := w.opts.Sandboxes.For(name); err == nil {
			return manager
		}
	}
	return w.opts.Sandbox
}

// engineManager resolves one engine's manager without starting anything: the tier builds and
// caches managers, and building one installs nothing.
func (w *Workspaces) engineManager(name string) *sandbox.Manager {
	if w.opts.Sandboxes != nil {
		if manager, err := w.opts.Sandboxes.For(name); err == nil {
			return manager
		}
		return nil
	}
	if w.opts.Sandbox != nil && w.opts.Sandbox.Driver() != nil && w.opts.Sandbox.Driver().Name() == name {
		return w.opts.Sandbox
	}
	return nil
}
