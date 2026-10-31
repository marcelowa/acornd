package httpapi

import (
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// stateWith is a state carrying two providers and four models, which is enough to tell a filtered list from a
// full one.
func stateWith() control.State {
	return control.State{Providers: []protocol.ProviderInfo{
		{ID: "anthropic", Models: []protocol.ModelInfo{{ID: "fast"}, {ID: "big"}}},
		{ID: "openai", Models: []protocol.ModelInfo{{ID: "cheap"}, {ID: "expensive"}}},
	}}
}

// offered lists what survived, as "provider/model", which is how a person reads a picker.
func offered(state control.State) []string {
	var out []string
	for _, provider := range state.Providers {
		for _, model := range provider.Models {
			out = append(out, provider.ID+"/"+model.ID)
		}
	}
	return out
}

// A person is offered the models they may run and nothing else: the picker used to offer every model the install
// had, and a press on one of the others ended in a refusal one step later (ADR 0032 §6).
func TestAPersonIsOfferedOnlyTheModelsTheyMayRun(t *testing.T) {
	opened := book(t)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := opened.SetModels("ana@example.com", []string{"anthropic/fast", "openai/cheap"}); err != nil {
		t.Fatalf("models: %v", err)
	}
	server := &Server{gate: Gate{People: opened}}
	got := offered(server.offerable(stateWith(), control.Caller{Email: "ana@example.com"}))
	want := []string{"anthropic/fast", "openai/cheap"}
	if len(got) != len(want) {
		t.Fatalf("offered %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("offered %v, want %v", got, want)
		}
	}
}

// The admin keeps the whole list: the install is theirs, and a picker narrowed for them would be a bug wearing a
// permission.
func TestTheAdminIsOfferedEverything(t *testing.T) {
	opened := book(t)
	server := &Server{gate: Gate{People: opened}}
	if got := offered(server.offerable(stateWith(), control.Caller{Admin: true, Email: "admin@example.com"})); len(got) != 4 {
		t.Errorf("the admin was offered %v", got)
	}
}

// A person with no list keeps everything, which is where they start: an empty list is "any model this install has"
// rather than "none".
func TestAPersonWithNoListIsOfferedEverything(t *testing.T) {
	opened := book(t)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	server := &Server{gate: Gate{People: opened}}
	if got := offered(server.offerable(stateWith(), control.Caller{Email: "ana@example.com"})); len(got) != 4 {
		t.Errorf("a person with no list was offered %v", got)
	}
}

// A record that cannot be read leaves the list alone rather than emptying it: the refusal at assignment is the
// control, and it still says what it said.
func TestAnUnreadableBookLeavesTheListAlone(t *testing.T) {
	server := &Server{gate: Gate{People: book(t)}}
	if got := offered(server.offerable(stateWith(), control.Caller{Email: "stranger@example.com"})); len(got) != 4 {
		t.Errorf("an unknown address was offered %v", got)
	}
}

// No book at all is the single-admin install of ADR 0020, and there is nobody to narrow for.
func TestAnInstallWithNoBookOffersEverything(t *testing.T) {
	server := &Server{}
	if got := offered(server.offerable(stateWith(), control.Caller{Email: "anybody@example.com"})); len(got) != 4 {
		t.Errorf("an install with no book offered %v", got)
	}
}
