package supervise

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A command that writes nothing produces no chunk at all. Reading returns zero bytes at end of
// file, and emitting that as data would put an empty event on the wire for every quiet command —
// which a client cannot tell from a command that said nothing.
func TestAQuietCommandSendsNoEmptyChunk(t *testing.T) {
	_, socket := startServer(t)
	connection := dial(t, socket)
	connection.send(t, Request{Type: "run", ID: "quiet", Argv: []string{"true"}})

	chunks := 0
	deadline := time.Now().Add(10 * time.Second)
	_ = connection.conn.SetReadDeadline(deadline)
	for {
		var event Event
		if err := connection.decoder.Decode(&event); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if event.ID != "quiet" {
			continue
		}
		if event.Type == "chunk" {
			chunks++
			if event.Data == "" {
				t.Fatal("an empty chunk was sent")
			}
		}
		if event.Type == "exit" || event.Type == "error" {
			break
		}
	}
	if chunks != 0 {
		t.Errorf("a command with no output sent %d chunks", chunks)
	}
}

// The owner going away is the end of the server: a supervisor whose parent is gone keeps a sandbox
// alive with nothing watching it, and nothing will ever ask it for a command again.
func TestAnOwnerThatIsGoneEndsTheServer(t *testing.T) {
	server := New(filepath.Join(t.TempDir(), "s.sock"), "test", 0)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !server.done(cancelled) {
		t.Error("a cancelled owner is the end of the server")
	}
	if server.done(context.Background()) {
		t.Error("a live owner is not")
	}
	server.Shutdown()
	if !server.done(context.Background()) {
		t.Error("a shutdown is the end of the server")
	}
}

// The idle check runs a quarter as often as the window it measures, and both ends are clamped: a
// window of a second would otherwise be checked every quarter second, which is more often than a
// timer needs to be, and an hour-long window every fifteen minutes, which would miss the deadline
// by minutes. The clamps are the rule, so they are read directly.
func TestTheIdleCheckIsAQuarterOfTheWindowClamped(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		time.Second:            250 * time.Millisecond,
		100 * time.Millisecond: 100 * time.Millisecond, // a quarter would be 25ms
		4 * time.Millisecond:   100 * time.Millisecond,
		2 * time.Minute:        30 * time.Second, // a quarter would be 30s exactly
		4 * time.Hour:          30 * time.Second, // a quarter would be an hour
	}
	for idle, want := range cases {
		if got := idleInterval(idle); got != want {
			t.Errorf("idleInterval(%s) = %s, want %s", idle, got, want)
		}
	}
}
