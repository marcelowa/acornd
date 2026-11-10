// Package bwrap is the bubblewrap engine: user, mount, pid, network, ipc and uts namespaces built
// from an argv. See docs/adr/0023-bwrap-engine.md.
package bwrap

import "github.com/marcelowa/acornd/gateway/internal/sandbox"

// Register adds the engine to sandbox.Engine's table. The entry point calls it before anything can
// choose an engine; a package that registered itself in init would decide that before main, which
// is house rule G3. It is idempotent.
func Register() {
	sandbox.RegisterDriver("bwrap", func(dataDir, state string) sandbox.Driver {
		driver := NewBwrap(dataDir)
		if state != "" {
			driver = driver.WithStateDir(state)
		}
		return driver
	})
}
