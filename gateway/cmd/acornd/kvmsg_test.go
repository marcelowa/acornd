package main

import "testing"

// An argument handed to `sg -c` goes through a shell: quoting keeps a path with a space, or a quote, one word.
func TestSGQuoteKeepsAnArgumentWhole(t *testing.T) {
	for _, test := range []struct{ in, want string }{
		{"/home/marcelo/.acornd/bin/acornd", `'/home/marcelo/.acornd/bin/acornd'`},
		{"/tmp/a b", `'/tmp/a b'`},
		{"it's", `'it'"'"'s'`},
	} {
		if got := sgQuote(test.in); got != test.want {
			t.Errorf("sgQuote(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}
