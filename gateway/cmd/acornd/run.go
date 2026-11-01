package main

// A run, as a thing a person starts and stops: the record of the process that serves this install,
// and the four verbs that read it.
//
// ADR 0036 §2. There is no service manager here on purpose. What a service manager would buy is a
// start at boot and a restart after a crash, and the owner of this install declined both: a crash
// should be visible rather than silently repaired, and the console is started by hand. What is left
// is knowing which process serves this folder, stopping it politely, and being able to say where it
// went — which is a pid, an address, and a log, not a unit file.
import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// runFile is the record, inside the install folder so two installs never see each other's.
	runFile = "run.json"
	// runLog is where a detached run writes what the terminal would have shown.
	runLog = "acornd.log"
)

// errNoRecord is "nothing was started from this folder", which is an answer and not a failure.
var errNoRecord = errors.New("nothing is running from this folder")

// runRecord is one run: enough to find it, stop it, and start it again the same way.
type runRecord struct {
	PID      int      `json:"pid"`
	Args     []string `json:"args"`
	Address  string   `json:"address"`
	Detached bool     `json:"detached,omitempty"`
	Started  string   `json:"started"`
}

// startedAt is when this run began, or the zero time for a record that cannot say.
func (r runRecord) startedAt() time.Time {
	at, err := time.Parse(time.RFC3339, r.Started)
	if err != nil {
		return time.Time{}
	}
	return at
}

func runRecordPath(dir string) string { return filepath.Join(dir, runFile) }
func runLogPath(dir string) string    { return filepath.Join(dir, runLog) }

// writeRunRecord writes the record whole and 0600, the same way the other install files are
// written: a half-written record is a run nobody can stop.
func writeRunRecord(dir string, record runRecord) error {
	if record.Started == "" {
		record.Started = time.Now().UTC().Format(time.RFC3339)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temp, err := os.CreateTemp(dir, ".run-*")
	if err != nil {
		return err
	}
	path := temp.Name()
	defer func() { _ = os.Remove(path) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(path, runRecordPath(dir))
}

// readRunRecord is the record, or errNoRecord when this folder was never started.
func readRunRecord(dir string) (runRecord, error) {
	payload, err := os.ReadFile(runRecordPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runRecord{}, errNoRecord
		}
		return runRecord{}, err
	}
	var record runRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return runRecord{}, fmt.Errorf("%s is not a run record: %w", runRecordPath(dir), err)
	}
	return record, nil
}

// runningRecord is the live run from this folder: a record, a pid that exists, and a process that
// is still acornd. Anything less is a record from a run that is over.
func runningRecord(dir string) (runRecord, bool) {
	record, err := readRunRecord(dir)
	if err != nil || !processAlive(record.PID) || !processLooksLikeAcornd(record.PID) {
		return runRecord{}, false
	}
	return record, true
}

// removeRunRecord forgets the run. Called by the process on its way out and by stop, because
// either can be the one that finishes last and both mean the same thing.
func removeRunRecord(dir string) {
	_ = os.Remove(runRecordPath(dir))
}

// runFlags is the flag set every lifecycle verb shares: where the install folder is.
func runFlags(name string, argv []string) (string, error) {
	flags := flag.NewFlagSet("acornd "+name, flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder")
	if err := flags.Parse(argv); err != nil {
		return "", err
	}
	return *dataDir, nil
}

// startDetached is `acornd start -d`: answer the questions the parent can answer itself (is it
// already up, is the port free), then spawn the run and report what happened.
func startDetached(argv []string, dataDir, host string, port int) error {
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	// Asked here rather than only inside the child: the parent answers, so a second start is one
	// sentence instead of a spawned process that dies into a log.
	if record, ok := runningRecord(dir); ok {
		return fmt.Errorf("acornd is already running from %s as pid %d (%s): `acornd restart` starts it again", dir, record.PID, record.Address)
	}
	// A squatter on the port is worth one sentence too, and the bind inside is still the judge.
	if listener, err := net.Listen("tcp", address); err != nil {
		return fmt.Errorf("cannot listen on %s: %w", address, err)
	} else {
		_ = listener.Close()
	}
	run, err := spawnDetached(append([]string{"start"}, withoutDetach(argv)...), dir)
	if err != nil {
		return err
	}
	reportStarted(run, dir, address)
	return nil
}

// recordRun writes this process down as the run of this folder, refusing when another live run
// already is: two of them would fight for the port and, worse, for the record a person means when
// they type stop.
func recordRun(dir, address string) error {
	if existing, ok := runningRecord(dir); ok && existing.PID != os.Getpid() {
		return fmt.Errorf("acornd is already running from %s as pid %d (%s): `acornd stop` stops it", dir, existing.PID, existing.Address)
	}
	return writeRunRecord(dir, runRecord{
		PID:      os.Getpid(),
		Args:     os.Args[1:],
		Address:  address,
		Detached: os.Getenv("ACORND_DETACHED") != "",
	})
}

// stopCommand is `acornd stop`: SIGTERM, then wait. It refuses a pid that is no longer acornd,
// because after a reboot that number can belong to anything.
func stopCommand(argv []string) {
	dataDir, err := runFlags("stop", argv)
	if err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		fail(2, err)
	}
	if err := stopRun(dir); err != nil {
		fail(1, err)
	}
}

func stopRun(dir string) error {
	record, err := readRunRecord(dir)
	if errors.Is(err, errNoRecord) {
		fmt.Println("acornd: not running")
		return nil
	}
	if err != nil {
		return err
	}
	pid := record.PID
	if !processAlive(pid) {
		removeRunRecord(dir)
		fmt.Printf("acornd: not running (the record for pid %d was stale)\n", pid)
		return nil
	}
	if !processLooksLikeAcornd(pid) {
		removeRunRecord(dir)
		return fmt.Errorf("pid %d is not acornd: left it alone and removed the stale record", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	// Long enough for the workers and sandboxes to go down the way the gateway asks them to;
	// short enough that a person watching is not left wondering whether the command worked.
	if !waitGone(pid, 30*time.Second) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		if !waitGone(pid, 5*time.Second) {
			return fmt.Errorf("pid %d did not exit", pid)
		}
	}
	removeRunRecord(dir)
	fmt.Printf("acornd: stopped pid %d\n", pid)
	return nil
}

// restartCommand is `acornd restart`: the same run again, with the arguments it was started with.
// A foreground run stays in the terminal by replacing this process, which is how the update path
// already restarts; a detached run is spawned again and this command returns.
func restartCommand(argv []string) {
	dataDir, err := runFlags("restart", argv)
	if err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		fail(2, err)
	}
	record, err := readRunRecord(dir)
	if errors.Is(err, errNoRecord) {
		fail(1, errors.New("nothing is running from this folder: `acornd start` starts it"))
	}
	if err != nil {
		fail(1, err)
	}
	if err := stopRun(dir); err != nil {
		fail(1, err)
	}
	startRecorded(record, dir)
}

// startRecorded starts a run the way the record says it ran: detached again, or by becoming it in
// the terminal this was typed in.
func startRecorded(record runRecord, dir string) {
	args := record.Args
	if len(args) == 0 || args[0] != "start" {
		args = []string{"start"}
	}
	if record.Detached {
		run, err := spawnDetached(args, dir)
		if err != nil {
			fail(1, err)
		}
		// The address is the one the record carries: restart starts it the same way, so deriving
		// it again from a flag set would be a second opinion nobody asked for.
		reportStarted(run, dir, record.Address)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		fail(1, err)
	}
	if err := syscall.Exec(executable, append([]string{executable}, args...), os.Environ()); err != nil {
		fail(1, err)
	}
}

// statusCommand is `acornd status`: is it up, where, since when — and when it is down, the last
// thing the log said, because that is the question the answer raises.
func statusCommand(argv []string) {
	dataDir, err := runFlags("status", argv)
	if err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		fail(2, err)
	}
	record, err := readRunRecord(dir)
	if errors.Is(err, errNoRecord) {
		fmt.Printf("acornd: down (nothing running from %s)\n", dir)
		reportLastLogLine(dir)
		return
	}
	if err != nil {
		fail(1, err)
	}
	if !processAlive(record.PID) {
		fmt.Printf("acornd: down (pid %d is gone, started %s)\n", record.PID, describeSince(record))
		reportLastLogLine(dir)
		return
	}
	if !processLooksLikeAcornd(record.PID) {
		fmt.Printf("acornd: down (pid %d is something else now)\n", record.PID)
		reportLastLogLine(dir)
		return
	}
	fmt.Printf("acornd: up, pid %d, %s, since %s\n", record.PID, record.Address, describeSince(record))
	if url := healthURL(record.Address); url != "" && !waitHealthy(url, 2*time.Second) {
		fmt.Println("acornd: the process is up and not answering yet (still booting, or wedged)")
	}
	fmt.Printf("acornd: log %s\n", runLogPath(dir))
}

// describeSince is "2026-11-01T14:02:11Z (2h14m)", because both the stamp and the age are useful.
func describeSince(record runRecord) string {
	at := record.startedAt()
	if at.IsZero() {
		return "at an unrecorded time"
	}
	return fmt.Sprintf("%s (%s ago)", at.Local().Format("2006-01-02 15:04:05"), time.Since(at).Round(time.Second))
}

// reportLastLogLine is the reason a run is down, in the words it wrote on the way out.
func reportLastLogLine(dir string) {
	line, ok := lastLogLine(runLogPath(dir))
	if !ok {
		return
	}
	fmt.Printf("acornd: log %s\nacornd: last line: %s\n", runLogPath(dir), line)
}

// lastLogLine reads the end of the file rather than all of it: a log is the one file here that
// grows without bound, and the answer wanted is the last line.
func lastLogLine(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return "", false
	}
	const window = 8 << 10
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line, true
		}
	}
	return "", false
}

// spawned is a detached child the command still holds: its pid, and the one answer that matters
// in the next five seconds — did it exit, and why.
type spawned struct {
	pid  int
	done chan error
}

// spawnDetached starts a run with no terminal: its own session, its own log, and a closed stdin.
//
// Setsid rather than a double fork: the process has no controlling terminal afterwards either way,
// and one fork is one thing to read. The log is opened 0600 and appended, because a restart should
// not erase what the previous run said on its way out.
func spawnDetached(args []string, dir string) (spawned, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return spawned{}, err
	}
	log, err := os.OpenFile(runLogPath(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return spawned{}, err
	}
	defer func() { _ = log.Close() }()
	executable, err := os.Executable()
	if err != nil {
		return spawned{}, err
	}
	command := exec.Command(executable, args...)
	command.Env = append(os.Environ(), "ACORND_DETACHED=1")
	command.Stdin = nil
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return spawned{}, err
	}
	run := spawned{pid: command.Process.Pid, done: make(chan error, 1)}
	// The child is ours until this process exits; reaping it is the goroutine's job so a fast
	// failure is reported rather than left as a zombie nobody asked about.
	go func() { run.done <- command.Wait() }()
	return run, nil
}

// reportStarted is what a person needs after `start -d`: it is up, here, or it died with a reason,
// and either way there is a log.
func reportStarted(run spawned, dir string, address string) {
	fmt.Printf("acornd: started pid %d on %s\n", run.pid, address)
	url := healthURL(address)
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-run.done:
			fmt.Println("acornd: it exited during boot")
			if err != nil {
				fmt.Printf("acornd: %v\n", err)
			}
			reportLastLogLine(dir)
			return
		default:
		}
		if url != "" && waitHealthy(url, 200*time.Millisecond) {
			fmt.Printf("acornd: up at %s\n", strings.TrimSuffix(url, "/healthz"))
			fmt.Printf("acornd: log %s\n", runLogPath(dir))
			return
		}
		if time.Now().After(deadline) {
			fmt.Println("acornd: still starting — the first boot fetches bun and the worker sources")
			fmt.Printf("acornd: log %s\n", runLogPath(dir))
			return
		}
	}
}

// healthURL is where to knock to know a run is answering. A wildcard bind is reached on loopback:
// 0.0.0.0 is an instruction to the kernel, not an address a client can dial.
func healthURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	// An all-interfaces IPv6 bind is reached on its own loopback, which is not the v4 one.
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// waitHealthy polls the one endpoint that answers before a workspace, a worker or a model exists.
func waitHealthy(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// processAlive answers "is there a process with this pid", and treats EPERM as alive: a process
// that exists and belongs to somebody else is not gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processLooksLikeAcornd guards the one destructive thing in this file. A record can outlive its
// process, and the pid it names can be reused; killing by number alone is how a tool kills a
// stranger.
func processLooksLikeAcornd(pid int) bool {
	command, ok := processCommand(pid)
	if !ok {
		return false
	}
	first := command
	if field := strings.Fields(command); len(field) > 0 {
		first = field[0]
	}
	return strings.Contains(filepath.Base(first), "acornd")
}

// processCommand is what the process was started as: /proc where there is one, ps everywhere else.
func processCommand(pid int) (string, bool) {
	if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")); err == nil {
		fields := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		return strings.Join(fields, " "), true
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", false
	}
	command := strings.TrimSpace(string(out))
	return command, command != ""
}

// waitGone waits for a pid to stop existing, so stop and restart report an outcome and not an
// intention.
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

// withoutDetach is the child's arguments: the same run, minus the request to detach, which it has
// already honoured by being a child.
func withoutDetach(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			continue
		}
		out = append(out, arg)
	}
	return out
}
