// Package supervise is the process a sandbox runs as its first process: it holds the sandbox's
// namespaces open, runs the commands a workspace asks for, streams them back, and reaps what
// they leave behind.
//
// It is engine-neutral. Nothing here names a sandbox engine: it is "run this argv, in the
// namespaces I already have, and tell me what it printed". That is why an engine whose own
// `exec` is missing or broken (bubblewrap has none, youki's is broken rootless) can still run
// commands in one long-lived sandbox — and why a daemon a command started outlives that command.
//
// The protocol is JSON lines over a Unix socket, one request per line and one event per line:
//
//	{"type":"run","id":"1","argv":["/bin/echo","hi"],"env":{"PATH":"/bin"},"dir":"/work"}
//	{"type":"chunk","id":"1","stream":"stdout","data":"aGkK"}
//	{"type":"exit","id":"1","code":0}
//
// Data is base64 so that a command's bytes are its own. A second connection may attach while
// commands from the first are still running: a worker restarts, the sandbox does not.
package supervise

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Request is one line from a client.
type Request struct {
	// Type is "run", "kill", "shutdown", or "ping".
	Type string `json:"type"`
	// ID names a command, is chosen by the client, and is what events carry.
	ID string `json:"id,omitempty"`
	// Argv is the program and its arguments; Argv[0] is the program.
	Argv []string `json:"argv,omitempty"`
	// Env is the whole environment of the command. Nothing is inherited: a secret reaches a
	// command because it was sent here, and for no other reason.
	Env map[string]string `json:"env,omitempty"`
	// Dir is the working directory inside the sandbox.
	Dir string `json:"dir,omitempty"`
}

// Event is one line to a client.
type Event struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Stream string `json:"stream,omitempty"`
	// Data is base64-encoded bytes, so a command's output is never mangled by JSON.
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
	// Stopped says why we stopped it rather than it finishing: "killed" or "timeout".
	Stopped string `json:"stopped,omitempty"`
	Error   string `json:"error,omitempty"`
	// Version is what "ready" answers, so a client can tell a live supervisor from a stale one.
	Version string `json:"version,omitempty"`
}

// Server is the supervisor. It owns the sandbox's processes for as long as the sandbox lives.
type Server struct {
	socket string
	// version is reported to a client that asks, which is how the driver distinguishes a
	// supervisor that is up from a socket file left by one that is not.
	version string

	mu    sync.Mutex
	runs  map[string]*run
	conns map[*conn]struct{}
	// shuttingDown stops new runs and ends Serve.
	shuttingDown bool
	listener     net.Listener
}

// run is one command, from request to exit.
type run struct {
	id  string
	cmd *exec.Cmd
	// stopped is why we killed it, if we did.
	stopped string
	// clients is every connection waiting for this command's events.
	clients map[*conn]struct{}
}

// New builds a supervisor that will listen on socket.
func New(socket, version string) *Server {
	return &Server{
		socket:  socket,
		version: version,
		runs:    map[string]*run{},
		conns:   map[*conn]struct{}{},
	}
}

// Serve listens and answers until the context ends or a client asks it to shut down. It is the
// sandbox's first process: when it returns, everything it started goes with it.
func (s *Server) Serve(ctx context.Context) error {
	if err := os.RemoveAll(s.socket); err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(s.socket), 0o700); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("supervisor: cannot listen on %s: %w", s.socket, err)
	}
	if err := os.Chmod(s.socket, 0o600); err != nil {
		listener.Close()
		return err
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	// A signal means the sandbox is being stopped: take the commands with it rather than
	// leaving processes behind in a namespace nobody can reach.
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stopping)
	go func() {
		select {
		case <-ctx.Done():
		case <-stopping:
		}
		s.Shutdown()
	}()

	for {
		connection, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			done := s.shuttingDown
			s.mu.Unlock()
			if done || ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveConn(&conn{conn: connection, server: s, encoder: json.NewEncoder(connection)})
	}
}

// Shutdown ends the server: no new runs, and every command is killed.
func (s *Server) Shutdown() {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return
	}
	s.shuttingDown = true
	listener := s.listener
	runs := make([]*run, 0, len(s.runs))
	for _, command := range s.runs {
		runs = append(runs, command)
	}
	conns := make([]*conn, 0, len(s.conns))
	for connection := range s.conns {
		conns = append(conns, connection)
	}
	s.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	for _, command := range runs {
		s.kill(command, "killed")
	}
	for _, connection := range conns {
		_ = connection.conn.Close()
	}
	_ = os.Remove(s.socket)
}

// conn is one client connection.
type conn struct {
	conn    net.Conn
	server  *Server
	encoder *json.Encoder
	mu      sync.Mutex
}

// send writes one event. A client that has gone away is not an error: the command keeps running
// and its output is dropped, because the sandbox outlives the process watching it.
func (c *conn) send(event Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.encoder.Encode(event)
}

func (s *Server) serveConn(connection *conn) {
	s.mu.Lock()
	s.conns[connection] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, connection)
		// The connection no longer wants events, but its commands are not cancelled: a worker
		// restarting must not kill what the workspace is running.
		for _, command := range s.runs {
			delete(command.clients, connection)
		}
		s.mu.Unlock()
		_ = connection.conn.Close()
	}()

	decoder := json.NewDecoder(bufio.NewReader(connection.conn))
	for {
		var request Request
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			connection.send(Event{Type: "error", Error: err.Error()})
			return
		}
		switch request.Type {
		case "ping":
			connection.send(Event{Type: "ready", Version: s.version})
		case "run":
			s.start(connection, request)
		case "kill":
			s.mu.Lock()
			command := s.runs[request.ID]
			s.mu.Unlock()
			if command == nil {
				connection.send(Event{Type: "error", ID: request.ID, Error: "no such command"})
				continue
			}
			s.kill(command, "killed")
		case "shutdown":
			connection.send(Event{Type: "ready", Version: s.version})
			s.Shutdown()
			return
		default:
			connection.send(Event{Type: "error", ID: request.ID, Error: "unknown request " + request.Type})
		}
	}
}

// start begins one command and reports its output to the client that asked for it.
func (s *Server) start(connection *conn, request Request) {
	s.mu.Lock()
	shuttingDown := s.shuttingDown
	_, exists := s.runs[request.ID]
	s.mu.Unlock()
	if shuttingDown {
		connection.send(Event{Type: "error", ID: request.ID, Error: "the sandbox is stopping"})
		return
	}
	if request.ID == "" || exists {
		connection.send(Event{Type: "error", ID: request.ID, Error: "a run needs a unique id"})
		return
	}
	if len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
		connection.send(Event{Type: "error", ID: request.ID, Error: "empty argv: no program to run"})
		return
	}

	command := exec.Command(request.Argv[0], request.Argv[1:]...)
	command.Env = environment(request.Env)
	command.Dir = request.Dir
	// Its own process group, so a kill takes what the command started and not only the
	// command. The sandbox's own first process stays out of it.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	if err := command.Start(); err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}

	current := &run{id: request.ID, cmd: command, clients: map[*conn]struct{}{connection: {}}}
	s.mu.Lock()
	s.runs[request.ID] = current
	s.mu.Unlock()
	connection.send(Event{Type: "started", ID: request.ID, PID: command.Process.Pid})

	var streams sync.WaitGroup
	streams.Add(2)
	pump := func(reader io.Reader, stream string) {
		defer streams.Done()
		buffer := make([]byte, 16*1024)
		for {
			read, err := reader.Read(buffer)
			if read > 0 {
				s.emit(current, Event{
					Type:   "chunk",
					ID:     request.ID,
					Stream: stream,
					Data:   base64.StdEncoding.EncodeToString(buffer[:read]),
				})
			}
			if err != nil {
				return
			}
		}
	}
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")

	go func() {
		streams.Wait()
		err := command.Wait()
		s.mu.Lock()
		delete(s.runs, request.ID)
		stopped := current.stopped
		s.mu.Unlock()
		event := Event{Type: "exit", ID: request.ID, Stopped: stopped}
		if command.ProcessState != nil {
			event.Code = command.ProcessState.ExitCode()
		}
		if err != nil && command.ProcessState == nil {
			// It never ran: Start succeeded and Wait failed, which is a broken process.
			event.Type = "error"
			event.Error = err.Error()
		}
		s.emit(current, event)
	}()
}

// emit sends an event to every client waiting on this command.
func (s *Server) emit(command *run, event Event) {
	s.mu.Lock()
	clients := make([]*conn, 0, len(command.clients))
	for connection := range command.clients {
		clients = append(clients, connection)
	}
	if event.Type == "exit" || event.Type == "error" {
		// The command is over: nobody is waiting on it any more.
		command.clients = map[*conn]struct{}{}
	}
	s.mu.Unlock()
	for _, connection := range clients {
		connection.send(event)
	}
}

// kill ends a command and everything it started.
func (s *Server) kill(command *run, reason string) {
	s.mu.Lock()
	if command.stopped == "" {
		command.stopped = reason
	}
	s.mu.Unlock()
	if command.cmd.Process == nil {
		return
	}
	// The group first: a shell that started a server is not the process to kill, the group is.
	if err := syscall.Kill(-command.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = command.cmd.Process.Kill()
	}
}

// environment turns a request's map into a process environment, in a stable order so a
// command's /proc has no surprises, and with nothing inherited from the supervisor's own.
func environment(values map[string]string) []string {
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

// dirOf is the folder a socket lives in.
func dirOf(path string) string {
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return "."
}

// WaitForReady connects until the socket answers, or the deadline passes. The driver uses it so
// that a sandbox is only reported running when something on the other end actually answers.
func WaitForReady(ctx context.Context, socket string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		connection, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			encoder := json.NewEncoder(connection)
			decoder := json.NewDecoder(connection)
			_ = encoder.Encode(Request{Type: "ping"})
			_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
			var event Event
			decodeErr := decoder.Decode(&event)
			_ = connection.Close()
			if decodeErr == nil && event.Type == "ready" {
				return nil
			}
			if decodeErr != nil {
				last = decodeErr
			}
		} else {
			last = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	if last == nil {
		last = errors.New("no answer")
	}
	return fmt.Errorf("supervisor at %s did not answer: %w", socket, last)
}
