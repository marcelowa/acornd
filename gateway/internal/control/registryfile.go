package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/marcelowa/acornd/gateway/internal/layout"
)

// RegistryFile is the workspaces registry as it exists on disk. It has one admin so the
// server and the command line cannot disagree about its shape.
//
// WorkspacesDir is recorded here rather than in the global config because it belongs to the
// registry, and because that config is what shapes prompts: a control-plane path there
// would be a concern in the wrong file.
type RegistryFile struct {
	// Path is where it was read from; not part of the file.
	Path string `json:"-"`
	// dropped says the file mentioned keys this build no longer writes, so the caller can
	// rewrite it. Not part of the file either.
	dropped       bool
	WorkspacesDir string      `json:"workspacesDir,omitempty"`
	Workspaces    []Workspace `json:"workspaces"`
}

// Registry is the registry file, and the one way to write it. Two processes write it: a running gateway, and
// `acornd workspace` from a terminal. Each used to write the copy it had read, so the gateway's next write erased
// a workspace the terminal had added while it ran, with no error (#69). A write here takes a lock, reads the file
// again, and changes what is on disk.
type Registry struct {
	path string
}

// NewRegistry is the registry of the install whose acornd folder is dataDir.
func NewRegistry(dataDir string) Registry {
	return Registry{path: layout.Registry(dataDir)}
}

// Load reads the registry, tolerating absence. It takes no lock: a write replaces the file with a rename, so a
// reader sees the old file or the new one, never part of each.
func (r Registry) Load() (RegistryFile, error) {
	registry := RegistryFile{Path: r.path}
	payload, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	if err := json.Unmarshal(payload, &registry); err != nil {
		return registry, fmt.Errorf("%s: %w", r.path, err)
	}
	registry.Path = r.path
	registry.dropped = droppedKeys(payload)
	return registry, nil
}

// Change reads the registry under the lock, lets change edit it, and writes the result before the lock goes. An
// error from change writes nothing.
func (r Registry) Change(change func(*RegistryFile) error) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	release, err := layout.Lock(r.path)
	if err != nil {
		return err
	}
	defer release()
	registry, err := r.Load()
	if err != nil {
		return err
	}
	if err := change(&registry); err != nil {
		return err
	}
	return r.save(registry)
}

// save writes the registry atomically and privately.
func (r Registry) save(registry RegistryFile) error {
	sort.Slice(registry.Workspaces, func(i, j int) bool { return registry.Workspaces[i].Name < registry.Workspaces[j].Name })
	payload, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return layout.WriteFile(r.path, append(payload, '\n'))
}

// droppedKeys reports whether the file on disk mentions a key this build no longer writes.
//
// A workspace used to carry `trusted`. Nothing reads it any more, and a stale value that
// nothing reads is a value something can read again by accident — so the first load that sees
// one rewrites the file without it, rather than leaving it there for a later version to
// rediscover and mistrust.
func droppedKeys(payload []byte) bool {
	var raw struct {
		Workspaces []map[string]json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return false
	}
	for _, record := range raw.Workspaces {
		if _, found := record["trusted"]; found {
			return true
		}
	}
	return false
}
