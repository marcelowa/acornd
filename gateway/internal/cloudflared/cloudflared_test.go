package cloudflared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The pin is the point: a payload that is not the pinned binary is refused, and nothing is installed. Without
// this the connector would be whatever the network handed it, holding the tunnel's token.
func TestAPayloadThatIsNotThePinnedBinaryIsRefused(t *testing.T) {
	if runtime.GOOS+"/"+runtime.GOARCH != "linux/amd64" {
		t.Skip("the pin is linux/amd64; the refusal for everything else has its own test")
	}
	dir := t.TempDir()
	_, err := Ensure(context.Background(), dir, func(context.Context, string) ([]byte, error) {
		return []byte("not cloudflared"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to install") {
		t.Fatalf("an unpinned payload was accepted: %v", err)
	}
	if _, statErr := os.Stat(Path(dir)); statErr == nil {
		t.Error("the refused payload was written anyway")
	}
}

// A platform with no pin is refused before any network call, and the refusal says what to do about it.
func TestAPlatformWithNoPinIsRefusedWithoutFetching(t *testing.T) {
	if runtime.GOOS+"/"+runtime.GOARCH == "linux/amd64" {
		t.Skip("this platform is pinned; the refusal cannot be reached here")
	}
	fetched := false
	_, err := Ensure(context.Background(), t.TempDir(), func(context.Context, string) ([]byte, error) {
		fetched = true
		return nil, nil
	})
	if !errors.Is(err, ErrUnpinned) {
		t.Fatalf("err = %v, want ErrUnpinned", err)
	}
	if fetched {
		t.Error("a platform with no pin went to the network")
	}
}

// A binary already installed and matching the pin is used without downloading it again: the connector is
// checked at every boot, and a boot that re-downloads forty megabytes is a boot that needs the network.
func TestAnInstalledBinaryIsVerifiedRatherThanRefetched(t *testing.T) {
	if runtime.GOOS+"/"+runtime.GOARCH != "linux/amd64" {
		t.Skip("the pin is linux/amd64")
	}
	dir := t.TempDir()
	path := Path(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The file has to match the pin, so this test writes the pinned payload's hash and then the same bytes:
	// what it proves is the "no fetch" branch, not the download.
	payload := []byte("pinned bytes")
	if err := os.WriteFile(path, payload, 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	sum := sha256.Sum256(payload)
	previous, had := sums["linux/amd64"]
	sums["linux/amd64"] = hex.EncodeToString(sum[:])
	defer func() {
		if had {
			sums["linux/amd64"] = previous
		}
	}()
	fetched := false
	got, err := Ensure(context.Background(), dir, func(context.Context, string) ([]byte, error) {
		fetched = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got != path {
		t.Errorf("returned %q, want %q", got, path)
	}
	if fetched {
		t.Error("an installed binary was downloaded again")
	}
}

// The supervisor restarts what dies, waits longer each time it dies too soon, and stops when it is told to.
// A connector that gave up after one crash would be a tunnel that goes quiet on a bad night.
func TestTheSupervisorRestartsWithBackoffAndStops(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nexit 3\n")
	supervisor := &Supervisor{Path: script, Base: 10 * time.Millisecond, Max: 40 * time.Millisecond, Healthy: time.Hour}
	var lines []string
	var mu sync.Mutex
	supervisor.Log = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := supervisor.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 {
		t.Fatalf("the connector was not restarted: %v", lines)
	}
	// Each wait is longer than the one before, until the ceiling.
	if !strings.Contains(lines[0], "10ms") || !strings.Contains(lines[1], "20ms") {
		t.Errorf("the backoff did not grow: %v", lines[:2])
	}
	if !strings.Contains(strings.Join(lines, " "), "40ms") {
		t.Errorf("the backoff did not reach its ceiling in %v", lines)
	}
}

// A run that lasted a while resets the wait: the next death is news, not a loop, and hammering a restart every
// ten milliseconds is how a machine with a real problem never gets a moment to recover.
func TestALongRunResetsTheBackoff(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nsleep 0.08\nexit 1\n")
	supervisor := &Supervisor{Path: script, Base: 10 * time.Millisecond, Max: time.Minute, Healthy: 50 * time.Millisecond}
	var mu sync.Mutex
	var lines []string
	supervisor.Log = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := supervisor.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 {
		t.Fatalf("the connector did not run twice: %v", lines)
	}
	// Every wait is the base: a run that lasted longer than "healthy" starts the count over.
	for _, line := range lines {
		if !strings.Contains(line, "restarting in 10ms") {
			t.Fatalf("a long run did not reset the wait: %v", lines)
		}
	}
}

// The token goes in the environment, never in the command line: argv is readable by every process on the
// machine, and this one is a key to the door.
func TestTheTokenIsNotInTheCommandLine(t *testing.T) {
	supervisor := &Supervisor{Path: "/usr/bin/false", Token: "secret-token-value"}
	command := supervisor.command(context.Background())
	for _, arg := range command.Args {
		if strings.Contains(arg, "secret-token-value") {
			t.Fatalf("the token is in argv: %v", command.Args)
		}
	}
	found := false
	for _, entry := range command.Env {
		if entry == "TUNNEL_TOKEN=secret-token-value" {
			found = true
		}
	}
	if !found {
		t.Errorf("the token is not in the environment: %v", command.Env)
	}
}

// The line a person needs is one line: "it is up". A connector that repeats it on every reconnect fills the
// log with the same good news and hides the bad.
func TestTheConnectedLineIsSaidOnce(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\necho 'INF Registered tunnel connection connIndex=0'\necho 'INF Registered tunnel connection connIndex=1'\nsleep 0.1\nexit 1\n")
	var mu sync.Mutex
	var lines []string
	supervisor := &Supervisor{Path: script, Command: func(ctx context.Context, path, token string) *exec.Cmd {
		return exec.CommandContext(ctx, path)
	}, Log: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := supervisor.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, line := range lines {
		if line == "tunnel: connected" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("said %q %d times, want once: %v", "tunnel: connected", count, lines)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}
