package main

import (
	"slices"
	"testing"
)

// The workspace command documents flags after its positionals, and the standard flag package
// stops at the first positional. This is the split that keeps the documented form working.
func TestSplitFlagsAcceptsFlagsAnywhere(t *testing.T) {
	value := map[string]bool{"sandbox": true, "image": true, "network": true, "data-dir": true}
	for _, test := range []struct {
		name  string
		argv  []string
		flags []string
		args  []string
	}{
		{
			name:  "flags last, as documented",
			argv:  []string{"add", "demo", "/work/demo", "--sandbox", "bwrap", "--network", "off"},
			flags: []string{"--sandbox", "bwrap", "--network", "off"},
			args:  []string{"add", "demo", "/work/demo"},
		},
		{
			name:  "flags first, as typed",
			argv:  []string{"--data-dir", "/home", "add", "demo"},
			flags: []string{"--data-dir", "/home"},
			args:  []string{"add", "demo"},
		},
		{
			name:  "an inline value is one argument",
			argv:  []string{"add", "demo", "--sandbox=bwrap"},
			flags: []string{"--sandbox=bwrap"},
			args:  []string{"add", "demo"},
		},
		{
			name:  "an unknown flag is left for the parser to refuse",
			argv:  []string{"add", "--verbose"},
			flags: []string{"--verbose"},
			args:  []string{"add"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			flags, args := splitFlags(test.argv, value)
			if !slices.Equal(flags, test.flags) {
				t.Errorf("flags: %v, want %v", flags, test.flags)
			}
			if !slices.Equal(args, test.args) {
				t.Errorf("args: %v, want %v", args, test.args)
			}
		})
	}
}
