package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/remote"
)

func TestRunRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	record := runRecord{PID: 4242, Args: []string{"start", "--host", "127.0.0.1"}, Address: "127.0.0.1:7777", Detached: true}
	if err := writeRunRecord(dir, record); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readRunRecord(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.PID != record.PID || got.Address != record.Address || !got.Detached {
		t.Fatalf("got %+v, want %+v", got, record)
	}
	if got.Started == "" {
		t.Fatal("the record has no start time")
	}
	if len(got.Args) != 3 || got.Args[2] != "127.0.0.1" {
		t.Fatalf("args did not survive: %v", got.Args)
	}
	info, err := os.Stat(runRecordPath(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode is %v, want 0600", info.Mode().Perm())
	}
}

func TestReadRunRecordWithoutOne(t *testing.T) {
	if _, err := readRunRecord(t.TempDir()); !errors.Is(err, errNoRecord) {
		t.Fatalf("got %v, want errNoRecord", err)
	}
}

func TestStopRefusesAProcessThatIsNotAcornd(t *testing.T) {
	dir := t.TempDir()
	command := exec.Command("/bin/sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if err := writeRunRecord(dir, runRecord{PID: command.Process.Pid, Address: "127.0.0.1:7777"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := stopRun(dir); err == nil {
		t.Fatal("stop killed a process that is not acornd")
	}
	if !processAlive(command.Process.Pid) {
		t.Fatal("the stranger was killed anyway")
	}
	if _, err := readRunRecord(dir); !errors.Is(err, errNoRecord) {
		t.Fatal("the stale record was left behind")
	}
}

func TestStopStopsTheRecordedRun(t *testing.T) {
	dir := t.TempDir()
	// A process whose command line reads `.../acornd`: the guard checks the name, and the name is
	// what this test is about.
	link := filepath.Join(dir, "acornd")
	if err := os.Symlink("/bin/sleep", link); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	command := exec.Command(link, "30")
	if err := command.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	reaped := make(chan struct{})
	go func() { _ = command.Wait(); close(reaped) }()
	if err := writeRunRecord(dir, runRecord{PID: command.Process.Pid, Address: "127.0.0.1:7777"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := stopRun(dir); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not exit")
	}
	if processAlive(command.Process.Pid) {
		t.Fatal("the process is still alive")
	}
	if _, err := readRunRecord(dir); !errors.Is(err, errNoRecord) {
		t.Fatal("stop left the record behind")
	}
}

func TestStopWithoutARecordIsNotAFailure(t *testing.T) {
	if err := stopRun(t.TempDir()); err != nil {
		t.Fatalf("stop with nothing running: %v", err)
	}
}

func TestLastLogLine(t *testing.T) {
	dir := t.TempDir()
	path := runLogPath(dir)
	if _, ok := lastLogLine(path); ok {
		t.Fatal("a log that does not exist answered")
	}
	if err := os.WriteFile(path, []byte("first\nsecond\n\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, ok := lastLogLine(path)
	if !ok || line != "second" {
		t.Fatalf("got %q, %v; want second", line, ok)
	}
}

func TestWithoutDetach(t *testing.T) {
	for _, test := range []struct{ in, want []string }{
		{[]string{"-d", "--host", "127.0.0.1"}, []string{"--host", "127.0.0.1"}},
		{[]string{"--detach", "--port", "7778"}, []string{"--port", "7778"}},
		{[]string{"--host", "127.0.0.1"}, []string{"--host", "127.0.0.1"}},
	} {
		got := withoutDetach(test.in)
		if len(got) != len(test.want) {
			t.Fatalf("withoutDetach(%v) = %v, want %v", test.in, got, test.want)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Fatalf("withoutDetach(%v) = %v, want %v", test.in, got, test.want)
			}
		}
	}
}

func TestHealthURLReachesAWildcardBindOnLoopback(t *testing.T) {
	for _, test := range []struct{ address, want string }{
		{"0.0.0.0:7777", "http://127.0.0.1:7777/healthz"},
		{"127.0.0.1:7777", "http://127.0.0.1:7777/healthz"},
		{"[::]:7777", "http://[::1]:7777/healthz"},
		{"", ""},
	} {
		if got := healthURL(test.address); got != test.want {
			t.Fatalf("healthURL(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

// Where a run binds follows the door now: an explicit --host wins, remote access means loopback and Cloudflare,
// and an install with no door stays reachable on the LAN so it can be configured at all (ADR 0036 §5).
func TestListenHostFollowsTheDoor(t *testing.T) {
	configured := t.TempDir()
	if err := remote.Save(configured, remote.Setup{
		Desired: remote.Desired{Hostname: "console.example.com", ZoneID: "z", AccountID: "a"},
	}, time.Now()); err != nil {
		t.Fatalf("save the door: %v", err)
	}
	cases := []struct{ name, explicit, dir, want string }{
		{"explicit wins", "192.0.2.1", configured, "192.0.2.1"},
		{"explicit loopback wins", "127.0.0.1", t.TempDir(), "127.0.0.1"},
		{"a door is loopback", "", configured, "127.0.0.1"},
		{"no door is every interface", "", t.TempDir(), "0.0.0.0"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := listenHost(test.explicit, test.dir)
			if err != nil {
				t.Fatalf("listen host: %v", err)
			}
			if got != test.want {
				t.Errorf("listenHost(%q) = %q, want %q", test.explicit, got, test.want)
			}
		})
	}
}

// A record that cannot be read is an error rather than a guess: binding every interface because the file is
// broken would be an open console where the person meant a closed one.
func TestListenHostRefusesAnUnreadableDoor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, remote.File), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if host, err := listenHost("", dir); err == nil {
		t.Errorf("an unreadable remote.json resolved to %q", host)
	}
}
