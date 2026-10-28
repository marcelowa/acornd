package httpapi

import (
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/people"
)

// The prefix is what the model reads, so it names the speaker when there is one and leaves the words exactly as
// they were otherwise.
func TestPrefixedNamesTheSpeakerOnlyWhenThereIsOne(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		speaker string
		want    string
	}{
		{"named", "can you check the build?", "Ana", "[Ana] can you check the build?"},
		{"alone", "can you check the build?", "", "can you check the build?"},
		{"files only", "", "Ana", ""},
		{"spaces only", "   ", "Ana", "   "},
		// A slash command is expanded by matching its first character; a prefix would turn it into a sentence about
		// compacting rather than a request to compact.
		{"command", "/compact", "Ana", "/compact"},
		{"command with spaces", "  /compact", "Ana", "  /compact"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := prefixed(test.text, test.speaker); got != test.want {
				t.Errorf("prefixed(%q, %q) = %q, want %q", test.text, test.speaker, got, test.want)
			}
		})
	}
}

// The name is the one the owner gave, and the address is the fallback: a person added by email only is still
// somebody the model can be told about.
func TestADisplayNameFallsBackToTheAddress(t *testing.T) {
	named := people.Person{Email: "ana@example.com", Name: "Ana Silva"}
	if got := named.DisplayName(); got != "Ana Silva" {
		t.Errorf("a named person is %q", got)
	}
	unnamed := people.Person{Email: "ana.silva@example.com"}
	if got := unnamed.DisplayName(); got != "ana.silva" {
		t.Errorf("an unnamed person is %q", got)
	}
}
