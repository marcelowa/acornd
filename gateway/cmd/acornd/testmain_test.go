package main

import (
	"os"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox/bwrap"
	"github.com/marcelowa/acornd/gateway/internal/sandbox/microsandbox"
	"github.com/marcelowa/acornd/gateway/internal/sandbox/youki"
)

// The entry point registers every engine; a test is its own entry point, and doctor's tests ask
// the tier a real engine by name.
func TestMain(m *testing.M) {
	bwrap.Register()
	microsandbox.Register()
	youki.Register()
	os.Exit(m.Run())
}
