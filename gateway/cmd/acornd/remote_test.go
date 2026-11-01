package main

import (
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/remote"
)

// `remote apply --hostname console --zone acornd.app` completes the label before anything is created, and says the
// name it made: the door answers on console.acornd.app, and a person should read that rather than discover it in
// remote.json afterwards (ADR 0036 §6).
func TestApplyingALabelSaysTheNameItBecame(t *testing.T) {
	cases := []struct {
		name, input, zone, hostname, want string
	}{
		{name: "a label", input: "console", zone: "acornd.app", hostname: "console.acornd.app", want: "console.acornd.app"},
		{name: "a whole name", input: "console.acornd.app", zone: "acornd.app", hostname: "console.acornd.app"},
		{name: "a capital", input: "Console", zone: "acornd.app", hostname: "console.acornd.app", want: "console.acornd.app"},
		{name: "a pasted url", input: "https://console.acornd.app/", zone: "acornd.app", hostname: "console.acornd.app", want: "console.acornd.app"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// The whole point of the pair: what the command applies is what the command says.
			hostname, err := remote.Hostname(test.input, test.zone)
			if err != nil {
				t.Fatalf("hostname: %v", err)
			}
			if hostname != test.hostname {
				t.Fatalf("the command would apply %q, want %q", hostname, test.hostname)
			}
			line := completionLine(test.input, test.zone, hostname)
			if test.want == "" {
				if line != "" {
					t.Errorf("a name that needed no completion was announced: %q", line)
				}
				return
			}
			if !strings.Contains(line, test.want) || !strings.Contains(line, test.zone) {
				t.Errorf("the line %q does not name both %q and the zone %q", line, test.want, test.zone)
			}
		})
	}
}
