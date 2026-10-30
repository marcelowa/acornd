// workspace settings: the body create and update share, and the listing
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"fmt"
	"github.com/acornfall/acornfall/gateway/internal/control"
	"io"
	"net/http"
	"strings"
)

// workspaceCreate makes a workspace inside the workspaces folder. It takes a name, not a
// path: the interface is scoped to one directory on purpose, and a folder anywhere else on
// the machine is an operator's decision rather than a form field.
// workspaceBody is the create and update body. A pointer is "not mentioned", which is what makes
// an update partial; create reads the same fields, because a setting is a setting whether it is
// set at birth or later.
type workspaceBody struct {
	Name string `json:"name"`
	// Title is the pointer on update and the derived value on create; see the two call sites.
	Title *string `json:"title"`
	Path  *string `json:"path"`
	// Image is the root filesystem the sandbox boots from.
	Image *string `json:"image"`
	// Network is "on" or "off", for a workspace whose tools should reach the network at all.
	Network *string `json:"network"`
	// Private lets the sandbox reach private networks as this machine: its LAN, link-local
	// addresses, and its tailnet. Absent means no, which is what every workspace gets unless
	// it asks. See docs/adr/0025-sandbox-egress.md.
	Private *bool `json:"private"`
	// Engine is which sandbox engine this workspace runs on: microsandbox, bwrap, or
	// host. Absent means the install's default. See docs/adr/0022 and 0023.
	Engine *string `json:"engine"`
	// KeepAlive keeps this workspace's sandbox running when nobody is watching it, which is
	// what a workspace that runs a daemon or a cron job needs.
	KeepAlive *bool `json:"keep_alive"`
	// DisableVMNesting, when true, keeps this workspace's sandbox from nesting; absent or
	// false nests whenever the engine and the machine can.
	DisableVMNesting *bool `json:"disable_vm_nesting"`
	// DisableDevices, when true, withholds /dev/net/tun and /dev/kvm from a bwrap
	// workspace. Absent means the default: the devices are granted.
	DisableDevices *bool `json:"disable_devices"`
	Trusted        *bool `json:"trusted"`
}

// refusal is why a body is not one this build accepts. Refused rather than ignored, because a
// field that does nothing is worse than a request that fails: a workspace used to carry a trust
// flag that decided whether its own resources were read, and it does not any more.
func (b workspaceBody) refusal() string {
	switch {
	case b.Trusted != nil:
		return "every workspace's own agents, skills, commands, and rules are read; there is no trust flag"
	case b.Network != nil && !control.ValidNetwork(*b.Network):
		return `network is "on" or "off"`
	case b.Engine != nil && !control.ValidEngine(*b.Engine):
		// Named, never defaulted: a workspace that asked for a sandbox and got another engine
		// would be a security change nobody chose.
		return "sandbox engine is one of " + strings.Join(control.EngineNames(), ", ")
	}
	return ""
}

// apply writes the fields the body names onto a workspace. Title stays out of it: create keeps
// the derived one when the body's is empty, and update clears it — two rules, two call sites.
func (b workspaceBody) apply(workspace control.Workspace) control.Workspace {
	if b.Image != nil {
		workspace.Image = *b.Image
	}
	if b.Network != nil {
		workspace.Network = *b.Network
	}
	if b.Private != nil {
		workspace.Private = *b.Private
	}
	if b.Engine != nil {
		workspace.Engine = *b.Engine
	}
	if b.KeepAlive != nil {
		workspace.KeepAlive = *b.KeepAlive
	}
	if b.DisableVMNesting != nil {
		workspace.DisableVMNesting = b.DisableVMNesting
	}
	if b.DisableDevices != nil {
		workspace.DisableDevices = b.DisableDevices
	}
	return workspace
}

// mayMakeRoom is `can_create`: the admin makes rooms always, and a member only when their record says so. A
// room is the only thing there is to share, so the permission is the install's answer to "may this person lead
// one", not a room role (ADR 0034 §3).
func (s *Server) mayMakeRoom(w http.ResponseWriter, r *http.Request) bool {
	caller := callerOf(r)
	if caller.Admin {
		return true
	}
	if caller.Email == "" || s.gate.People == nil {
		http.Error(w, "only the install's admin may make rooms here", http.StatusForbidden)
		return false
	}
	person, found, err := s.gate.People.Find(caller.Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return false
	}
	if !found || !person.CanCreate {
		http.Error(w, "this install does not let you make rooms: ask its admin to allow it", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) workspaceCreate(w http.ResponseWriter, r *http.Request) {
	if !s.mayMakeRoom(w, r) {
		return
	}
	var body workspaceBody
	if !decode(w, r, &body) {
		return
	}
	if body.Path != nil {
		// Refused rather than ignored: accepting and dropping it would be a field that does
		// nothing, and pointing at a folder elsewhere is the operator's command.
		http.Error(w, "workspaces are created inside "+s.workspaces.Dir()+"; use `acornfall workspace add` for a folder elsewhere", http.StatusBadRequest)
		return
	}
	if refusal := body.refusal(); refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	// The creator is the caller's address, and a room is born with its owner (ADR 0034 §3): a room nobody owns
	// is refused to everybody, its maker included, because being in the room is what allows a caller.
	workspace, err := s.workspaces.Create(body.Name, callerOf(r).Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	workspace.Title = firstNonEmpty(derefString(body.Title), workspace.Title)
	if body.Engine == nil {
		// Recorded, not left blank: a blank engine means "whatever this machine prefers", and a
		// workspace that woke up on a different machine would then run somewhere nobody chose.
		// The preference is resolved once, here, where the machine that answered is the machine
		// the workspace was made on.
		engine := s.workspaces.DefaultEngine()
		body.Engine = &engine
	}
	if err := s.workspaces.Update(body.apply(workspace)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.listingFor(callerOf(r)))
}

func (s *Server) workspaceUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("ws")
	existing, ok := s.workspaces.Get(name)
	if !ok {
		http.Error(w, fmt.Sprintf("no workspace %q", name), http.StatusNotFound)
		return
	}
	// The same body as create: a pointer is "not mentioned", so a partial update cannot
	// silently clear a field it did not name. No path, for the same reason as create.
	var body workspaceBody
	if !decode(w, r, &body) {
		return
	}
	if refusal := body.refusal(); refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	if body.Title != nil {
		existing.Title = *body.Title
	}
	if err := s.workspaces.Update(body.apply(existing)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.listing())
}

// workspaceRemove forgets a room. It is the room's owner's act, not the install's: the admin's route to the same
// power is `acornfall workspace remove` on the machine, where it is visible (ADR 0034 §6). A room the caller is
// not in is a 404, the same refusal as one that does not exist, so a status cannot enumerate.
func (s *Server) workspaceRemove(w http.ResponseWriter, r *http.Request) {
	caller := callerOf(r)
	workspace, ok := s.workspaces.Get(r.PathValue("ws"))
	if !ok || !workspace.Allows(caller) {
		http.Error(w, "no workspace", http.StatusNotFound)
		return
	}
	if !workspace.May(caller, control.ActionAdminister) {
		http.Error(w, control.ErrNotAllowed.Error(), http.StatusForbidden)
		return
	}
	if err := s.workspaces.Remove(r.Context(), workspace.Name); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.listingFor(caller))
}

// addMember invites somebody the book already names into a room. A name the book does not hold is refused with
// the sentence that says who can add them: an invite that the door will refuse is worse than a refusal now.
func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &body) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		http.Error(w, "an invitation needs an address", http.StatusBadRequest)
		return
	}
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people yet: run `acornfall people claim you@example.com` first", http.StatusBadRequest)
		return
	}
	if _, found, err := s.gate.People.Find(email); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	} else if !found {
		http.Error(w, email+" is not in People: ask the install's admin to add them first", http.StatusBadRequest)
		return
	}
	if _, err := s.workspaces.AddGuest(callerOf(r), r.PathValue("ws"), email); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.listingFor(callerOf(r)))
}

// removeMember takes a guest out, or lets a guest leave. The owner cannot leave: ending a room is forgetting it.
func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	if _, err := s.workspaces.RemoveMember(callerOf(r), r.PathValue("ws"), r.PathValue("email")); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.listingFor(callerOf(r)))
}

// page serves the client for a conversation route, so a reload and a shared link both
// land on the same conversation.
func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	file, err := s.static.Open("index.html")
	if err != nil {
		http.Error(w, "client not found", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, file)
}
