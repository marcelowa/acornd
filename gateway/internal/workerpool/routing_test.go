package workerpool

import (
	"strings"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// Where a frame goes. The worker speaks JSONL on stdout and the reader is the only thing that
// decides: a response belongs to whoever is waiting for that id, everything else is news for the
// handler. Getting that wrong is a conversation that never answers, or a state frame that settles
// a wait it has nothing to do with.

func TestAResponseGoesToItsWaiterAndNowhereElse(t *testing.T) {
	w := New(Config{})
	var handled []protocol.Frame
	w.OnFrame(func(frame protocol.Frame) { handled = append(handled, frame) })
	waiter := make(chan protocol.Frame, 1)
	w.pending["7"] = waiter

	stream := strings.Join([]string{
		`{"id":"7","type":"response","ok":true,"data":{"a":1}}`,
		`{"type":"view","conversationId":"1","seq":1,"value":{}}`,
		`{"id":"8","type":"response","ok":true}`, // nobody is waiting for this one
	}, "\n") + "\n"
	w.readStdout(strings.NewReader(stream))

	select {
	case frame := <-waiter:
		if frame.ID != "7" {
			t.Errorf("the waiter got id %q", frame.ID)
		}
	default:
		t.Fatal("the waiter was never given the response")
	}
	// The view and the unclaimed response both belong to the handler: a response nobody waits for
	// is still news, and dropping it would hide a late answer.
	if len(handled) != 2 {
		t.Fatalf("the handler saw %d frames, want 2: %+v", len(handled), handled)
	}
	if handled[0].Type != "view" || handled[1].ID != "8" {
		t.Errorf("the handler saw %+v", handled)
	}
}

// A line that is not a frame is dropped, and the next line is still read: a harness that writes
// half a line before dying must not cost the frames that came after it.
func TestAGarbledLineDoesNotStopTheReader(t *testing.T) {
	w := New(Config{})
	var handled []protocol.Frame
	w.OnFrame(func(frame protocol.Frame) { handled = append(handled, frame) })
	w.readStdout(strings.NewReader("not json\n{\"type\":\"view\",\"seq\":1}\n"))

	if len(handled) != 1 || handled[0].Type != "view" {
		t.Errorf("the good line should still arrive, got %+v", handled)
	}
}

// A worker with nobody listening is a state the reader must survive: a frame that arrives before
// the handler is registered is dropped, not a panic.
func TestAFrameWithNoHandlerIsDropped(t *testing.T) {
	w := New(Config{})
	w.readStdout(strings.NewReader(`{"type":"view","seq":1}` + "\n"))
}

// The stderr tail is bounded: it exists to explain a failure, not to be a log file, and a harness
// that prints a thousand lines must not grow the gateway's memory with them.
func TestTheStderrTailKeepsTheLastLines(t *testing.T) {
	w := New(Config{})
	for index := 0; index < stderrKept*3; index++ {
		w.rememberStderr("line")
	}
	w.rememberStderr("the last thing it said")
	if len(w.stderr) != stderrKept {
		t.Fatalf("kept %d lines, want %d", len(w.stderr), stderrKept)
	}
	if got := w.stderr[len(w.stderr)-1]; got != "the last thing it said" {
		t.Errorf("the most recent line is %q", got)
	}
}

// And the reader settles every waiter when the stream ends: a closed stdout is a worker that is
// gone, and a waiter left hanging is a conversation that never answers.
func TestTheEndOfTheStreamSettlesEveryone(t *testing.T) {
	w := New(Config{})
	waiter := make(chan protocol.Frame, 1)
	w.pending["9"] = waiter
	done := make(chan struct{})
	go func() {
		w.readStdout(strings.NewReader(""))
		close(done)
	}()
	select {
	case frame := <-waiter:
		if frame.OK {
			t.Error("a settled waiter gets a failure, not a success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter was never settled")
	}
	<-done
}
