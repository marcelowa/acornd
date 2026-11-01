// What the console answers on, completed rather than guessed: a person types a label or a whole name, and the
// zone they chose settles the rest (ADR 0036 §6).
package remote

import (
	"errors"
	"fmt"
	"strings"
)

// The two lengths DNS holds, so a name this function answers is one a record can point at.
const (
	maxNameLength  = 253
	maxLabelLength = 63
)

// Hostname completes what the setup screen was given into the one name the door answers on.
//
// A value with no dot in it is a label, and the zone it was chosen with completes it: `console` beside
// `acornd.app` is `console.acornd.app`. A name that already ends with the zone is taken as it is, because that is
// the same choice spelled out. Anything else is refused with a sentence naming both, because a record in somebody
// else's zone is a door this install does not own — and a screen that guessed would make the record anyway.
//
// The value is trimmed, lowercased, and stripped of a pasted scheme and a trailing slash first: those are shapes of
// the same name, not mistakes to refuse. A path, a port, or a character no DNS label holds is refused, because
// completing one of those would write something no resolver can answer.
func Hostname(input, zone string) (string, error) {
	name := cleanName(input)
	if name == "" {
		return "", errors.New("remote access needs a hostname: a label such as console, or the name the door answers on")
	}
	if err := nameOnly(input, name); err != nil {
		return "", err
	}
	zone, err := zoneOf(zone)
	if err != nil {
		return "", err
	}
	switch {
	case !strings.Contains(name, "."):
		return completed(name, zone, input)
	case name == zone:
		// The apex is not under itself, and a CNAME at the zone root is not what this makes.
		return "", fmt.Errorf("%q is the zone itself: the door answers on a name inside it, such as console.%s", input, zone)
	case strings.HasSuffix(name, "."+zone):
		return name, nil
	default:
		return "", fmt.Errorf("%q is not in %s: type a label such as console, or a name that ends with .%s", input, zone, zone)
	}
}

// cleanName is the value as a name: trimmed, lowercase, without a pasted scheme and without a trailing slash. A
// person copies a URL from a browser bar and types a capital; both mean the same name, so neither is a refusal.
func cleanName(value string) string {
	name := strings.ToLower(strings.TrimSpace(value))
	if _, after, found := strings.Cut(name, "://"); found {
		name = after
	}
	return strings.TrimRight(name, "/")
}

// zoneOf is the zone a label is completed with, or the refusal. A zone no resolver could hold would make every
// name this function answers one that cannot be pointed at.
func zoneOf(zone string) (string, error) {
	clean := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	switch {
	case clean == "":
		return "", errors.New("remote access needs the zone the hostname belongs to, so a label can be completed with it")
	case !nameHolds(clean):
		return "", fmt.Errorf("%q is not a zone name: letters, digits and hyphens, with a dot between labels", zone)
	}
	return clean, nil
}

// nameOnly refuses the three ways a value is more than a name, each in the words of the part the person can fix.
func nameOnly(input, name string) error {
	switch {
	case strings.Contains(name, "/"):
		return fmt.Errorf("%q has a path in it: a hostname is one name, with nothing after a /", input)
	case strings.Contains(name, ":"):
		return fmt.Errorf("%q carries a port, and a hostname does not: the tunnel and the door are reached on 443", input)
	case !nameHolds(name):
		return fmt.Errorf("%q is not a name a resolver can hold: letters, digits and hyphens, with a dot between labels", input)
	}
	return nil
}

// completed puts a typed label under the zone it was chosen with, and refuses the one thing that can go wrong when
// it does: a label and a zone can add up to a name longer than DNS holds.
func completed(label, zone, input string) (string, error) {
	name := label + "." + zone
	if !nameHolds(name) {
		return "", fmt.Errorf("%q is longer than a hostname may be: the whole name holds at most %d characters", input, maxNameLength)
	}
	return name, nil
}

// nameHolds reports whether a value is what DNS can carry: labels of letters, digits and hyphens, none of them
// empty, none of them starting or ending with a hyphen, and no longer than a name may be.
func nameHolds(name string) bool {
	if len(name) > maxNameLength {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if !labelHolds(label) {
			return false
		}
	}
	return true
}

// labelHolds is one label of the name. Uppercase is already gone: this reads what is left.
func labelHolds(label string) bool {
	if label == "" || len(label) > maxLabelLength {
		return false
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, character := range label {
		if !labelCharacter(character) {
			return false
		}
	}
	return true
}

// labelCharacter is one character a label may carry: an ASCII letter, a digit, or a hyphen.
func labelCharacter(character rune) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= '0' && character <= '9':
		return true
	}
	return character == '-'
}
