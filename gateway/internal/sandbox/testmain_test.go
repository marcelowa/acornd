package sandbox

import (
	"os"
	"testing"
)

// registeredDriver is the shared tests' stand-in for a real engine. The drivers live in their own
// packages and import this one, so this package's tests cannot import them back; registering a
// fake under the real names gives Tier and Engine a table to answer from without the cycles.
type registeredDriver struct {
	fakeDriver
	name  string
	state string
}

func (d *registeredDriver) Name() string     { return d.name }
func (d *registeredDriver) StateDir() string { return d.state }

func TestMain(m *testing.M) {
	RegisterDriver("bwrap", func(_, state string) Driver {
		return &registeredDriver{name: "bwrap", state: state}
	})
	RegisterDriver("microsandbox", func(_, state string) Driver {
		return &registeredDriver{name: "microsandbox", state: state}
	})
	os.Exit(m.Run())
}
