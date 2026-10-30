// construction, the state the interface reads, and the lifetime of the whole thing
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/secretsproxy"
	"github.com/acornfall/acornfall/gateway/internal/vault"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
)

// ErrNoProvider means the install has no usable credential yet. It is a state,
// not a failure: the UI has to be reachable so the user can connect one.
var ErrNoProvider = errors.New("no authenticated provider")

// Options configures one Control instance, which owns exactly one workspace.
type Options struct {
	// Workspace is the folder and name this Control serves.
	Workspace Workspace
	// DataDir is the installation folder: global resources and shared credentials.
	DataDir string
	// UserHome is the user's home folder, for `~/.agents`, which a sandbox mounts read-only
	// beside the install's own resource folders. The entry point resolves it and fails when it
	// cannot, so a control always has one.
	UserHome string
	// StoragePath is this workspace's durable storage. One storage per workspace, because
	// conversation ids are only unique inside one.
	StoragePath string
	// WorkerCommand is the base command; the per-workspace flags are appended in here.
	WorkerCommand []string
	WorkerDir     string
	// Update reports the self-update summary for the state stream. Nil
	// means unknown: the interface falls back to fetching.
	Update      func() UpdateSummary
	Credentials string
	// Model is the "provider/modelId" to use as the workspace default. It wins over a
	// persisted default, and every conversation that never chose a model follows it.
	Model string
	// SettingsPath persists the active conversation and per-conversation agents.
	SettingsPath string
	// DraftsPath persists the unsent message of each conversation. Beside the settings, and
	// a file of its own: a draft changes with every keystroke, the settings rarely, and one
	// file that holds both would rewrite the choices every time someone typed.
	DraftsPath string
	// Registry lists the install's workspaces, so a client can switch between them.
	Registry func() []Workspace
	// WorkspacesDir is where new workspaces are created, so the interface can say so.
	WorkspacesDir string
	// Sandboxes holds every engine this install can run, so a workspace that names an engine
	// gets that engine rather than the install's default. Nil means this build has one.
	Sandboxes *sandbox.Tier
	// Engines is the machine's engine list for the chooser: names, versions, and why one
	// cannot run here. Nil reports none, which is what an install with no sandbox tier has.
	Engines func() []EngineInfo
	// Progress reports where a workspace is, so the list in every state frame says which ones
	// are opening, failed, or ready. Nil means the states carry no status.
	Progress func(name string) Progress
	// OnStage reports a step that takes time while a workspace opens: starting the harness,
	// preparing an image, booting the sandbox. Nil means nobody is listening.
	OnStage func(step string)
	// Sandbox is the machine's sandbox tier. Nil means this Control was built without one,
	// which is what ACORNFALL_NO_SANDBOX asks the gateway for: the workspace still opens and
	// its tools refuse.
	Sandbox *sandbox.Manager
	// Vault is the install-wide tool-secret store. Nil means this workspace has no vault,
	// and its sandbox receives no secret rather than failing to open.
	Vault *vault.Store
}

// UpdateSummary is the self-update state as the stream carries it: enough
// for the header dot and the failure line, without a second round trip.
// The full screen still reads GET /api/update/status. It rides the state
// because the state stream is the one channel every client already holds.
type UpdateSummary struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	Available       bool   `json:"available"`
	Failed          string `json:"failed,omitempty"`
	RestartRequired bool   `json:"restartRequired,omitempty"`
}

// WorkspaceState is a workspace as the interface needs it: what it is called, where it
// points, and whether its worker is running.
type WorkspaceState struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Title string `json:"title"`
	// Personal is this workspace being somebody's own room, by its stored kind (ADR 0034 §5). It decides the
	// rail's order, and it is why the interface never offers to share or forget one.
	Personal bool `json:"personal"`
	// Kind is "personal" or "room", and Members is who is in it: an owner and its guests. A room's members
	// are shown to the room, which is what the share dialog reads.
	Kind    string   `json:"kind,omitempty"`
	Members []Member `json:"members,omitempty"`
	Image   string   `json:"image,omitempty"`
	Network string   `json:"network,omitempty"`
	// Private is whether this workspace's sandbox may reach private networks as this machine.
	Private bool `json:"private,omitempty"`
	// KeepAlive is whether this workspace's sandbox is exempt from idle eviction.
	KeepAlive bool `json:"keep_alive,omitempty"`
	// Engine is which sandbox engine this workspace runs on; empty means the install default.
	Engine string `json:"engine,omitempty"`
	// DisableVMNesting is nil when this workspace nests whenever it can,
	// and a choice of its own otherwise. True refuses nesting; absent or false allows it
	// wherever the engine and the machine can.
	DisableVMNesting *bool `json:"disable_vm_nesting,omitempty"`
	// DisableDevices is nil when this bwrap workspace receives /dev/net/tun and /dev/kvm,
	// which is the default, and a choice of its own otherwise. True withholds them.
	DisableDevices *bool `json:"disable_devices,omitempty"`
	Running        bool  `json:"running"`
	// Status is stopped, opening, ready, or failed; Stage is the step an opening workspace is on,
	// in the words of the code doing it. They are separate from Running because a workspace that
	// is opening has no worker yet, and "not running" would say nothing about why.
	Status string `json:"status,omitempty"`
	Stage  string `json:"stage,omitempty"`
	Error  string `json:"error,omitempty"`
}

// workspaceStateOf is one record as the interface reads it, in the state and in the listing (Views has the
// same mapping for a list; this one is the active room, and the two must not disagree).
func workspaceStateOf(workspace Workspace, running bool) WorkspaceState {
	return WorkspaceState{
		Name:     workspace.Name,
		Path:     workspace.Path,
		Title:    workspace.Title,
		Personal: workspace.Personal(),
		Kind:     workspace.Kind,
		Members:  workspace.Members,
		Running:  running,
	}
}

// canIn is the state's copy of the permission table, so the interface disables a control instead of reading a
// role out of a name.
func canIn(workspace Workspace, caller Caller) Can {
	return Can{
		Create:     workspace.May(caller, ActionCreate),
		Fork:       workspace.May(caller, ActionFork),
		Hide:       workspace.May(caller, ActionHide),
		Rename:     workspace.May(caller, ActionRename),
		Model:      workspace.May(caller, ActionModel),
		Administer: workspace.May(caller, ActionAdminister),
	}
}

// Can is what the caller may change in this room, so the interface disables what it may not instead of
// inferring a role from a control's name. It is the state's copy of the one table, `Workspace.May`.
type Can struct {
	// MakeRoom is the install's permission, not the room's: may this caller lead a room of their own.
	MakeRoom   bool `json:"makeRoom"`
	Create     bool `json:"create"`
	Fork       bool `json:"fork"`
	Hide       bool `json:"hide"`
	Rename     bool `json:"rename"`
	Model      bool `json:"model"`
	Administer bool `json:"administer"`
}

// Inherited reports which fields the active conversation left to the defaults.
type Inherited struct {
	Model    bool `json:"model"`
	Thinking bool `json:"thinking"`
	Agent    bool `json:"agent"`
}

// State is everything the UI needs to render before any frame arrives.
type State struct {
	Workspace     WorkspaceState   `json:"workspace"`
	Workspaces    []WorkspaceState `json:"workspaces"`
	WorkspacesDir string           `json:"workspacesDir"`
	// Engines is what a new workspace may run on, with this machine's answer for each. It is
	// a machine fact, so it is the same in every state frame.
	Engines       []EngineInfo            `json:"engines,omitempty"`
	Harness       string                  `json:"harness"`
	Capabilities  protocol.Capabilities   `json:"capabilities"`
	Conversation  *Conversation           `json:"conversation,omitempty"`
	Conversations []Conversation          `json:"conversations"`
	Agent         protocol.ModelBinding   `json:"agent"`
	Resolved      *protocol.ResolvedAgent `json:"resolved,omitempty"`
	// Defaults apply to conversations that never chose, and to new ones. They are resolved
	// across the layers, so what is here is what will run; the origin says which layer gave
	// each one, so the interface can explain rather than just state it.
	DefaultModel    protocol.ModelBinding `json:"defaultModel"`
	DefaultThinking string                `json:"defaultThinking"`
	DefaultAgent    string                `json:"defaultAgent"`
	DefaultsOrigin  DefaultOrigins        `json:"defaultsOrigin"`
	// Each scope's own layer, unresolved. The resolved defaults above are what will run; these
	// are what the settings screen shows and writes, one tab per scope.
	WorkspaceDefaults ScopeDefaults `json:"workspaceDefaults"`
	InstallDefaults   ScopeDefaults `json:"installDefaults"`
	// Inherited says, per field, that this conversation never chose it.
	Inherited Inherited `json:"inherited"`
	// Subagent is true when the active conversation was created by another one. Its
	// configuration belongs to that creator, so the UI shows it rather than offering
	// to change it.
	Subagent bool `json:"subagent"`
	// Policy is what this conversation may do with other conversations.
	Policy json.RawMessage `json:"policy,omitempty"`
	// Draft is the active conversation's unsent message, if it ever had one. It rides the
	// state so every machine shows the same half-thought in the composer, and so a reload
	// lands on what was typed a second ago rather than on an empty card.
	Draft *Draft `json:"draft,omitempty"`
	// Can is what this caller may change here, and Role is what they are in the room. A guest sees the model
	// and the agent with their values and cannot pick another (ADR 0034 §4).
	Can  Can    `json:"can"`
	Role string `json:"role,omitempty"`
	// Admin is the install's role, not the room's: it is what shows the People screen. Owning the install does
	// not make the caller a member of anybody's room (ADR 0034 §1).
	Admin bool `json:"admin"`
	// WorkerUp is false when the harness process is gone. Without it a dead backend is
	// indistinguishable from an idle one, because everything here is served from cache.
	WorkerUp    bool   `json:"workerUp"`
	WorkerError string `json:"workerError,omitempty"`
	// Sandbox is where this workspace's commands and file reads actually happen.
	Sandbox SandboxState `json:"sandbox"`
	// Update is the self-update summary: the header dot without fetching.
	Update    UpdateSummary           `json:"update"`
	Agents    []protocol.AgentSummary `json:"agents"`
	Skills    []protocol.SkillSummary `json:"skills"`
	Defaults  []protocol.LayerTrace   `json:"defaults"`
	Providers []protocol.ProviderInfo `json:"providers"`
	Seq       int64                   `json:"seq"`
}

// workerConn is what control needs from a harness process: the commands it sends, the frames it is
// handed, and the little it asks about one that has died. Declared here because this package is the
// consumer; every method is one control actually calls, and *workerpool.Worker is the only
// implementation in the product. It exists because the tests speak the protocol without a process,
// which is the reason G2 allows an interface at all.
type workerConn interface {
	Start(ctx context.Context) error
	Close()
	Call(ctx context.Context, command protocol.Command) (json.RawMessage, error)
	CallInto(ctx context.Context, command protocol.Command, out any) error
	OnFrame(handler func(protocol.Frame))
	OnExit(handler func(error))
	LastWords() string
}

type Control struct {
	opts Options
	// worker is the harness process. It is an interface because control is its consumer and the
	// tests speak the protocol without a process: the only implementation in the product is
	// *workerpool.Worker, which is what startWorker assigns.
	worker workerConn

	mu            sync.RWMutex
	describe      protocol.Describe
	conversations []protocol.ConversationInfo
	active        string
	workerUp      bool
	workerError   string
	// sandbox is what the agent's tools can reach, refreshed when the worker starts. It is
	// nil-free on purpose: "no sandbox" is a state the interface has to be able to show.
	sandbox SandboxState
	// engine resolves this workspace's engine once, because a workspace's engine is a
	// property of its record and the record only changes through SetWorkspace.
	engineMu      sync.Mutex
	engineDone    bool
	engineManager *sandbox.Manager
	engineProblem error
	// One notification per death, drained by the single supervisor loop. A restart loop
	// per death would compound: every failed attempt is another death.
	workerExited chan struct{}
	// lifetime is what the background loops run on. Not the context that happened to start
	// this workspace: that is often one HTTP request, and a supervisor that stops when the
	// first request finishes never supervises anything. Close is what ends it.
	lifetime context.Context
	stop     context.CancelFunc
	// proxy serves this workspace's secrets, and proxyStop ends it. It is the control's lifetime,
	// not a request's: a proxy that stopped when the request that started it finished would leave
	// a running sandbox with a forward into a closed port. Nil when the engine substitutes nothing
	// or the vault resolved to no secret. See docs/adr/0031-a-proxy-for-bwrap-secrets.md.
	proxy     *secretsproxy.Proxy
	proxyStop context.CancelFunc
	// bindings holds only explicit choices, made in a conversation. A conversation
	// with no entry here follows the definition's model or defaultModel.
	bindings map[string]protocol.ModelBinding
	// default* is this workspace's own layer, and is what gets persisted here.
	defaultModel    protocol.ModelBinding
	defaultThinking string
	defaultAgent    string
	/** Whether the conversations that predate naming an agent have been given the general one. */
	generalPinned bool
	// install is the install-wide layer, from <data-dir>/settings.json. Read-only for a
	// workspace: it is changed through the workspace layer, which writes the file once for
	// every workspace to see.
	install Defaults
	// lastResort is what is left when no layer chose a model. It is computed from what the
	// credentials actually offer and is never persisted: a guess written into a workspace
	// would outrank the install default for good, which is the opposite of a fallback.
	lastResort protocol.ModelBinding
	// promptedAt remembers an accepted prompt per conversation, in Unix milliseconds. The
	// worker's run record lags behind the click and outlives dead runs; the stamp covers
	// both directions: just-prompted counts as busy before the record exists, and only a
	// record with recent activity counts after. A settlement clears its stamp, so the
	// grace ends when the run does rather than running its ten seconds out.
	promptedAt map[string]int64
	// actionAt remembers an accepted builtin per conversation, in Unix
	// milliseconds. A compaction is one bounded model task, not a turn, so no
	// run record ever marks it: the stamp keeps the busy sign up for up to five
	// minutes, and the worker's answer clears it sooner.
	actionAt map[string]int64
	// announced is the signature of the state attached clients were last told about. The poll
	// compares the state against it, because a change only time can make has no data behind
	// it: nothing refreshes, and only the comparison across ticks can see it.
	announced   string
	resolved    map[string]*protocol.ResolvedAgent
	catalog     protocol.AgentsData
	providers   []protocol.ProviderInfo
	policies    map[string]json.RawMessage
	views       map[string]cachedView
	subscribers map[int]chan protocol.Frame
	// staleSignals wakes one client per state change. It is a channel of its own because a
	// state frame is a signal, not a transcript: on the frame channel a slow write would drop
	// it, or the signal would evict a view frame the client has not read yet, and a client
	// that never hears the state moved keeps the state it read before — busy sign and all.
	staleSignals map[int]chan struct{}
	nextSub      int
	// Which conversation each subscriber watches, and how many watch each. Several
	// clients watching different conversations is what makes browser tabs work.
	subscriberConversation map[int]string
	attachments            map[string]int
	// drafts holds one unsent message per person per conversation, keyed by the harness's own id
	// and the address of the person typing. Loaded at open, persisted on every save: a draft
	// outlives the process that took it.
	drafts map[draftKey]Draft
	// landing is where each person lands, by address, and active is the workspace's fallback for a
	// caller with no landing of their own. One landing per workspace is what selecting used to mean,
	// and it made one person's move move everybody (ADR 0034 §7).
	landing map[string]string
	// secretRequests are tools waiting for a person to set a secret. The worker asks; the
	// card answers through the gateway, which owns the vault and the answer.
	secretMu       sync.Mutex
	secretRequests map[string]*pendingSecret
	// restarting says stopWorker is about to be followed by a new worker. The old pointer
	// stays in place until then so a concurrent call gets "not running" instead of a nil
	// dereference, and its exit notification is not mistaken for a crash to supervise.
	restarting bool
	// lifecycle serializes a prompt's admission with a pending secret restart. A restart
	// only starts when no run is active; a prompt accepted during one waits, and a restart
	// waits for an admitted prompt's run. It is not the worker's lifetime lock.
	lifecycle sync.Mutex
	// continuations are conversations waiting for a deferred secret before the automatic
	// continuation prompt. Kept until the sandbox restart succeeds.
	continuationMu sync.Mutex
	continuations  map[string][]string
}

// cachedView is the last full view per conversation, so switching back is instant
// rather than waiting for the worker's next frame. The annotation is cached with it:
// a view frame is more than its value, and dropping the annotation on the way to a
// newly attached client is invisible until attribution quietly stops working.
type cachedView struct {
	value      []byte
	annotation json.RawMessage
	seq        int64
}

func New(opts Options) *Control {
	return &Control{
		workerExited:           make(chan struct{}, 1),
		opts:                   opts,
		bindings:               map[string]protocol.ModelBinding{},
		resolved:               map[string]*protocol.ResolvedAgent{},
		views:                  map[string]cachedView{},
		subscribers:            map[int]chan protocol.Frame{},
		staleSignals:           map[int]chan struct{}{},
		subscriberConversation: map[int]string{},
		attachments:            map[string]int{},
		policies:               map[string]json.RawMessage{},
		promptedAt:             map[string]int64{},
		actionAt:               map[string]int64{},
		drafts:                 map[draftKey]Draft{},
		landing:                map[string]string{},
		secretRequests:         map[string]*pendingSecret{},
		continuations:          map[string][]string{},
	}
}

// Open starts the worker, reads the catalog, and attaches to the active
// conversation, creating the root conversation if the storage is empty.
//
// A harness that cannot start is not a workspace that cannot be used: the control still
// answers, saying the harness is down and why, and the supervisor keeps trying so that fixing
// the file is enough. Refusing to open hides the reason behind a process that never serves —
// which is what a guard with a typo in it used to do to the whole gateway.
func (c *Control) Open(ctx context.Context) {
	// Values are kept, cancellation is not: the caller may be a request that is about to end.
	c.lifetime, c.stop = context.WithCancel(context.WithoutCancel(ctx))
	// The drafts are this workspace's own words: read before the worker is asked for
	// anything, so a state is complete from the first client that asks.
	c.loadDrafts()
	if err := c.attachToWorker(ctx); err != nil {
		c.mu.Lock()
		c.workerUp = false
		c.workerError = err.Error()
		c.mu.Unlock()
		workerpool.Log("workspace %s: %v", c.opts.Workspace.Name, err)
		go c.supervise(c.lifetime)
		// Something for the supervisor to react to: it retries on a death, and this is the
		// first one.
		select {
		case c.workerExited <- struct{}{}:
		default:
		}
		c.publish(protocol.Frame{Type: "state"})
		return
	}
	go c.watchConversations(c.lifetime)
	go c.supervise(c.lifetime)
}

// stateStale reports whether the state has moved since attached clients were last told.
// Against the announcement, not against the previous tick's data: a change only time can
// make — the prompt grace expiring — happens between ticks, and two signatures taken
// milliseconds apart would never see it. The signature is read first and the announcement
// after, so a signal raised in between can only cause an announcement that says the same
// thing twice, never a missed one.
func (c *Control) stateStale() bool {
	signature := c.conversationSignature()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return signature != c.announced
}

// conversationSignature is what the interface sees, not the worker's raw record: the busy
// rule gates a record with a prompt stamp and a freshness window, so a flag that moved only
// because time passed is still a change the rail has to hear about — otherwise a client that
// read the state inside the grace keeps a busy sign nothing will ever take down.
func (c *Control) conversationSignature() string {
	c.mu.RLock()
	infos := append([]protocol.ConversationInfo(nil), c.conversations...)
	c.mu.RUnlock()
	now := time.Now().UnixMilli()
	var builder strings.Builder
	for _, info := range infos {
		fmt.Fprintf(&builder, "%s:%t:%s:%.0f|", info.ID, c.runActive(info, now), info.LastEntryID, info.Tokens)
	}
	return builder.String()
}

func (c *Control) workspaceList() []WorkspaceState {
	if c.opts.Registry == nil {
		return nil
	}
	return Views(c.opts.Registry(), c.opts.Progress)
}

// SetWorkspace applies a changed record: a folder that moved, or trust being granted.
// May answers whether this control's room lets a caller do something. It is the same table every handler asks,
// read under the lock because SetWorkspace replaces the record a membership change is written into.
func (c *Control) May(caller Caller, action Action) bool {
	c.mu.RLock()
	workspace := c.opts.Workspace
	c.mu.RUnlock()
	return workspace.May(caller, action)
}

func (c *Control) SetWorkspace(workspace Workspace) {
	c.mu.Lock()
	changed := c.opts.Workspace.Engine != workspace.Engine
	c.opts.Workspace = workspace
	c.mu.Unlock()
	if changed {
		// A different engine is a different manager, and the next worker start must resolve
		// it rather than reuse the one that belonged to the last record.
		c.engineMu.Lock()
		c.engineDone = false
		c.engineManager = nil
		c.engineProblem = nil
		c.engineMu.Unlock()
	}
}

// Commands lists this workspace's slash commands, global first and workspace shadowing.
func (c *Control) Commands(ctx context.Context) (json.RawMessage, error) {
	worker, err := c.currentWorker()
	if err != nil {
		return nil, err
	}
	return worker.Call(ctx, protocol.Command{Type: "commands"})
}

// Policy reports what a conversation may do with other conversations, resolved
// the same way its agent is.
func (c *Control) Policy(ctx context.Context, conversationID string) (json.RawMessage, error) {
	worker, err := c.currentWorker()
	if err != nil {
		return nil, err
	}
	return worker.Call(ctx, protocol.Command{Type: "policy", ConversationID: conversationID})
}

func (c *Control) Close() {
	// The loops stop first. A supervisor that sees its worker close takes that for a crash and
	// restarts it from the settings this control was built from, so closing in the other order
	// left a second worker running with the record that had just been replaced — over the same
	// storage as its replacement.
	if c.stop != nil {
		c.stop()
	}
	// After the loops: a worker that is still settling must not find its proxy gone.
	c.mu.Lock()
	stopProxy := c.proxyStop
	c.proxy, c.proxyStop = nil, nil
	c.mu.Unlock()
	if stopProxy != nil {
		stopProxy()
	}
	// The flag goes with the process. stopWorker has always cleared it; this path did not, so a
	// closed workspace went on reporting itself as running — and a list built after a shutdown
	// said every workspace was still up.
	c.mu.Lock()
	worker := c.worker
	c.workerUp = false
	c.mu.Unlock()
	if worker != nil {
		worker.Close()
	}
	// The worker is gone, so nothing can use the sandbox: stopping it frees the memory and
	// the engine's own idle timeout would do it anyway. Its disk stays, so a workspace that
	// installed dependencies yesterday still has them.
	if manager := c.sandboxManager(); manager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), sandbox.Timeout)
		defer cancel()
		if err := manager.Stop(ctx, c.opts.Workspace.Path); err != nil && !errors.Is(err, sandbox.ErrUnavailable) {
			workerpool.Log("stopping the sandbox for %s: %v", c.opts.Workspace.Name, err)
		}
	}
}

// Describe returns the cached worker self-report.
func (c *Control) Describe() protocol.Describe {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.describe
}

// State is everything a client needs for the conversation this caller lands on.
// StateFor is everything a client needs for one conversation. Each tab asks for its own,
// so two tabs can show two conversations without agreeing on an "active" one — and each
// person asks as themselves, because the landing conversation and the draft are theirs
// (ADR 0034 §7).
func (c *Control) StateFor(caller Caller, conversationID string) State {
	parts := c.stateParts(caller, conversationID)
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stateLocked(caller, parts)
}

// stateParts is what a state needs and cannot read under the snapshot lock. The effective
// binding is computed here because inputsFor takes its own read locks and they are not
// re-entrant.
//
// The registry lock is also taken here, before the snapshot's, never inside it. The eviction
// sweep takes the registry lock and then reads a control, so a state build that read the
// registry from inside this lock would close the cycle and hang every request behind it. Read
// it first, and the two orders agree. The defaults are resolved here for the same reason: they
// take the lock themselves.
type stateParts struct {
	active     string
	inputs     resolveInputs
	subagent   bool
	workspaces []WorkspaceState
	fallback   Defaults
	origins    DefaultOrigins
}

func (c *Control) stateParts(caller Caller, conversationID string) stateParts {
	active := conversationID
	if active == "" {
		active = c.landingFor(caller)
	}
	inputs, child := c.conversationInputs(active)
	workspaces := c.workspaceList()
	fallback, origins := c.effectiveDefaults()
	return stateParts{
		active:     active,
		inputs:     inputs,
		subagent:   child,
		workspaces: workspaces,
		fallback:   fallback,
		origins:    origins,
	}
}

// conversationInputs is the binding resolution, plus whether control owns this conversation. A
// child belongs to the harness, so what it stored wins over what this control would resolve.
func (c *Control) conversationInputs(active string) (resolveInputs, bool) {
	inputs := c.inputsFor(active)
	child := c.isSubagent(active)
	if !child {
		return inputs, false
	}
	if info, found := c.conversationInfo(active); found {
		if binding, ok := parseModelBinding(info.Model); ok {
			inputs.explicit.Provider, inputs.explicit.ModelID = binding.Provider, binding.ModelID
		}
		if info.Thinking != "" {
			inputs.explicit.ThinkingLevel = info.Thinking
		}
	}
	return inputs, true
}

// stateLocked assembles the snapshot for one caller. The caller holds the read lock.
func (c *Control) stateLocked(caller Caller, parts stateParts) State {
	active := parts.active
	workspace := c.opts.Workspace
	state := State{
		Workspace:     workspaceStateOf(workspace, c.workerUp),
		Role:          string(workspace.RoleOf(caller.Email)),
		Can:           canIn(workspace, caller),
		Workspaces:    parts.workspaces,
		WorkspacesDir: c.opts.WorkspacesDir,
		Engines:       c.engineList(),
		Harness:       c.describe.Provider,
		Capabilities:  c.describe.Capabilities,
		Conversations: c.toConversations(c.conversations),
		// Agent is the effective binding, so pickers show what will actually run;
		// Inherited says which parts of it this conversation chose itself.
		Agent:           parts.inputs.effective(),
		Subagent:        parts.subagent,
		Resolved:        c.resolved[active],
		DefaultModel:    parts.fallback.Model,
		DefaultThinking: parts.fallback.Thinking,
		DefaultAgent:    parts.fallback.Agent,
		DefaultsOrigin:  parts.origins,
		WorkspaceDefaults: ScopeDefaults{
			Model:    c.defaultModel,
			Thinking: c.defaultThinking,
			Agent:    c.defaultAgent,
		},
		InstallDefaults: ScopeDefaults{
			Model:    c.install.Model,
			Thinking: c.install.Thinking,
			Agent:    c.install.Agent,
		},
		WorkerUp:    c.workerUp,
		WorkerError: c.workerError,
		Sandbox:     c.sandbox,
		Update:      c.updateSummary(),
		Inherited: Inherited{
			Model:    c.bindings[active].Provider == "",
			Thinking: c.bindings[active].ThinkingLevel == "",
			Agent:    c.bindings[active].Definition == "",
		},
		Agents:    c.catalog.Agents,
		Skills:    c.catalog.Skills,
		Defaults:  c.catalog.Defaults,
		Providers: c.providers,
		Seq:       c.views[active].seq,
		Policy:    c.policies[active],
	}
	// The draft travels with the state, not with the transcript: it is the composer's, and
	// the conversation it belongs to is the one this state is about. A copy, so the client
	// reads a value rather than a map that a keystroke on another machine can change under it.
	// It is this caller's draft: somebody else's half-typed message is not theirs to see, and
	// showing it would put words in a composer that person never typed (ADR 0034 §7).
	if draft, found := c.drafts[draftKey{conversation: active, caller: caller.Email}]; found {
		state.Draft = &draft
	}
	// The conversation this call asked for, not the one the worker happens to be attached
	// to. They are the same only once the attach has landed, and a client that asks about a
	// conversation before attaching — which is every switch, and the state fetch that comes
	// with it — was told about a different one. Everything else here already used `active`;
	// this is the field that made the difference visible, because the client follows it.
	for index := range state.Conversations {
		if state.Conversations[index].ID == active {
			conversation := state.Conversations[index]
			state.Conversation = &conversation
			break
		}
	}
	return state
}

// WorkspacePath is the folder this control serves, for the answer endpoint's vault entry.
func (c *Control) WorkspacePath() string { return c.opts.Workspace.Path }

// notifyState wakes every attached client: the state it holds is stale, and the HTTP layer
// answers each one with a fresh state for its own conversation. A signal already waiting
// says the same thing, so a second one is not a second wakeup. What was announced is
// remembered here, so the poll can tell a change it has already announced from one only time
// has made.
func (c *Control) notifyState() {
	signature := c.conversationSignature()
	c.mu.Lock()
	c.announced = signature
	signals := make([]chan struct{}, 0, len(c.staleSignals))
	for _, signal := range c.staleSignals {
		signals = append(signals, signal)
	}
	c.mu.Unlock()
	for _, signal := range signals {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

type persistedSettings struct {
	Active string `json:"active,omitempty"`
	// Landing is where each person lands, by address (ADR 0034 §7). Active stays beside it as the
	// workspace's fallback: it is what a caller with no landing of their own opens, a new browser for
	// instance, and it is what an install written before landings were per person already had.
	Landing         map[string]string                `json:"landing,omitempty"`
	GeneralPinned   bool                             `json:"generalPinned,omitempty"`
	DefaultModel    protocol.ModelBinding            `json:"defaultModel,omitzero"`
	DefaultThinking string                           `json:"defaultThinking,omitempty"`
	DefaultAgent    string                           `json:"defaultAgent,omitempty"`
	Agents          map[string]protocol.ModelBinding `json:"agents,omitempty"`
}

// loadSettings reads our own state: the default model, the active conversation,
// and which conversations chose a model. It must run before ensureDefault, or
// ensureDefault derives a default and overwrites the one on disk.
func (c *Control) loadSettings() string {
	// The install layer first, and whatever this workspace's own file says: a workspace that
	// has never saved settings has no file, and it still follows the install's defaults.
	c.loadInstallDefaults()
	settings, ok := c.readSettings()
	if !ok {
		return ""
	}
	c.applySettings(settings)
	return settings.Active
}

// readSettings reads this workspace's own file. No file, and a file nobody can read, are the
// same answer: the install's defaults stand.
func (c *Control) readSettings() (persistedSettings, bool) {
	if c.opts.SettingsPath == "" {
		return persistedSettings{}, false
	}
	payload, err := os.ReadFile(c.opts.SettingsPath)
	if err != nil {
		return persistedSettings{}, false
	}
	var settings persistedSettings
	if err := json.Unmarshal(payload, &settings); err != nil {
		return persistedSettings{}, false
	}
	return settings, true
}

// applySettings writes the file's layers into memory under one lock: half-applied settings are
// a state nobody chose, and none of it does I/O.
func (c *Control) applySettings(settings persistedSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if settings.DefaultModel.Provider != "" && settings.DefaultModel.ModelID != "" {
		c.defaultModel = protocol.ModelBinding{
			Provider: settings.DefaultModel.Provider,
			ModelID:  settings.DefaultModel.ModelID,
		}
	}
	c.defaultThinking = settings.DefaultThinking
	c.defaultAgent = settings.DefaultAgent
	c.generalPinned = settings.GeneralPinned
	for caller, landing := range settings.Landing {
		if landing != "" {
			c.landing[caller] = landing
		}
	}
	for id, binding := range settings.Agents {
		if binding.Provider == "" && binding.ModelID == "" && binding.Definition == "" {
			continue
		}
		c.bindings[id] = binding
	}
}

func (c *Control) persist() {
	if c.opts.SettingsPath == "" {
		return
	}
	c.mu.RLock()
	// This workspace's own layer, not the resolved one: writing the resolved value here
	// would turn the install's default, or a guess, into this workspace's choice and it
	// would outrank every layer below for good.
	settings := persistedSettings{
		Active:          c.active,
		Landing:         map[string]string{},
		GeneralPinned:   c.generalPinned,
		DefaultModel:    c.defaultModel,
		DefaultThinking: c.defaultThinking,
		DefaultAgent:    c.defaultAgent,
		Agents:          map[string]protocol.ModelBinding{},
	}
	for caller, landing := range c.landing {
		if landing != "" {
			settings.Landing[caller] = landing
		}
	}
	for id, binding := range c.bindings {
		if binding.Provider == "" && binding.ModelID == "" && binding.Definition == "" {
			continue
		}
		settings.Agents[id] = binding
	}
	c.mu.RUnlock()

	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	temp := c.opts.SettingsPath + ".tmp"
	if err := os.WriteFile(temp, append(payload, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(temp, c.opts.SettingsPath)
}
