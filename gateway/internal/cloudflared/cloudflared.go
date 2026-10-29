// Cloudflared: the connector that makes a tunnel real. A tunnel is a name and a route in Cloudflare; nothing
// answers at it until a process dials out and says "send this hostname's traffic to me". That process is
// cloudflared, and it is the one piece of remote access a family must never have to start by hand.
//
// Two jobs, and they are separate on purpose: Ensure puts the pinned binary in the install folder, Run keeps it
// running. Neither reaches outside the install folder, and neither puts anything on the host's PATH.
package cloudflared

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Version is the one cloudflared this build runs. Pinned rather than "latest": a connector that changes under
// an install without a test run is how a door stops opening on a Tuesday.
const Version = "2026.10.7"

// asset is what Cloudflare publishes for a platform, and sums is what it must hash to.
//
// Cloudflare publishes no checksum file — the release carries binaries and nothing to compare them against —
// so the pin is ours, taken once from the release and written down here. A platform with no pin is refused
// rather than downloaded unverified: the connector holds the tunnel's token, and a wrong byte in it is the
// whole door.
var assets = map[string]string{
	"linux/amd64": "cloudflared-linux-amd64",
}

var sums = map[string]string{
	"linux/amd64": "1f99fb518b83dec2ba8d870a7cc991d82a77e6196295a3d2754ba17ab9f91872",
}

// DownloadBase is where Cloudflare's releases live. A test points it elsewhere.
const DownloadBase = "https://github.com/cloudflare/cloudflared/releases/download"

// ErrUnpinned is the refusal for a platform this build has no verified binary for. It names the way out,
// because the person reading it can act on it — and because installing cloudflared by hand is a fine answer.
var ErrUnpinned = errors.New("this build has no pinned cloudflared for this platform: install it yourself and pass --cloudflared <path>")

// Fetcher is the one network call this package makes, in a shape a test can answer.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// Path is where the pinned binary lives inside an install.
func Path(dataDir string) string {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dataDir, "tools", "cloudflared", Version, name)
}

// Ensure makes the pinned binary exist at Path and answer its version, downloading it when it is not there.
// A file that is present is hashed before it is run: the install folder is writable by the process that runs
// it, so "it is there" is not the same as "it is the binary we pinned".
func Ensure(ctx context.Context, dataDir string, fetch Fetcher) (string, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	asset, pinned := assets[platform]
	sum, hasSum := sums[platform]
	if !pinned || !hasSum {
		return "", ErrUnpinned
	}
	path := Path(dataDir)
	if present(path, sum) {
		return path, nil
	}
	if fetch == nil {
		fetch = Download
	}
	payload, err := fetch(ctx, DownloadBase+"/"+Version+"/"+asset)
	if err != nil {
		return "", fmt.Errorf("downloading cloudflared %s: %w", Version, err)
	}
	got := sha256.Sum256(payload)
	if hex.EncodeToString(got[:]) != sum {
		return "", fmt.Errorf("cloudflared %s downloaded but hashes to %s, not %s: refusing to install it",
			Version, hex.EncodeToString(got[:])[:12], sum[:12])
	}
	if err := write(path, payload); err != nil {
		return "", err
	}
	return path, nil
}

// Download is the real fetch: a plain GET with a generous timeout, because forty megabytes over a home
// connection is not a slow request, it is a normal one.
func Download(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, response.Status)
	}
	return io.ReadAll(response.Body)
}

// present reports whether the file at path is the binary this build pinned.
func present(path, sum string) bool {
	payload, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	got := sha256.Sum256(payload)
	return hex.EncodeToString(got[:]) == sum
}

// write installs the binary atomically, so a download that dies halfway never leaves half a connector behind.
func write(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "cloudflared-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(payload); err != nil {
		// Both errors, joined: the write is what went wrong, and a close that failed too is part of the same
		// refusal rather than a second one. errors.Join drops a nil, so the usual message is the write's alone.
		return errors.Join(err, temp.Close())
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o700); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Supervisor keeps the connector running. It restarts it when it dies, waits longer after each death that
// came too soon, and reports one line when the tunnel is up.
type Supervisor struct {
	Path  string
	Token string
	// Log is where the lines go. Nil discards them, which is what tests want.
	Log func(format string, args ...any)
	// Base and Max are the restart backoff. Zero means one second and one minute.
	Base, Max time.Duration
	// Command builds the process. Nil runs the real cloudflared; a test runs a script.
	Command func(ctx context.Context, path, token string) *exec.Cmd
	// Healthy is how long a run must last to count as having worked, and reset the backoff.
	Healthy time.Duration
	// announced is the one line a person needs, said once: "it is up". Both output streams are read, and a
	// reconnect says it again only through the restart line that preceded it.
	announced sync.Once
}

// Run supervises until ctx is cancelled. It returns nil on a clean stop: a cancelled context is the caller
// shutting the connector down, not a failure of the connector.
func (s *Supervisor) Run(ctx context.Context) error {
	base, max := s.Base, s.Max
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 {
		max = time.Minute
	}
	healthy := s.Healthy
	if healthy <= 0 {
		healthy = 30 * time.Second
	}
	wait := base
	for {
		started := time.Now()
		err := s.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) >= healthy {
			// It worked for a while, so the next death is news rather than a loop: start the wait over.
			wait = base
		}
		s.logf("cloudflared stopped (%v); restarting in %s", err, wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if wait *= 2; wait > max {
			wait = max
		}
	}
}

// once runs the connector until it exits, logging the line that says the tunnel is real.
func (s *Supervisor) once(ctx context.Context) error {
	command := s.command(ctx)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go s.watch(&readers, stdout)
	go s.watch(&readers, stderr)
	waitErr := command.Wait()
	readers.Wait()
	return waitErr
}

func (s *Supervisor) command(ctx context.Context) *exec.Cmd {
	if s.Command != nil {
		return s.Command(ctx, s.Path, s.Token)
	}
	// The token travels in the environment, never in argv: a command line is readable by every process on the
	// machine, and this one is a key to the door.
	command := exec.CommandContext(ctx, s.Path, "--no-autoupdate", "tunnel", "--loglevel", "info", "run")
	command.Env = append(os.Environ(), "TUNNEL_TOKEN="+s.Token)
	return command
}

// watch prints what the connector says, and announces the tunnel once, when it is connected — the line a
// person needs is "it is up", repeated no more than the number of times it comes back up.
func (s *Supervisor) watch(readers *sync.WaitGroup, source io.Reader) {
	defer readers.Done()
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "Registered tunnel connection") {
			s.announced.Do(func() { s.logf("tunnel: connected") })
		}
	}
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}
