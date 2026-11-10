// Package youki is the OCI-runtime engine: a sandbox whose namespaces, mounts and policy are an
// OCI configuration rather than an argv. It is the engine for a machine without a hypervisor, and
// the only one of the three that can state a policy rather than only drop it. See ADR 0022 and
// issue #47.
package youki

import "github.com/marcelowa/acornd/gateway/internal/sandbox"

// Register adds the engine to sandbox.Engine's table. The entry point calls it before anything can
// choose an engine; a package that registered itself in init would decide that before main, which
// is house rule G3. It is idempotent.
func Register() {
	sandbox.RegisterDriver("youki", func(dataDir, state string) sandbox.Driver {
		driver := NewYouki(dataDir)
		if state != "" {
			driver = driver.WithStateDir(state)
		}
		return driver
	})
}
