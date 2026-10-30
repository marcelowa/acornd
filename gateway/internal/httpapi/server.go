// the server, and the shape every workspace-scoped handler has
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/identity"
	"github.com/acornfall/acornfall/gateway/internal/people"
	"github.com/acornfall/acornfall/gateway/internal/update"
	"io"
	"io/fs"
	"net/http"
)

type Server struct {
	workspaces *control.Workspaces
	static     fs.FS
	update     *update.Manager
	gate       Gate
}

// Gate is what the edge admits by: the door that proves who is calling, and the book of people it has to
// know once it has.
//
// The zero Gate is the single admin ADR 0020 describes — no door, no list, every request answered —
// and it is the default on purpose: a guard nothing configured is how a switched-off guard comes back.
type Gate struct {
	Door   identity.Door
	People *people.Book
	// Dir is the install folder. The remote-access screen reads and writes remote.json there, and nothing else
	// on the edge needs a path at all.
	Dir string
}

func New(workspaces *control.Workspaces, static fs.FS, updates *update.Manager, gate Gate) *Server {
	return &Server{workspaces: workspaces, static: static, update: updates, gate: gate}
}

// controlFor finds the workspace a request is about. The workspace-scoped routes name it;
// the earlier paths do not, and mean the caller's own workspace.
func (s *Server) controlFor(r *http.Request) (*control.Control, error) {
	name := r.PathValue("ws")
	if name == "" {
		name = r.URL.Query().Get("workspace")
	}
	_, ctl, err := s.workspaces.Route(r.Context(), callerOf(r), name)
	if err != nil {
		return nil, err
	}
	return ctl, nil
}

// The shape every workspace-scoped handler has, in one place. Fifteen of them were the same
// four error checks, which is what the duplication report was pointing at; and the checks are
// the part a reader skips, so a handler that says only what it does reads better.
//
// A mutation answers with the state, deliberately: the client shows what changed instead of
// assuming it, and a reconnect needs nothing else.

// withControl finds the workspace a request is about. ok is false when the answer has already
// been written: there is no such workspace, and the sentence says which.
func (s *Server) withControl(w http.ResponseWriter, r *http.Request) (*control.Control, bool) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return nil, false
	}
	return ctl, true
}

// statusFor is the status a failed lookup gets. A workspace that does not exist and one the caller is not
// a member of answer the same thing, so a status cannot be used to enumerate the workspaces somebody cannot
// see (ADR 0032 §4); everything else that refuses a workspace is a conflict, as it always was.
func statusFor(err error) int {
	if errors.Is(err, control.ErrNoWorkspace) {
		return http.StatusNotFound
	}
	if errors.Is(err, control.ErrNotAllowed) {
		return http.StatusForbidden
	}
	return http.StatusConflict
}

// stateOf is the state a caller gets. The install's two answers — am I its admin, may I lead a room — come from
// the book, which is the edge's file; control builds the room's answer and cannot read people.json (ADR 0002).
func (s *Server) stateOf(ctl *control.Control, caller control.Caller, conversation string) control.State {
	state := ctl.StateFor(caller, conversation)
	state.Admin = caller.Admin
	state.Can.MakeRoom = caller.Admin
	if !caller.Admin && caller.Email != "" && s.gate.People != nil {
		if person, found, err := s.gate.People.Find(caller.Email); err == nil && found {
			state.Can.MakeRoom = person.CanCreate
		}
	}
	return state
}

// mayRoom refuses an action this room keeps for its owner. The table is control's; this only decides the status
// and the sentence, because a 403 here is "the room says no" and a 404 would be "there is no such room".
func (s *Server) mayRoom(w http.ResponseWriter, r *http.Request, ctl *control.Control, action control.Action) bool {
	if ctl.May(callerOf(r), action) {
		return true
	}
	http.Error(w, control.ErrNotAllowed.Error(), http.StatusForbidden)
	return false
}

// stateChange runs one mutation on the workspace and answers with its state.
func (s *Server) stateChange(w http.ResponseWriter, r *http.Request, change func(*control.Control) error) {
	ctl, ok := s.withControl(w, r)
	if !ok {
		return
	}
	if err := change(ctl); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.stateOf(ctl, callerOf(r), ""))
}

// stateBody is stateChange with one JSON body read first. A body that will not decode has
// already been answered.
func stateBody[B any](s *Server, w http.ResponseWriter, r *http.Request, change func(*control.Control, B) error) {
	bodyChange(s, w, r, func(ctl *control.Control, body B) (bool, error) {
		if err := change(ctl, body); err != nil {
			return false, err
		}
		writeJSON(w, http.StatusOK, s.stateOf(ctl, callerOf(r), ""))
		return true, nil
	})
}

// ownAnswer is stateBody for a handler whose answer is its own value rather than the state: a
// draft's answer is the stored draft, and a keystroke is not a change to anything else. The
// change writes the answer itself and says so, so the state is not written after it — two JSON
// documents on one response is a body no client can parse.
func ownAnswer[B any](s *Server, w http.ResponseWriter, r *http.Request, change func(*control.Control, B) error) {
	bodyChange(s, w, r, func(ctl *control.Control, body B) (bool, error) {
		return true, change(ctl, body)
	})
}

// bodyChange is the shared half: the workspace, the body, and the error the caller gets for a
// mutation it refused. answered is whether the change wrote its own response.
func bodyChange[B any](s *Server, w http.ResponseWriter, r *http.Request, change func(*control.Control, B) (bool, error)) {
	ctl, ok := s.withControl(w, r)
	if !ok {
		return
	}
	var body B
	if !decode(w, r, &body) {
		return
	}
	if _, err := change(ctl, body); err != nil {
		http.Error(w, err.Error(), statusFor(err))
	}
}

// view answers with what a read produced.
func view[T any](s *Server, w http.ResponseWriter, r *http.Request, read func(*control.Control) (T, error)) {
	ctl, ok := s.withControl(w, r)
	if !ok {
		return
	}
	result, err := read(ctl)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) workspaceList(w http.ResponseWriter, r *http.Request) {
	// The folder is reported with the list, so the interface can say where a new workspace
	// will be created instead of asking for a path it should not be given.
	writeJSON(w, http.StatusOK, s.listingFor(callerOf(r)))
}

// listing is the one shape every workspace endpoint answers with, so a caller never has to
// know which operation produced it.
func (s *Server) listing() map[string]any {
	return s.listingFor(control.Caller{Admin: true})
}

// listingFor is the list a caller may see: the whole install for its admin, their own workspaces for a
// person. A person's rail showing rooms they cannot open would be worse than one that shows nothing.
func (s *Server) listingFor(caller control.Caller) map[string]any {
	return map[string]any{
		"workspacesDir": s.workspaces.Dir(),
		"workspaces":    control.Views(s.workspaces.ListFor(caller), s.workspaces.Progress),
		// Every engine this build can run and whether this machine can: the chooser shows all
		// of them, with the sentence that says why one cannot be used here.
		"engines": s.workspaces.Engines(),
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// noCache keeps a reload honest while the client has no build step and no
// content hashes: a stale bundle is indistinguishable from a broken one.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.offerable(s.stateOf(ctl, callerOf(r), r.URL.Query().Get("conversation")), callerOf(r)))
}

// workspaceStatus answers where a workspace is, and never starts it: opening a workspace is the
// request that takes seconds, so the interface asks this first and shows the step it is on
// instead of an empty pane. The status also rides every state frame, for the list.
func (s *Server) workspaceStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("ws")
	if name == "" {
		name = r.URL.Query().Get("workspace")
	}
	if name == "" {
		name = control.PersonalWorkspaceName
	}
	if _, known := s.workspaces.Get(name); !known {
		http.Error(w, "no workspace "+name, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.workspaces.Progress(name))
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeEvent(w http.ResponseWriter, name string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("event: " + name + "\ndata: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}
