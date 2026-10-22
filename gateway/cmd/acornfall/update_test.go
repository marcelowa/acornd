package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The offer is a headline, not a changelog: five lines at most, each one screen wide, headings
// and blank lines skipped so the first thing read is the first thing that changed.
func TestTheUpdateOfferKeepsHeadlines(t *testing.T) {
	notes := "## What changed\n\n" +
		"- first thing\n" +
		"- second thing that is " + strings.Repeat("long ", 40) + "\n" +
		"- third thing\n" +
		"- fourth thing\n" +
		"- fifth thing\n" +
		"- sixth thing\n"
	lines := firstLines(notes, 3)
	if len(lines) != 3 {
		t.Fatalf("wanted three lines, got %d: %q", len(lines), lines)
	}
	if lines[0] != "first thing" || lines[2] != "third thing" {
		t.Fatalf("the lines are not the notes in order: %q", lines)
	}
	if utf8.RuneCountInString(lines[1]) > 110 || !strings.HasSuffix(lines[1], "…") {
		t.Fatalf("a long line should be cut, got %d characters", len(lines[1]))
	}
}

// The prompt after `acornfall update` defaults to yes: the command was already the request.
func TestTheUpdatePromptDefaultsToYes(t *testing.T) {
	for input, want := range map[string]bool{
		"\n": true, "y\n": true, "Y\n": true, "yes\n": true,
		"n\n": false, "no\n": false, "later\n": false,
	} {
		original := stdinForTest
		stdinForTest = strings.NewReader(input)
		got, err := confirmYes("? ")
		stdinForTest = original
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if got != want {
			t.Errorf("%q: got %v, want %v", input, got, want)
		}
	}
}
