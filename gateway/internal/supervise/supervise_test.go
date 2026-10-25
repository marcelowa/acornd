package supervise

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// client is the other end: a test that talks the protocol the worker will talk.
type client struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
	mu      sync.Mutex
}

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	server := New(socket, "test", 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		server.Shutdown()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("the supervisor did not stop")
		}
	})
	if err := WaitForReady(ctx, socket, 3*time.Second); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return server, socket
}

func dial(t *testing.T, socket string) *client {
	t.Helper()
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { connection.Close() })
	return &client{conn: connection, encoder: json.NewEncoder(connection), decoder: json.NewDecoder(bufio.NewReader(connection))}
}

func (c *client) send(t *testing.T, request Request) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.encoder.Encode(request); err != nil {
		t.Fatalf("send %s: %v", request.Type, err)
	}
}

// collect reads events for one command until it exits, returning its output and exit event.
func (c *client) collect(t *testing.T, id string) (string, Event) {
	t.Helper()
	var output strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	_ = c.conn.SetReadDeadline(deadline)
	for {
		var event Event
		if err := c.decoder.Decode(&event); err != nil {
			t.Fatalf("decode while waiting for %s: %v", id, err)
		}
		if event.ID != "" && event.ID != id {
			continue
		}
		switch event.Type {
		case "chunk":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				t.Fatalf("chunk is not base64: %v", err)
			}
			output.Write(data)
		case "exit", "error":
			return output.String(), event
		}
	}
}

func TestARunStreamsAndReportsItsExitCode(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{
		Type: "run",
		ID:   "1",
		Argv: []string{"/bin/sh", "-c", "printf one; printf two >&2; exit 3"},
		Env:  map[string]string{"PATH": "/usr/bin:/bin"},
		Dir:  "/",
	})
	output, event := c.collect(t, "1")
	if event.Type != "exit" {
		t.Fatalf("event: %+v", event)
	}
	if event.Code != 3 {
		t.Errorf("exit code %d, want 3", event.Code)
	}
	if !strings.Contains(output, "one") || !strings.Contains(output, "two") {
		t.Errorf("output %q should carry both streams", output)
	}
}

// The environment is exactly what was sent: a supervisor that inherited its own environment
// would hand the sandbox everything the worker holds.
func TestARunSeesOnlyTheEnvironmentItWasSent(t *testing.T) {
	t.Setenv("ACORNFALL_SUPERVISOR_SHOULD_NOT_LEAK", "yes")
	_, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{
		Type: "run",
		ID:   "1",
		Argv: []string{"/bin/sh", "-c", `printf '%s' "$ONLY"`},
		Env:  map[string]string{"ONLY": "this"},
	})
	output, _ := c.collect(t, "1")
	if output != "this" {
		t.Fatalf("output %q, want only what was sent", output)
	}

	c.send(t, Request{
		Type: "run",
		ID:   "2",
		Argv: []string{"/bin/sh", "-c", `printf '%s' "${ACORNFALL_SUPERVISOR_SHOULD_NOT_LEAK:-absent}"`},
		Env:  map[string]string{},
	})
	output, _ = c.collect(t, "2")
	if output != "absent" {
		t.Fatalf("the supervisor's own environment leaked into a command: %q", output)
	}
}

// A kill takes what the command started, not only the command: a shell that started a server is
// not the process that has to die. What survived is asked of the process group this command
// started — the pid comes from the supervisor's own "started" event — and not of the machine:
// `pgrep -f "sleep 30"` also counts the shell running the assertion and the sleepers a killed
// mutation run left behind, which is a green test on a quiet machine and a red one after any
// interrupted run.
func TestKillEndsTheCommandAndWhatItStarted(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{
		Type: "run",
		ID:   "1",
		Argv: []string{"/bin/sh", "-c", "sleep 30 & sleep 30 & echo started; wait"},
		Env:  map[string]string{"PATH": "/usr/bin:/bin"},
	})
	go func() {
		time.Sleep(200 * time.Millisecond)
		c.send(t, Request{Type: "kill", ID: "1"})
	}()

	var output strings.Builder
	group := 0
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var event Event
	for {
		if err := c.decoder.Decode(&event); err != nil {
			t.Fatalf("decode while waiting for 1: %v", err)
		}
		if event.ID != "" && event.ID != "1" {
			continue
		}
		switch event.Type {
		case "started":
			if pgid, err := syscall.Getpgid(event.PID); err == nil {
				group = pgid
			}
		case "chunk":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				t.Fatalf("chunk is not base64: %v", err)
			}
			output.Write(data)
		}
		if event.Type == "exit" || event.Type == "error" {
			break
		}
	}
	if !strings.Contains(output.String(), "started") {
		t.Fatalf("output %q", output.String())
	}
	if event.Stopped != "killed" {
		t.Errorf("stopped: %q, want killed", event.Stopped)
	}
	if group == 0 {
		t.Fatal("the command never reported a process group")
	}
	gone := false
	for range 40 {
		if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		t.Errorf("process group %d survived the kill", group)
	}
}

// A client that goes away does not take the workspace's processes with it: a worker restart is
// not a sandbox stop.
func TestACommandSurvivesItsClientGoingAway(t *testing.T) {
	_, socket := startServer(t)
	first := dial(t, socket)
	first.send(t, Request{
		Type: "run",
		ID:   "1",
		Argv: []string{"/bin/sh", "-c", "echo started; sleep 2; echo finished"},
		Env:  map[string]string{"PATH": "/usr/bin:/bin"},
	})
	// Read the first chunk, then hang up.
	_ = first.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event Event
	_ = first.decoder.Decode(&event)
	first.conn.Close()
	time.Sleep(2500 * time.Millisecond)
	// Still running; a second client can attach and start its own command.
	second := dial(t, socket)
	second.send(t, Request{Type: "run", ID: "2", Argv: []string{"/bin/echo", "still here"}, Env: map[string]string{}, Dir: "/"})
	output, exit := second.collect(t, "2")
	if exit.Code != 0 || !strings.Contains(output, "still here") {
		t.Fatalf("a new client could not run: %+v %q", exit, output)
	}
}

// The sandbox stops itself when nothing is running in it: that is the timer under a gateway that
// died and under a workspace control deliberately keeps warm.
func TestAnIdleSandboxStopsItself(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	server := New(socket, "test", 300*time.Millisecond)
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	if err := WaitForReady(context.Background(), socket, 3*time.Second); err != nil {
		t.Fatalf("ready: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle sandbox did not stop itself")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the sandbox: %v", err)
	}
}

// A worker sits attached with nothing to run for as long as a conversation is idle, which can be
// longer than the sandbox's own window. That is not an abandoned sandbox: the worker is still
// there, and the timer that exists for a gateway that died must not stop it under one.
func TestAKeepaliveKeepsTheSandboxAwake(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	server := New(socket, "test", 300*time.Millisecond)
	ctx := t.Context()
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(ctx) }()
	if err := WaitForReady(context.Background(), socket, 3*time.Second); err != nil {
		t.Fatalf("ready: %v", err)
	}
	c := dial(t, socket)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		c.send(t, Request{Type: "keepalive"})
		var event Event
		_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
		if err := c.decoder.Decode(&event); err != nil {
			t.Fatalf("keepalive: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case <-stopped:
		t.Fatal("a keepalive did not keep the sandbox awake")
	default:
	}
}

// Asking whether the sandbox is up is not the same as being a client of it: control's status and
// adoption probes must not keep a sandbox nobody is using alive, or the timer that reclaims one
// after a dead gateway never fires.
func TestAPingDoesNotKeepTheSandboxAwake(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	server := New(socket, "test", 300*time.Millisecond)
	ctx := t.Context()
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(ctx) }()
	if err := WaitForReady(context.Background(), socket, 3*time.Second); err != nil {
		t.Fatalf("ready: %v", err)
	}
	c := dial(t, socket)
	// Ping like a status probe would, and expect the sandbox to stop anyway.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		_ = c.encoder.Encode(Request{Type: "ping"})
		select {
		case <-stopped:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("a sandbox kept awake by pings never stopped")
}

// A command is activity: a sandbox that is doing something is not idle, however long it takes.
func TestARunningCommandKeepsTheSandboxAwake(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	server := New(socket, "test", 300*time.Millisecond)
	ctx := t.Context()
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(ctx) }()
	if err := WaitForReady(context.Background(), socket, 3*time.Second); err != nil {
		t.Fatalf("ready: %v", err)
	}
	c := dial(t, socket)
	c.send(t, Request{Type: "run", ID: "1", Argv: []string{"/bin/sh", "-c", "sleep 1.2; echo done"}, Env: map[string]string{"PATH": "/usr/bin:/bin"}})
	output, exit := c.collect(t, "1")
	if exit.Code != 0 || !strings.Contains(output, "done") {
		t.Fatalf("the command was stopped as idle: %+v %q", exit, output)
	}
	select {
	case <-stopped:
		t.Fatal("the sandbox stopped while a command was running")
	default:
	}
}

// A command can end with a process in another session still holding its stdout. `timeout 20
// pasta …` does exactly that: timeout runs the command in a process group of its own, and pasta
// ignores SIGTERM. Killing the run's group then leaves that process alive with the pipe in its
// hands. The run has ended all the same, and the client waiting for its exit must hear so;
// before this it kept the shell a zombie and no exit ever came, so the tool call it belonged to
// hung forever — past its own timeout and past an abort.
func TestAKilledRunEndsEvenWhenAGrandchildHoldsThePipes(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{
		Type: "run",
		ID:   "1",
		Argv: []string{"/bin/sh", "-c", "setsid sh -c 'echo held; sleep 5' & wait"},
		Env:  map[string]string{"PATH": "/usr/bin:/bin"},
		Dir:  "/",
	})
	// Wait until the grandchild is really running and holding the pipes before killing the run:
	// it prints "held" after it has its own session and the inherited stdout.
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	held := false
	for !held {
		var event Event
		if err := c.decoder.Decode(&event); err != nil {
			t.Fatalf("the command did not start: %v", err)
		}
		if event.Type == "chunk" && event.Stream == "stdout" {
			data, _ := base64.StdEncoding.DecodeString(event.Data)
			held = strings.Contains(string(data), "held")
		}
	}

	c.send(t, Request{Type: "kill", ID: "1"})
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var event Event
		if err := c.decoder.Decode(&event); err != nil {
			t.Fatalf("the run never ended after it was killed: %v", err)
		}
		if event.Type == "exit" {
			if event.Stopped != "killed" {
				t.Fatalf("the exit should say it was killed: %+v", event)
			}
			return
		}
	}
}

func TestPingAnswersWithTheVersion(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{Type: "ping"})
	var event Event
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := c.decoder.Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "ready" || event.Version != "test" {
		t.Fatalf("event: %+v", event)
	}
}

func TestShutdownStopsTheServerAndItsCommands(t *testing.T) {
	server, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{Type: "run", ID: "1", Argv: []string{"/bin/sleep", "30"}, Env: map[string]string{}, Dir: "/"})
	time.Sleep(200 * time.Millisecond)
	c.send(t, Request{Type: "shutdown"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		running := len(server.runs)
		server.mu.Unlock()
		if running == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("shutdown left a command running")
}

func TestAnUnknownRequestIsRefused(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	c.send(t, Request{Type: "dance", ID: "1"})
	var event Event
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := c.decoder.Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "error" || !strings.Contains(event.Error, "unknown request") {
		t.Fatalf("event: %+v", event)
	}
}
