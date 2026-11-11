package control

import (
	"context"
	"errors"
	"testing"
)

// A bundle is the room owner's or the install admin's. A guest is in the room and can read it;
// a support bundle names the machine's cgroups and logs, which the room does not own.
func TestSupportBundleIsTheOwnersAct(t *testing.T) {
	control := New(Options{
		Workspace: Workspace{
			Name: "room",
			Path: t.TempDir(),
			Kind: KindRoom,
			Members: []Member{
				{Email: "owner@example.com", Role: RoleOwner},
				{Email: "guest@example.com", Role: RoleGuest},
			},
		},
		DataDir: t.TempDir(),
	})
	for _, test := range []struct {
		name   string
		caller Caller
		ok     bool
	}{
		{"owner", Caller{Email: "owner@example.com"}, true},
		{"admin", Caller{Admin: true, Email: "admin@example.com"}, true},
		{"guest", Caller{Email: "guest@example.com"}, false},
		{"stranger", Caller{Email: "nobody@example.com"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview, err := control.SupportPreview(context.Background(), test.caller)
			if test.ok {
				if err != nil {
					t.Fatalf("preview: %v", err)
				}
				if preview.Workspace != "room" || len(preview.Files) == 0 {
					t.Fatalf("preview = %+v", preview)
				}
				found := false
				for _, file := range preview.Files {
					if file.Name == "workspace/state.json" {
						found = true
					}
				}
				if !found {
					t.Fatalf("a console bundle should carry the live state: %+v", preview.Files)
				}
				return
			}
			if !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("want ErrNotAllowed, got %v", err)
			}
		})
	}
}

// The rail only offers a bundle on a room the caller can open: owning the install does not make
// somebody a member of another person's room (ADR 0034 §1), so the console route would 404.
func TestSupportIsOfferedOnlyOnRoomsTheCallerCanOpen(t *testing.T) {
	room := Workspace{Name: "room", Path: t.TempDir(), Kind: KindRoom, Members: []Member{{Email: "owner@example.com", Role: RoleOwner}}}
	views := ViewsFor([]Workspace{room}, Caller{Admin: true, Email: "admin@example.com"}, nil)
	if len(views) != 1 || views[0].CanAdminister {
		t.Fatalf("the admin is not in this room: %+v", views)
	}
	views = ViewsFor([]Workspace{room}, Caller{Email: "owner@example.com"}, nil)
	if len(views) != 1 || !views[0].CanAdminister {
		t.Fatalf("the owner runs this room: %+v", views)
	}
}
