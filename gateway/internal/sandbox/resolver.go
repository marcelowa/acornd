package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// What a sandbox reads for DNS is not a detail of the engine: both container engines give their
// sandbox a rootfs of its own, and a resolver that works on the machine can still point at
// 127.0.0.53, which inside the sandbox is the sandbox's own loopback. ADR 0023 measured the
// symptom — an `apt-get update` that works on the host and fails inside — so the rule lives here
// once rather than once per engine.

// ResolverFiles is where a machine keeps its resolver configuration: the file in use, and the one
// its upstream servers are written to when a local resolver owns 127.0.0.53.
type ResolverFiles struct {
	Host     string
	Upstream string
}

// resolveResolv decides what a sandbox should read for DNS.
//
// A workspace granted private networks reads the machine's resolver, because it can reach it. One
// without the grant cannot: the machine's resolver is often an overlay address (Tailscale's
// MagicDNS at 100.100.100.100, systemd-resolved at 127.0.0.53), which is on the list of ranges the
// sandbox refuses. That sandbox reads a file naming pasta, which forwards the query to the machine's
// resolver from the machine's side — the sandbox never needs a route into anything private, and a
// name still resolves.
func ResolveResolvFor(workspaceDir string, files ResolverFiles, mode string, private bool) (string, error) {
	if mode != "pasta" || private {
		return resolverFileFor(files, workspaceDir)
	}
	path := filepath.Join(workspaceDir, "pasta-resolv.conf")
	content := "# written by acornd: pasta forwards these queries to the machine's resolver\n" +
		"nameserver " + SandboxDNS + "\n"
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// resolverFile decides what a sandbox should read for DNS when the machine's own resolver is not
// necessarily reachable from inside it.
//
// When the machine resolves through a loopback address — systemd-resolved at 127.0.0.53, which is
// what most distributions do — a sandbox cannot use it: 127.0.0.1 inside a sandbox is the
// sandbox's own loopback, where nothing listens. The machine's upstream servers are in a file of
// their own, so that is what gets bound; everything else binds the machine's file unchanged.
func resolverFileFor(files ResolverFiles, workspaceDir string) (string, error) {
	raw, err := os.ReadFile(files.Host)
	if err != nil {
		// No resolver file to read: binding the machine's path is what the sandbox did before, and
		// a sandbox with no DNS says so when a command tries to resolve something.
		return "", nil
	}
	if !hasLoopbackServers(string(raw)) {
		return "", nil
	}
	upstream, err := os.ReadFile(files.Upstream)
	if err != nil {
		return "", nil
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(workspaceDir, "resolv.conf")
	if err := os.WriteFile(path, upstream, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// hasLoopbackServers reports whether a resolver file names an address that only means something
// on the machine itself: 127.0.0.0/8 and ::1.
func hasLoopbackServers(contents string) bool {
	for line := range strings.SplitSeq(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		address := fields[1]
		if strings.HasPrefix(address, "127.") || address == "::1" {
			return true
		}
	}
	return false
}
