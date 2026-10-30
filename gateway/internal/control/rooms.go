// The room's record: who is in it, what they are in it, and whether it is somebody's own.
//
// A room is the screen word for a workspace. ADR 0034 gives each axis its own roles — the install has an
// admin and its members, and a room has an owner and its guests — and this file is the room's half. It is
// also the one place that reads a member list, so no caller has to work out what a list means.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The two kinds a workspace can be. It is a stored kind rather than a derived one because a room is born
// with its owner (ADR 0034 §3), so one member no longer tells somebody's own room from a room nobody has
// shared yet — and the two rules that follow from it, never shared and never forgotten, are enforced.
const (
	KindPersonal = "personal"
	KindRoom     = "room"
)

// Role is what somebody is in a room, and never what they are on the install: a room's owner cannot add a
// provider key, and the install's admin is a guest in somebody else's room until they are invited
// (ADR 0034 §1).
type Role string

const (
	// RoleOwner is the person a room belongs to. They invite and remove guests, and they never leave.
	RoleOwner Role = "owner"
	// RoleGuest is everybody else in a room: they speak in conversations it already has, and they cannot
	// make, fork, rename or reset one (ADR 0034 §4).
	RoleGuest Role = "guest"
)

// Member is one person in a room: their address, and what they are in it.
type Member struct {
	// Email is the address, lowercase: the book names people by address, and two spellings of one
	// address are one person.
	Email string `json:"email"`
	Role  Role   `json:"role"`
}

// UnmarshalJSON reads a member from either shape this install has written: the bare address a room's list
// held before rooms had roles, and the record it holds now.
//
// An address on its own is read with no role rather than refused, and that empty role is what tells the
// migration which records it still has to decide a role for (ADR 0034 §8). A registry is a file people
// keep; reading it is where a shape change is paid, not a roadblock in front of the person's install.
func (m *Member) UnmarshalJSON(payload []byte) error {
	if len(payload) > 0 && payload[0] == '"' {
		var email string
		if err := json.Unmarshal(payload, &email); err != nil {
			return err
		}
		*m = Member{Email: email}
		return nil
	}
	type member Member // a type of its own, so this is the struct's decoder and not this function again
	var plain member
	if err := json.Unmarshal(payload, &plain); err != nil {
		return err
	}
	*m = Member(plain)
	return nil
}

// Personal reports whether this is somebody's own room: never shared, and never forgotten.
//
// ADR 0032 §9 removed the stored flag because membership answered the same question, and ADR 0034 §5 put the
// kind back for the reason the flag was removed: a room is born with its owner, so one member no longer
// means "somebody's own room" — it also means "made and not shared yet", and calling that personal would
// make every fresh room un-shareable and un-forgettable.
func (w Workspace) Personal() bool { return w.Kind == KindPersonal }

// Owner is the address the room belongs to, and "" when it has none. An install with no admin claimed has a
// room nobody is named in yet, and the load leaves it that way rather than inventing an address.
func (w Workspace) Owner() string {
	for _, member := range w.Members {
		if member.Role == RoleOwner {
			return member.Email
		}
	}
	return ""
}

// RoleOf is what this address is in the room — its owner, a guest, or "" when it is not in it — which is the
// caller's role when the address is the caller's. Callers ask this instead of reading the list, so that one
// place knows what a role means and what a missing one is.
func (w Workspace) RoleOf(email string) Role {
	address := strings.ToLower(strings.TrimSpace(email))
	if address == "" {
		return ""
	}
	for _, member := range w.Members {
		if member.Email == address {
			return member.Role
		}
	}
	return ""
}

// Shared reports whether more than one person is in the room, which is the whole of when a speaker has to be
// named (ADR 0032 §7): one voice needs no prefix.
func (w Workspace) Shared() bool { return len(w.Members) > 1 }

// Allows reports whether a caller may use this room.
//
// Being in the room is the whole of it: a guest speaks, and what a guest may not do is a rule inside the
// room rather than a door in front of it (ADR 0034 §4). The admin is not special here — the install is
// theirs, not other people's rooms — and the one exception is an install where no admin has been claimed,
// which is the machine talking to itself before anybody is named.
func (w Workspace) Allows(caller Caller) bool {
	if strings.TrimSpace(caller.Email) == "" {
		return caller.Admin
	}
	return w.RoleOf(caller.Email) != ""
}

// memberProblem is what is wrong with the members this record names: a room has exactly one owner, and a
// personal room is one person's.
//
// It answers to the same rule whether the record is being read or written, so there is one sentence for one
// invariant rather than a load path and a write path that disagree. The one shape it tolerates is a room with no
// members at all: an install with no admin claimed is the machine talking to itself, and a room it makes is
// nobody's yet — every caller with an address is refused it, and the load adopts it once an admin exists
// (ADR 0034 §8).
func (w Workspace) memberProblem() error {
	if w.Personal() {
		return personalProblem(w.Members)
	}
	return roomProblem(w.Members)
}

// personalProblem is the rule for somebody's own room: one person in it, and that person is its owner.
func personalProblem(members []Member) error {
	if len(members) > 1 {
		return fmt.Errorf("a personal room is never shared, and this one names %d people", len(members))
	}
	for _, member := range members {
		if member.Role != RoleOwner {
			return fmt.Errorf("a personal room is never shared, and it names %q as a %s", member.Email, member.Role)
		}
	}
	return nil
}

// roomProblem is the rule for a room: one owner, any number of guests, and no member whose role is neither.
func roomProblem(members []Member) error {
	owners := 0
	for _, member := range members {
		switch member.Role {
		case RoleOwner:
			owners++
		case RoleGuest:
		default:
			return fmt.Errorf("%q is %q in this room, and a room names its owner and its guests", member.Email, member.Role)
		}
	}
	if owners > 1 {
		return fmt.Errorf("a room has exactly one owner, and this one names %d", owners)
	}
	if owners == 0 && len(members) > 0 {
		return errors.New("a room with guests and no owner is not a room: nobody in it could invite or remove anybody")
	}
	return nil
}

// migrated is one record read into the shape this build writes, and what reading it took.
type migrated struct {
	workspace Workspace
	changed   bool
	// guessed says the pass had to decide roles for an address list, which can only be a registry
	// somebody edited by hand: no shipped code ever wrote a room with more than one address in it.
	guessed bool
	// ownerless says the room has nobody in it and there is no admin to adopt it, which is an install
	// nobody has claimed yet rather than a record to correct.
	ownerless bool
}

// migrateRooms is the load's one pass over the record: every member gets a role, and every room a kind.
//
// It reads the shapes this install has actually written, and nothing else (ADR 0034 §8): an address list,
// because that is what Members was, and none at all. One address was somebody's own room — CreatePersonal
// made it, or it is the install's own — and no members was a room made before there were people, which the
// admin's claim fills in. It answers with what it changed, and with the rooms whose reading was a guess or
// that it had to leave with nobody in them, so the load can say each one in a line.
func (w *Workspaces) migrateRooms() (changed bool, guessed, ownerless []string) {
	admin := strings.ToLower(strings.TrimSpace(w.opts.AdminEmail))
	w.mu.Lock()
	defer w.mu.Unlock()
	for name, workspace := range w.registry {
		record := workspace.migrateRoom(admin)
		if record.changed {
			w.registry[name] = record.workspace
			changed = true
		}
		if record.guessed {
			guessed = append(guessed, name)
		}
		if record.ownerless {
			ownerless = append(ownerless, name)
		}
	}
	// Sorted, so a registry with two odd records says the same thing on every run: a map's iteration order
	// is not a decision, and a log whose order changes for no reason is a log nobody diffs.
	sort.Strings(guessed)
	sort.Strings(ownerless)
	return changed, guessed, ownerless
}

// migrateRoom is one record: the members lowercased and given their roles, and the kind its own evidence
// supports.
func (w Workspace) migrateRoom(admin string) migrated {
	members, unnamed, changed := membersWithRoles(w.Members)
	kind := kindOf(w.Name, w.Kind, len(members), unnamed)
	if unnamed {
		decideRoles(members)
	}
	ownerless := false
	if len(members) == 0 && admin != "" {
		// Nobody is named and the admin is known: the claim fills the room in, which is the only answer there is.
		members, changed = []Member{{Email: admin, Role: RoleOwner}}, true
	} else if len(members) == 0 && kind != KindPersonal {
		// An install with no admin claimed is the pre-claim machine, and the room it left is nobody's yet. The
		// install's own room is not reported: that is what every install without a door looks like.
		ownerless = true
	}
	room := w
	room.Kind = kind
	room.Members = members
	return migrated{
		workspace: room,
		changed:   changed || kind != w.Kind,
		guessed:   unnamed && len(members) > 1,
		ownerless: ownerless,
	}
}

// membersWithRoles is the member list as this build writes it — addresses trimmed and lowercased — and whether
// any of them arrived without a role, which is what tells the migration it is reading an address list.
func membersWithRoles(members []Member) (out []Member, unnamed, changed bool) {
	out = make([]Member, 0, len(members))
	for _, member := range members {
		email := strings.ToLower(strings.TrimSpace(member.Email))
		if email == "" {
			// An address that is nothing is not a person, so there is no reading of it that is not a guess.
			changed = true
			continue
		}
		if email != member.Email || member.Role == "" {
			changed = true
		}
		if member.Role == "" {
			unnamed = true
		}
		out = append(out, Member{Email: email, Role: member.Role})
	}
	return out, unnamed, changed
}

// kindOf is the kind a record's own evidence supports: the install's own room is personal by name, one bare
// address is somebody's own room because CreatePersonal is the only thing that ever wrote one, and anything else
// is a room.
func kindOf(name, kind string, members int, unnamed bool) string {
	switch {
	case name == PersonalWorkspaceName:
		return KindPersonal
	case kind != "":
		return kind
	case unnamed && members == 1:
		return KindPersonal
	}
	return KindRoom
}

// decideRoles fills in the role an address list never had: the first is the owner and the rest are guests.
func decideRoles(members []Member) {
	for index := range members {
		if members[index].Role != "" {
			continue
		}
		members[index].Role = RoleOwner
		if index > 0 {
			members[index].Role = RoleGuest
		}
	}
}
