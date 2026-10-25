// Package sandbox keeps every model-driven touch of the world inside one microVM per
// workspace: commands, file reads, file writes, and the workspace's own resource files.
// See docs/adr/0012-sandbox.md for the decision and the measurements behind it.
//
// Control owns the sandbox's life (this package: install the engine, create, start, stop,
// remove) and the worker owns execution inside it (the engine's SDK, in worker/src/sandbox).
// The two speak to the same engine and the same state, which is why one side can be Go and
// the other JavaScript.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Policy is a workspace's intent for its sandbox. It says what the workspace needs; how that
// becomes an engine's configuration is the driver's business, which is why the engine's own
// vocabulary does not appear here. It is the whole policy: what the sandbox
// is made of, how big it may get, whether it may reach the network, whether it may nest
// virtual machines, and which host folder it may see. The engine reads it as sandbox.yaml.
type Policy struct {
	// Image is the project's environment — Go, Rust, Python, Node, Ruby, whatever the work
	// needs. It is deliberately not the worker's runtime: the worker is not in here.
	Image string
	// CPUs and Memory bound the sandbox. Zero values take the defaults.
	CPUs   int
	Memory string
	// DisableVMNesting is whether this workspace must not nest virtual machines inside its
	// sandbox. False (the default) allows nesting wherever the engine offers it and this
	// machine can grant it; true refuses it, and the engine is told so explicitly where
	// it has the switch. A sandbox on a machine that cannot nest still boots. See
	// docs/adr/0018-disable-vm-nesting.md.
	DisableVMNesting bool
	// Devices grants a bwrap sandbox the host's /dev/net/tun and /dev/kvm, and the
	// capabilities nested pasta needs. It is what lets a workspace run another acornfall
	// honestly: the inner engine gets a real bwrap sandbox and a real network instead of
	// falling back to the host escape hatch. False by default at this layer; control fills
	// it from the workspace's own opt-out. See docs/adr/0030-nested-engines.md.
	Devices bool
	// OnStage reports a step that takes time — pulling an image, copying it, booting the engine —
	// so the interface can say what is happening instead of showing an empty pane. Nil means
	// nobody is listening, which is the case for a command or a test.
	OnStage func(step string)
	// Egress allows the sandbox to reach the network. On by default for new workspaces;
	// ADR 0012 records what that exposes.
	Egress bool
	// PrivateEgress lets the sandbox reach private networks: the machine's own LAN, its
	// link-local metadata address, and any overlay the machine is part of, such as a tailnet.
	// Off by default, and the difference is visible on the machine: with it off the sandbox is
	// given routes that refuse those ranges, because egress that carries the machine's routing
	// table reaches them as the machine otherwise. See docs/adr/0025-sandbox-egress.md.
	PrivateEgress bool
	// Workspace is the host folder mounted into the sandbox at the same path, which is what
	// makes a path mean the same thing on both sides.
	Workspace string
	// Global lists host resource folders the sandbox mounts read-only at their own paths: the
	// user's `~/.agents` bundle and the install's agents, skills, and prompt templates. A
	// skill's instructions can then name `scripts/check.sh` and have it be a command that
	// works. Read-only because editing them is self-modification, which belongs behind review
	// (issue #17). GlobalPaths builds it.
	Global []string
	// IdleTimeout stops a sandbox nobody is using. Empty takes the default.
	IdleTimeout string
	// Owned lists paths inside the workspace that belong to the sandbox rather than to the
	// user's folder, so that build artifacts cannot fight between host and guest. Not used
	// yet, deliberately: see ADR 0012, "Not doing now, on purpose".
	Owned []string
	// Secrets are the values a driver may deliver, already filtered for its capability. A
	// substitute engine writes source references into the policy file and keeps the values in
	// the create/modify process environment; an environment engine puts them in the sandbox.
	// See docs/adr/0021-a-vault-for-tool-secrets.md.
	Secrets []Secret
}

// Defaults, chosen to be unremarkable. The image matches this host's libc family so that
// artifacts already in the workspace can still run; the timeout is long enough that a
// workspace with dependencies installed is not rebuilt between two questions.
const (
	DefaultImage       = "debian:stable-slim"
	DefaultCPUs        = 2
	DefaultMemory      = "2G"
	DefaultIdleTimeout = "30m"
	// KeptAliveIdleTimeout is what a workspace that runs something tells the engine instead: a
	// daemon or a cron job must not be stopped for being idle, and this is long enough that it
	// never fires in practice while still being a number the engine understands. Control owns the
	// rule; this is how a policy expresses it to an engine that keeps a timer of its own.
	KeptAliveIdleTimeout = "720h"
)

func (p Policy) withDefaults() Policy {
	if strings.TrimSpace(p.Image) == "" {
		p.Image = DefaultImage
	}
	if p.CPUs <= 0 {
		p.CPUs = DefaultCPUs
	}
	if strings.TrimSpace(p.Memory) == "" {
		p.Memory = DefaultMemory
	}
	if strings.TrimSpace(p.IdleTimeout) == "" {
		p.IdleTimeout = DefaultIdleTimeout
	}
	return p
}

// ecosystemDir is the rest of the ecosystem's name for a resource bundle. The worker reads
// it as well as the install's own folders, so a bundle written for another harness works
// here, and the sandbox has to see the same paths the worker read the instructions from.
const ecosystemDir = ".agents"

// globalFolders are the install's resource folders, which exist in every install so that a
// sandbox's mount always resolves: the user's bundle in full, then the install's own. The
// prompt-template folder has two names because both are read.
func globalFolders(dataDir, userHome string) []string {
	folders := []string{filepath.Join(userHome, ecosystemDir)}
	for _, name := range []string{"agents", "skills", "prompts", "commands"} {
		folders = append(folders, filepath.Join(dataDir, name))
	}
	return folders
}

// GlobalPaths is what a workspace's sandbox mounts read-only at the same paths. A file is
// only mountable if it is there when the sandbox is created, so `<home>/config.json` is
// included when it exists and not otherwise; adding one later needs the sandbox recreated.
//
// `<home>` itself is deliberately absent: it holds credentials.json, token, and state/, and
// the agent has no business reading any of them. That is the whole reason the list is
// explicit. See docs/adr/0014-global-resources-in-the-sandbox.md.
func GlobalPaths(dataDir, userHome string) []string {
	paths := globalFolders(dataDir, userHome)
	config := filepath.Join(dataDir, "config.json")
	if info, err := os.Stat(config); err == nil && !info.IsDir() {
		paths = append(paths, config)
	}
	return paths
}

// EnsureGlobal creates the folders GlobalPaths names, so a fresh install has somewhere to
// mount from. It does not create config.json: an empty one would be a decision about
// defaults nobody made, and the mount is skipped while the file is absent.
func EnsureGlobal(dataDir, userHome string) error {
	for _, folder := range globalFolders(dataDir, userHome) {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", folder, err)
		}
	}
	return nil
}

// Name is the engine-level name for a workspace's sandbox. It is derived from the path so
// that two gateways pointed at one folder agree on which sandbox is which, and readable so
// that `msb ls` means something to a person.
func Name(workspace string) string {
	slug := slugify(base(workspace))
	return fmt.Sprintf("acornfall-%s-%s", slug, shortHash(workspace))
}

// shortHash tells two workspaces apart that end in the same folder name.
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func base(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return "workspace"
	}
	if _, after, ok := strings.CutLast(trimmed, "/"); ok {
		return after
	}
	return trimmed
}

// slugify keeps a name inside what the engine accepts and a human can read.
func slugify(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "ws"
	}
	return out
}

// Stage reports a step that takes time. It is nil-safe: a policy built by a caller with no
// interface attached says nothing.
func (p Policy) Stage(step string) {
	if p.OnStage != nil {
		p.OnStage(step)
	}
}
