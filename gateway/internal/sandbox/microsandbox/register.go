// Package microsandbox is the microVM engine: libkrun microVMs with a command line for the
// lifecycle and an SDK for execution. See docs/adr/0012-sandbox.md.
package microsandbox

import "github.com/marcelowa/acornd/gateway/internal/sandbox"

// Register adds the engine to sandbox.Engine's table. The entry point calls it before anything can
// choose an engine; a package that registered itself in init would decide that before main, which
// is house rule G3. It is idempotent.
func Register() {
	sandbox.RegisterDriver("microsandbox", func(dataDir, state string) sandbox.Driver {
		driver := NewMicrosandbox(dataDir)
		if state != "" {
			driver = driver.WithStateDir(state)
		}
		return driver
	})
}
