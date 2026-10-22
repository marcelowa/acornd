package workerpool

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// FuzzTheReaderSurvivesWhateverTheWorkerSaid feeds the frame reader bytes it was never meant to
// see: a truncated line, invalid UTF-8, an id nobody is waiting for, an empty id, a megabyte of
// junk. Whatever arrives, three things must hold — it does not panic, it returns, and a waiter
// is settled rather than left hanging forever. A worker is a separate process and its stdout is
// the only thing this layer trusts it for, so this is the one boundary that has no type in front
// of it.
func FuzzTheReaderSurvivesWhateverTheWorkerSaid(f *testing.F) {
	for _, seed := range []string{
		`{"id":"1","type":"response","ok":true}`,
		`{"id":"1","type":"view","conversationId":"1","seq":1,"value":{}}`,
		`{"id":"","type":"response","ok":true}`,
		`{"type":"view"}`,
		`null`,
		`[]`,
		`"a string is not a frame"`,
		"{",
		"\x00\xff\xfe",
		strings.Repeat("x", 1<<16),
		"{\"id\":\"1\"}\n{\"id\":\"2\"}",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		w := New(Config{})
		var handled atomic.Int64
		w.OnFrame(func(protocol.Frame) { handled.Add(1) })
		waiter := make(chan protocol.Frame, 1)
		w.pending["1"] = waiter

		// A trailing newline is the frame separator, and a fuzz input without one is a line the
		// real reader would still see when the process exits.
		w.readStdout(bytes.NewReader(append(append([]byte{}, line...), '\n')))

		select {
		case <-waiter:
		case <-time.After(5 * time.Second):
			t.Fatalf("the waiter for id 1 was never settled after %q", line)
		}
	})
}
