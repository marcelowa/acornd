package control

import (
	"os"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox/bwrap"
	"github.com/marcelowa/acornd/gateway/internal/sandbox/microsandbox"
)

// The drivers register from the entry point, and a test is its own entry point: without this the
// tier these tests open has the names but no factories.
func TestMain(m *testing.M) {
	bwrap.Register()
	microsandbox.Register()
	os.Exit(m.Run())
}
