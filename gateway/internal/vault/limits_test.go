package vault

import (
	"strings"
	"testing"
)

// The limits, at their boundaries. Each one exists so that a vault file cannot be made to refuse
// to load, or a secret to be stored that no list can show — and each is a number somebody chose,
// which is exactly what an off-by-one mutation moves.

func TestTheSizeLimitsAreAtTheNumberTheySay(t *testing.T) {
	store := openTestStore(t)
	cases := []struct {
		name string
		in   Upsert
		ok   bool
		want string
	}{
		{
			name: "a name at its limit",
			in:   Upsert{Name: strings.Repeat("A", maxNameBytes), Description: "d", Value: "v"},
			ok:   true,
		},
		{
			name: "a name one byte over",
			in:   Upsert{Name: strings.Repeat("A", maxNameBytes+1), Description: "d", Value: "v"},
			want: "name",
		},
		{
			name: "a description at its limit",
			in:   Upsert{Name: "OK", Description: strings.Repeat("d", maxDescBytes), Value: "v"},
			ok:   true,
		},
		{
			name: "a description one byte over",
			in:   Upsert{Name: "OK", Description: strings.Repeat("d", maxDescBytes+1), Value: "v"},
			want: "description",
		},
		{
			name: "a value at its limit",
			in:   Upsert{Name: "OK", Description: "d", Value: strings.Repeat("v", maxValueBytes)},
			ok:   true,
		},
		{
			name: "a value one byte over",
			in:   Upsert{Name: "OK", Description: "d", Value: strings.Repeat("v", maxValueBytes+1)},
			want: "value",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.in.Scope = Global
			_, err := store.Upsert(test.in)
			if test.ok {
				if err != nil {
					t.Fatalf("should be accepted: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("should be refused")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("the refusal should name %q: %v", test.want, err)
			}
		})
	}
}

// A host is what keeps a value from reaching anywhere else, so the alphabet is the rule: lowercase
// letters, digits and the hyphen, with a leading "*." as the one wildcard.
func TestAHostIsOnlyWhatAHostMayBe(t *testing.T) {
	accepted := []string{"example.com", "api.example.com", "a", "x1.example.com", "host-1.example.com", "*.example.com"}
	for _, host := range accepted {
		if err := validateHost(host); err != nil {
			t.Errorf("%q should be a host: %v", host, err)
		}
	}
	refused := []string{
		"*",                              // every host is not a host list
		"*.",                             // a wildcard with nothing after it
		"api.*.example.com",              // a wildcard in the middle
		"API.example.com",                // the caller lowercases first; a capital here is not a hostname
		"example..com",                   // an empty label
		"-example.com",                   // a label starting with a hyphen
		"example-.com",                   // a label ending with one
		"exa mple.com",                   // a space
		"example.com/path",               // a path is not a host
		"example.com:443",                // and neither is a port
		strings.Repeat("a", 64) + ".com", // a label over 63
	}
	for _, host := range refused {
		if err := validateHost(host); err == nil {
			t.Errorf("%q should be refused", host)
		}
	}
	// The total length limit is a DNS name's: 253 bytes.
	tooLong := strings.Repeat("a.", 127) + "com"
	if len(tooLong) <= maxHostBytes {
		t.Skipf("fixture is %d bytes, under the %d limit", len(tooLong), maxHostBytes)
	}
	if err := validateHost(tooLong); err == nil {
		t.Error("a name over 253 bytes should be refused")
	}
}

// The list is by name, then by scope, so two secrets with one name are in a stable order no matter
// what order the file happens to hold them in.
func TestTheListIsOrderedAndStable(t *testing.T) {
	store := openTestStore(t)
	for _, in := range []Upsert{
		{Scope: Global, Name: "BETA", Description: "d", Value: "v"},
		{Scope: Global, Name: "ALPHA", Description: "d", Value: "v"},
		{Scope: Workspace, Name: "ALPHA", Description: "d", Value: "v", Workspace: t.TempDir()},
	} {
		if _, err := store.Upsert(in); err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
	}
	metas, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("listed %d secrets", len(metas))
	}
	if metas[0].Name != "ALPHA" || metas[1].Name != "ALPHA" || metas[2].Name != "BETA" {
		t.Errorf("order = %s, %s, %s", metas[0].Name, metas[1].Name, metas[2].Name)
	}
	// Same name, so the scope decides: global sorts before workspace.
	if metas[0].Scope != Global || metas[1].Scope != Workspace {
		t.Errorf("same name should order by scope, got %s then %s", metas[0].Scope, metas[1].Scope)
	}
}

// openTestStore is a store in a temp folder, opened and cleaned up by the test.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir() + "/secrets.json")
	if err != nil {
		t.Fatal(err)
	}
	return store
}
