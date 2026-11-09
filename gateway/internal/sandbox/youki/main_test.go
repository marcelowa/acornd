package youki

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/netns"
	"github.com/marcelowa/acornd/gateway/internal/supervise"
)

// TestMain lets this binary be the sandbox's supervisor and the netns helper, as the bwrap
// suite's binary is: a live youki test binds the executable it is running into the sandbox, and a
// test binary that ignored the arguments would start a second test run inside the sandbox.
func TestMain(m *testing.M) {
	Register()
	if len(os.Args) > 1 && os.Args[1] == "help" {
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "internal" && os.Args[2] == "netns" {
		command, err := netns.Command(os.Args[3:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "acornd test binary: netns:", err)
			os.Exit(2)
		}
		if err := syscall.Exec(command[0], command, os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, "acornd test binary: netns:", err)
			os.Exit(2)
		}
	}
	if len(os.Args) > 2 && os.Args[1] == "internal" && os.Args[2] == "sandbox-supervisor" {
		socket := ""
		for index, argument := range os.Args {
			if argument == "--socket" && index+1 < len(os.Args) {
				socket = os.Args[index+1]
			}
		}
		if socket != "" {
			_ = supervise.New(socket, "test", 0).Serve(context.Background())
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
