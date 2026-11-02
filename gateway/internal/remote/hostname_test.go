package remote

import (
	"strings"
	"testing"
)

// A label, a whole name, and the shapes of the same name a person pastes: the zone settles what the door answers on,
// and everything else is refused rather than guessed (ADR 0036 §6).
func TestAHostnameIsCompletedAndNotGuessed(t *testing.T) {
	longZone := strings.Join([]string{strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60), strings.Repeat("d", 60)}, ".")
	cases := []struct {
		name string
		// input is what the person typed, and zone is what the screen had chosen.
		input, zone string
		// want is the name the door answers on, and refusal is every part the sentence must name when there is
		// nothing to answer with.
		want    string
		refusal []string
	}{
		{name: "a label", input: "console", zone: "acornd.app", want: "console.acornd.app"},
		{name: "a whole name", input: "console.acornd.app", zone: "acornd.app", want: "console.acornd.app"},
		{name: "a name in another zone", input: "console.example.com", zone: "acornd.app", refusal: []string{"console.example.com", "acornd.app"}},
		{name: "a pasted scheme", input: "https://console", zone: "acornd.app", want: "console.acornd.app"},
		{name: "a pasted url", input: "https://console.acornd.app/", zone: "acornd.app", want: "console.acornd.app"},
		{name: "a trailing slash", input: "console/", zone: "acornd.app", want: "console.acornd.app"},
		{name: "a port", input: "console:8443", zone: "acornd.app", refusal: []string{"console:8443", "port"}},
		{name: "uppercase", input: "Console.Acornd.App", zone: "acornd.app", want: "console.acornd.app"},
		{name: "an underscore", input: "console_acornd", zone: "acornd.app", refusal: []string{"console_acornd", "resolver"}},
		{name: "a space", input: "console acornd.app", zone: "acornd.app", refusal: []string{"resolver"}},
		{name: "a label starting with a hyphen", input: "-console", zone: "acornd.app", refusal: []string{"-console", "resolver"}},
		{name: "a label longer than DNS holds", input: strings.Repeat("a", 64), zone: "acornd.app", refusal: []string{"resolver"}},
		{name: "a path", input: "console/acornd.app", zone: "acornd.app", refusal: []string{"console/acornd.app", "path"}},
		{name: "the zone itself", input: "acornd.app", zone: "acornd.app", refusal: []string{"acornd.app", "the zone itself"}},
		{name: "a name longer than DNS holds", input: strings.Repeat("a", 60), zone: longZone, refusal: []string{"longer than a hostname"}},
		{name: "empty", input: "   ", zone: "acornd.app", refusal: []string{"needs a hostname"}},
		{name: "nothing at all", input: "", zone: "acornd.app", refusal: []string{"needs a hostname"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Hostname(test.input, test.zone)
			if len(test.refusal) == 0 {
				if err != nil {
					t.Fatalf("Hostname(%q, %q) = %v, want %q", test.input, test.zone, err, test.want)
				}
				if got != test.want {
					t.Fatalf("Hostname(%q, %q) = %q, want %q", test.input, test.zone, got, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Hostname(%q, %q) = %q, want a refusal naming %v", test.input, test.zone, got, test.refusal)
			}
			for _, part := range test.refusal {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("the refusal does not name %q: %v", part, err)
				}
			}
			// A refusal names what was typed, so a form with eleven fields says which one is wrong.
			if typed := strings.TrimSpace(test.input); typed != "" && !strings.Contains(err.Error(), typed) {
				t.Errorf("the refusal does not name %q: %v", typed, err)
			}
		})
	}
}

// The zone is half of the answer: a label with nothing to complete it with, or a zone no resolver holds, is refused
// rather than turned into a name that cannot be pointed at.
func TestAHostnameNeedsAZoneItCanBeCompletedWith(t *testing.T) {
	cases := []struct {
		name, input, zone, refusal string
	}{
		{name: "no zone", input: "console", zone: "", refusal: "needs the zone"},
		{name: "a zone with an underscore", input: "console", zone: "acornd_app", refusal: "not a zone name"},
		{name: "a whole name with no zone", input: "console.acornd.app", zone: "", refusal: "needs the zone"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Hostname(test.input, test.zone)
			if err == nil {
				t.Fatalf("Hostname(%q, %q) = %q, want a refusal", test.input, test.zone, got)
			}
			if !strings.Contains(err.Error(), test.refusal) {
				t.Errorf("the refusal does not say %q: %v", test.refusal, err)
			}
		})
	}
}
